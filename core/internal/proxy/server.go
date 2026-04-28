package proxy

import (
	"context"
	"fmt"
	stdLog "log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/elazarl/goproxy"
)

// quietGoproxyLogger drops a small set of well-known goproxy log lines that
// fire whenever a client (browser/scraper) cancels a request mid-stream.
// Those events are normal during page navigation/timeouts and don't represent
// a real error in the proxy. Everything else is forwarded to the standard log.
type quietGoproxyLogger struct {
	inner *stdLog.Logger
}

var quietGoproxyDrops = []string{
	"Error copying to client",
	"An existing connection was forcibly closed by the remote host",
	"connection was aborted by the software in your host machine",
	"use of closed network connection",
	"broken pipe",
	"wsarecv",
	"wsasend",
}

func (q *quietGoproxyLogger) Printf(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	for _, drop := range quietGoproxyDrops {
		if strings.Contains(msg, drop) {
			return
		}
	}
	q.inner.Output(2, msg) //nolint:errcheck // logger never errors
}

// Server represents the proxy server
type Server struct {
	proxy          *goproxy.ProxyHttpServer
	server         *http.Server
	logger         *logger.Logger
	port           int
	selector       ProxySelector
	tracker        *UsageTracker
	handler        *UpstreamProxyHandler
	authMiddleware *AuthMiddleware
	rateLimitMw    *RateLimitMiddleware
	proxyRepo      *repository.ProxyRepository
	settingsRepo   *repository.SettingsRepository
	refreshTicker  *time.Ticker
	cleanupTicker  *time.Ticker
	stopChan       chan struct{}
}

// New creates a new proxy server instance
func New(
	port int,
	log *logger.Logger,
	proxyRepo *repository.ProxyRepository,
	settingsRepo *repository.SettingsRepository,
	assignmentRepo *repository.AssignmentRepository,
) (*Server, error) {
	// Load settings
	ctx := context.Background()
	settings, err := settingsRepo.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %w", err)
	}

	// Create proxy selector based on rotation settings
	selector, err := NewProxySelector(proxyRepo, &settings.Rotation)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy selector: %w", err)
	}

	// Initial refresh of proxy list
	if err := selector.Refresh(ctx); err != nil {
		log.Warn("no proxies available at startup - server will start but requests will fail until proxies are added", "error", err)
	} else {
		log.Info("proxy server initialized successfully")
	}

	// Create usage tracker
	tracker := NewUsageTracker(proxyRepo)

	// Create upstream proxy handler
	handler := NewUpstreamProxyHandler(selector, tracker, &settings.Rotation, log)
	handler.SetAssignmentRepo(assignmentRepo)

	// Create middlewares
	authMiddleware := NewAuthMiddleware(settings.Authentication)
	rateLimitMw := NewRateLimitMiddleware(settings.RateLimit)

	// Create goproxy instance
	proxyServer := goproxy.NewProxyHttpServer()
	proxyServer.Verbose = log.Logger.Enabled(context.Background(), -4) // Enable verbose if debug level

	// Replace goproxy's default stdlib logger with one that drops harmless
	// "client cancelled mid-stream" warnings. Real errors still pass through.
	proxyServer.Logger = &quietGoproxyLogger{
		inner: stdLog.New(os.Stderr, "", stdLog.LstdFlags),
	}

	// CRITICAL: Set ConnectDialWithReq so we get the original CONNECT request
	// (and its Proxy-Authorization header) for routing-aware proxy selection.
	// Scrapers using `proxy=main_machine_vm1:Taiwan@host:8006` get a sticky
	// proxy via AssignmentRepository.Checkout. Anything else falls back to
	// the existing random/round-robin selector.
	proxyServer.ConnectDialWithReq = func(req *http.Request, network string, addr string) (net.Conn, error) {
		if hints, ok := parseRoutingHints(req); ok && assignmentRepo != nil {
			ctx := context.Background()
			if req != nil && req.Context() != nil {
				ctx = req.Context()
			}
			picked, sticky, err := assignmentRepo.Checkout(ctx, hints.MachineID, hostOnly(addr), hints.Country)
			if err != nil {
				log.Warn("routed CONNECT failed at checkout",
					"source", "proxy",
					"addr", addr,
					"machine_id", hints.MachineID,
					"country", hints.Country,
					"error", err,
				)
				return nil, fmt.Errorf("checkout failed: %w", err)
			}
			log.Info("routed CONNECT",
				"source", "proxy",
				"addr", addr,
				"machine_id", hints.MachineID,
				"country", hints.Country,
				"proxy_id", picked.ID,
				"sticky", sticky,
			)
			return handler.ConnectThroughChosenProxy(picked, addr)
		}

		// Default path: random rotation, exactly as before.
		conn, _, err := handler.ConnectThroughProxyForDial(addr)
		if err != nil {
			log.Error("ConnectDial failed",
				"source", "proxy",
				"addr", addr,
				"error", err,
			)
			return nil, err
		}
		return conn, nil
	}

	// Setup handlers with middleware chain
	// Order: Auth -> RateLimit -> Handler

	// HTTP requests
	proxyServer.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// If the client encoded routing hints in Proxy-Authorization
		// (e.g. proxy=main_machine_vm1:Taiwan@host), skip the auth check —
		// those credentials are routing data, not auth.
		hints, hasHints := parseRoutingHints(req)
		if !hasHints {
			if req, resp := authMiddleware.HandleRequest(req, ctx); resp != nil {
				return req, resp
			}
		} else {
			// Drop the header so it never leaks to the upstream.
			stripProxyAuth(req)
			ctx.UserData = hints
		}

		// Rate limiting middleware
		if req, resp := rateLimitMw.HandleRequest(req, ctx); resp != nil {
			return req, resp
		}

		// Main handler
		return handler.HandleRequest(req, ctx)
	})

	// HTTPS CONNECT requests - middleware only (actual dial handled by ConnectDialWithReq above)
	proxyServer.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		// Same routing-hints bypass for the HTTPS path.
		hints, hasHints := parseRoutingHints(ctx.Req)
		if !hasHints {
			if _, resp := authMiddleware.HandleConnect(ctx.Req, ctx); resp != nil {
				ctx.Resp = resp
				return goproxy.RejectConnect, host
			}
		} else {
			ctx.UserData = hints
			// Note: we leave Proxy-Authorization on ctx.Req so
			// ConnectDialWithReq can read it, then it isn't forwarded — the
			// upstream CONNECT we send is built from scratch.
		}

		// Rate limiting middleware
		if _, resp := rateLimitMw.HandleConnect(ctx.Req, ctx); resp != nil {
			ctx.Resp = resp
			return goproxy.RejectConnect, host
		}

		// Allow CONNECT - actual connection will be made by ConnectDialWithReq
		return goproxy.OkConnect, host
	}))

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      proxyServer,
		ReadTimeout:  time.Duration(settings.Rotation.Timeout) * time.Second,
		WriteTimeout: time.Duration(settings.Rotation.Timeout) * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s := &Server{
		proxy:          proxyServer,
		server:         httpServer,
		logger:         log,
		port:           port,
		selector:       selector,
		tracker:        tracker,
		handler:        handler,
		authMiddleware: authMiddleware,
		rateLimitMw:    rateLimitMw,
		proxyRepo:      proxyRepo,
		settingsRepo:   settingsRepo,
		stopChan:       make(chan struct{}),
	}

	// Start background tasks
	s.startBackgroundTasks()

	return s, nil
}

// startBackgroundTasks starts periodic background tasks
func (s *Server) startBackgroundTasks() {
	// Refresh proxy list every 30 seconds
	s.refreshTicker = time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-s.refreshTicker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := s.selector.Refresh(ctx); err != nil {
					s.logger.Error("failed to refresh proxy list", "error", err)
				} else {
					s.logger.Info("proxy list refreshed")
				}
				cancel()
			case <-s.stopChan:
				return
			}
		}
	}()

	// Cleanup rate limiters every 5 minutes
	s.cleanupTicker = time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-s.cleanupTicker.C:
				s.rateLimitMw.CleanupLimiters()
				s.logger.Info("cleaned up rate limiters")
			case <-s.stopChan:
				return
			}
		}
	}()
}

// Start starts the proxy server
func (s *Server) Start() error {
	s.logger.Info("starting proxy server", "port", s.port)

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("proxy server failed: %w", err)
	}

	return nil
}

// Shutdown gracefully shuts down the proxy server
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down proxy server")

	// Stop background tasks
	close(s.stopChan)
	if s.refreshTicker != nil {
		s.refreshTicker.Stop()
	}
	if s.cleanupTicker != nil {
		s.cleanupTicker.Stop()
	}

	return s.server.Shutdown(ctx)
}

// ReloadSettings reloads settings from database and updates components
func (s *Server) ReloadSettings(ctx context.Context) error {
	settings, err := s.settingsRepo.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	// Update middleware settings
	s.authMiddleware.UpdateSettings(settings.Authentication)
	s.rateLimitMw.UpdateSettings(settings.RateLimit)

	// Update handler settings
	s.handler.settings = &settings.Rotation

	// Recreate selector if rotation method changed
	newSelector, err := NewProxySelector(s.proxyRepo, &settings.Rotation)
	if err != nil {
		return fmt.Errorf("failed to create new selector: %w", err)
	}

	if err := newSelector.Refresh(ctx); err != nil {
		return fmt.Errorf("failed to refresh new selector: %w", err)
	}

	s.selector = newSelector
	s.handler.selector = newSelector

	s.logger.Info("settings reloaded successfully")
	return nil
}
