package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"h12.io/socks"
	proxyDialer "golang.org/x/net/proxy"
)

// ProbeWorker is the background loop that drives the Recovery Test phase of
// the proxy lifecycle. Every tick it asks the ban repo for any (proxy,
// machine, domain) scopes whose next_probe_at has fired, then sends a real
// GET to the domain through that proxy, classifies the response, and feeds
// the verdict back into the ban repo's state machine.
type ProbeWorker struct {
	bans         *repository.BanRepository
	log          *logger.Logger
	interval     time.Duration
	batchSize    int
	probeTimeout time.Duration

	stop chan struct{}
	once sync.Once
}

// NewProbeWorker constructs a worker. interval is how often the queue is
// polled. batchSize bounds how many probes are sent per tick.
func NewProbeWorker(bans *repository.BanRepository, log *logger.Logger) *ProbeWorker {
	return &ProbeWorker{
		bans:         bans,
		log:          log,
		interval:     60 * time.Second,
		batchSize:    25,
		probeTimeout: 20 * time.Second,
		stop:         make(chan struct{}),
	}
}

// Start runs the worker until Stop is called. Non-blocking.
func (w *ProbeWorker) Start(ctx context.Context) {
	go w.loop(ctx)
}

// Stop signals the worker to exit at the end of the current tick.
func (w *ProbeWorker) Stop() {
	w.once.Do(func() { close(w.stop) })
}

func (w *ProbeWorker) loop(ctx context.Context) {
	w.log.Info("probe worker started",
		"source", "probe",
		"interval", w.interval.String(),
		"batch_size", w.batchSize,
	)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	// First tick immediately so we don't waste a minute on startup.
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *ProbeWorker) tick(ctx context.Context) {
	batch, err := w.bans.NextProbeBatch(ctx, w.batchSize)
	if err != nil {
		w.log.Warn("probe batch fetch failed", "source", "probe", "error", err)
		return
	}
	if len(batch) == 0 {
		return
	}
	w.log.Info("probing banned scopes",
		"source", "probe",
		"count", len(batch),
	)
	// Probe in parallel — these are I/O bound. Small fan-out so a single
	// machine's bad pool doesn't saturate the box.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, target := range batch {
		target := target
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			w.probeOne(ctx, target)
		}()
	}
	wg.Wait()
}

func (w *ProbeWorker) probeOne(ctx context.Context, t repository.ProbeTarget) {
	verdict := w.runProbe(ctx, t)

	recCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.bans.RecordProbeResult(recCtx, t.Scope, verdict.passed); err != nil {
		w.log.Warn("probe result write failed",
			"source", "probe",
			"scope", t.Scope.String(),
			"error", err,
		)
		return
	}

	if verdict.passed {
		w.log.Info("probe passed — proxy unbanned",
			"source", "probe",
			"scope", t.Scope.String(),
			"status_code", verdict.statusCode,
			"latency_ms", verdict.latencyMS,
		)
	} else {
		w.log.Info("probe failed — staying banned",
			"source", "probe",
			"scope", t.Scope.String(),
			"reason", verdict.reason,
			"status_code", verdict.statusCode,
			"latency_ms", verdict.latencyMS,
		)
	}
}

type probeVerdict struct {
	passed     bool
	reason     string
	statusCode int
	latencyMS  int
}

// runProbe decides whether a banned scope has recovered. The question it
// answers is "can the proxy REACH this destination, and is it being blocked?"
// — NOT "does this host serve a full HTML homepage". The old GET-and-expect-a-
// page approach false-failed every non-page endpoint (CDNs, APIs, trackers,
// and binary services like mtalk.google.com which speaks a non-HTTP protocol).
//
// Logic:
//  1. Try an HTTP GET. If we get a real HTTP response, PASS unless it carries
//     an explicit block signal (403/429, anti-bot marker, challenge redirect).
//  2. If the GET produced no usable HTTP response (transport error, or the
//     host speaks a non-HTTP protocol so the body can't be parsed), fall back
//     to a raw reachability check: can the proxy open a tunnel to host:443?
//     If yes, the proxy works — PASS. If not, it's genuinely unreachable — FAIL.
func (w *ProbeWorker) runProbe(ctx context.Context, t repository.ProbeTarget) probeVerdict {
	proxy := &models.Proxy{
		ID:       t.Scope.ProxyID,
		Address:  t.ProxyAddress,
		Protocol: t.ProxyProtocol,
		Username: t.ProxyUsername,
		Password: t.ProxyPassword,
	}
	host := t.Scope.TargetDomain

	// Step 1: HTTP GET for block detection (best effort).
	if transport, err := CreateProxyTransport(proxy); err == nil {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client := &http.Client{
			Transport: transport,
			Timeout:   w.probeTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		}
		if v, gotResp := w.blockCheck(ctx, client, "https://"+host+"/"); gotResp {
			return v
		}
	}

	// Step 2: no usable HTTP response — test raw reachability through the
	// proxy. A successful tunnel to host:port means the proxy can reach the
	// origin (e.g. mtalk.google.com:5228, which answers in binary, not HTTP).
	// Use the exact port the scraper reached on; fall back to 443.
	port := t.Scope.TargetPort
	if port == "" {
		port = "443"
	}
	start := time.Now()
	reachable, rerr := proxyCanReach(proxy, net.JoinHostPort(host, port), w.probeTimeout)
	latencyMS := int(time.Since(start).Milliseconds())
	if reachable {
		return probeVerdict{passed: true, reason: "reachable (non-HTTP endpoint)", latencyMS: latencyMS}
	}
	reason := "unreachable"
	if rerr != nil {
		reason = "unreachable: " + rerr.Error()
	}
	return probeVerdict{passed: false, reason: reason, latencyMS: latencyMS}
}

// blockCheck sends a GET and reports (verdict, gotResponse). gotResponse is
// false when no HTTP response could be read (transport error / non-HTTP host),
// signalling the caller to fall back to the raw reachability check.
func (w *ProbeWorker) blockCheck(ctx context.Context, client *http.Client, url string) (probeVerdict, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, w.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return probeVerdict{}, false
	}
	// Use a realistic UA — some sites will instantly block "Go-http-client".
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	start := time.Now()
	resp, err := client.Do(req)
	latencyMS := int(time.Since(start).Milliseconds())
	if err != nil {
		// No HTTP response — could be a non-HTTP host. Caller falls back.
		return probeVerdict{}, false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	verdict := classifyResponse(resp, body)
	verdict.statusCode = resp.StatusCode
	verdict.latencyMS = latencyMS
	return verdict, true
}

// proxyCanReach opens a tunnel to target ("host:port") through the proxy and
// closes it. A successful tunnel proves the proxy can reach the origin at the
// TCP level — independent of whether the origin speaks HTTP.
func proxyCanReach(p *models.Proxy, target string, timeout time.Duration) (bool, error) {
	conn, err := dialThroughProxy(p, target, timeout)
	if err != nil {
		return false, err
	}
	_ = conn.Close()
	return true, nil
}

// dialThroughProxy establishes a raw TCP tunnel to target through the proxy,
// mirroring the handler's connectViaProxy logic so the probe reaches hosts the
// same way live traffic does.
func dialThroughProxy(p *models.Proxy, target string, timeout time.Duration) (net.Conn, error) {
	switch p.Protocol {
	case "socks5":
		var auth *proxyDialer.Auth
		if p.Username != nil && *p.Username != "" {
			pw := ""
			if p.Password != nil {
				pw = *p.Password
			}
			auth = &proxyDialer.Auth{User: *p.Username, Password: pw}
		}
		d, err := proxyDialer.SOCKS5("tcp", p.Address, auth, proxyDialer.Direct)
		if err != nil {
			return nil, err
		}
		return d.Dial("tcp", target)
	case "socks4", "socks4a":
		return socks.Dial(socksURL(p))("tcp", target)
	case "http", "https", "":
		return connectViaHTTPProxyRaw(p, target, timeout)
	default:
		return nil, fmt.Errorf("unsupported proxy protocol: %s", p.Protocol)
	}
}

func socksURL(p *models.Proxy) string {
	proto := p.Protocol
	if proto == "" {
		proto = "socks4"
	}
	if p.Username != nil && *p.Username != "" {
		pw := ""
		if p.Password != nil {
			pw = *p.Password
		}
		return fmt.Sprintf("%s://%s:%s@%s", proto, *p.Username, pw, p.Address)
	}
	return fmt.Sprintf("%s://%s", proto, p.Address)
}

// connectViaHTTPProxyRaw sends a CONNECT to an HTTP proxy and returns the
// tunnelled connection if the proxy answered 200. Standalone copy of the
// handler's connectViaHTTPProxy (the probe worker has no handler instance).
func connectViaHTTPProxyRaw(p *models.Proxy, target string, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", p.Address, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial proxy: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, err
	}

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if p.Username != nil && *p.Username != "" {
		pw := ""
		if p.Password != nil {
			pw = *p.Password
		}
		enc := base64.StdEncoding.EncodeToString([]byte(*p.Username + ":" + pw))
		req += "Proxy-Authorization: Basic " + enc + "\r\n"
	}
	req += "User-Agent: ProxyMonitor-Probe/1.0\r\nProxy-Connection: Keep-Alive\r\n\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	statusLine := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	if !strings.Contains(statusLine, "200") {
		conn.Close()
		return nil, fmt.Errorf("CONNECT failed: %s", statusLine)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// antiBotMarkers is the generic v1 detector. If any of these substrings
// appears in the body OR the response is too small to be a real page, we
// consider the request blocked even when the status code is 200. Per-site
// validators (next iteration) will override this.
var antiBotMarkers = []string{
	// Cloudflare
	"just a moment...",
	"cf-challenge",
	"__cf_chl",
	"checking your browser",
	"cf-mitigated",
	// DataDome
	"datadome",
	"geo.captcha-delivery.com",
	"captcha-delivery.com",
	// PerimeterX / HUMAN
	"_pxaction",
	"px-captcha",
	"perimeterx",
	// Akamai Bot Manager
	"_abck",
	"akamai bot",
	// reCAPTCHA / hCaptcha widgets on a challenge page
	"g-recaptcha",
	"h-captcha",
	"grecaptcha.render",
	// Imperva / Incapsula
	"_incapsula_resource",
	"incapsula incident id",
	// Generic
	"access denied",
	"please verify you are human",
	"verify you are a human",
	"unusual traffic from your",
	"you have been blocked",
	"403 forbidden",
}

// classifyResponse interprets an HTTP response we DID manage to read. The proxy
// already reached the origin (we got a response), so the only thing that makes
// this a FAIL is an explicit block signal. A small body, a 404, a JSON blob, or
// a 5xx are all "reachable" — the proxy did its job, so those PASS.
func classifyResponse(resp *http.Response, body []byte) probeVerdict {
	// 403/429 are textbook IP-block signals.
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return probeVerdict{passed: false, reason: "blocked status"}
	}
	// Final URL redirected to a challenge path.
	if resp.Request != nil && resp.Request.URL != nil {
		path := strings.ToLower(resp.Request.URL.Path)
		if strings.Contains(path, "/captcha") || strings.Contains(path, "/challenge") || strings.Contains(path, "/blocked") {
			return probeVerdict{passed: false, reason: "redirected to challenge path"}
		}
	}
	// Body marker scan for anti-bot challenge pages served with a 2xx.
	lower := bytes.ToLower(body)
	for _, marker := range antiBotMarkers {
		if bytes.Contains(lower, []byte(marker)) {
			return probeVerdict{passed: false, reason: "marker:" + marker}
		}
	}
	// Reachable and not blocked — the proxy can serve this destination.
	return probeVerdict{passed: true}
}
