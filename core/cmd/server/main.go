// Package main provides the entry point for the Rota Proxy Server
//
//	@title			Rota Proxy API
//	@version		1.0.0
//	@description	A high-performance proxy rotation server with health monitoring and intelligent routing
//	@description	Provides comprehensive API for managing proxy servers, monitoring their health,
//	@description	and configuring rotation strategies.
//
//	@contact.name	API Support
//	@contact.url	https://github.com/alpkeskin/rota
//
//	@license.name	LICENSE
//	@license.url	https://github.com/alpkeskin/rota/blob/main/LICENSE
//
//	@host		localhost:8001
//	@BasePath	/api/v1
//
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Type "Bearer" followed by a space and JWT token.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alpkeskin/rota/core/internal/api"
	"github.com/alpkeskin/rota/core/internal/config"
	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/proxy"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize logger
	log := logger.New(cfg.LogLevel)
	log.Info("starting application",
		"proxy_port", cfg.ProxyPort,
		"api_port", cfg.APIPort,
	)

	// Initialize database
	ctx := context.Background()
	db, err := database.New(ctx, &cfg.Database, database.DefaultConfig(), log)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Run database migrations
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Create repositories
	proxyRepo := repository.NewProxyRepository(db)
	settingsRepo := repository.NewSettingsRepository(db)
	assignmentRepo := repository.NewAssignmentRepository(db)
	banRepo := repository.NewBanRepository(db)
	if err := banRepo.EnsureMongoIndexes(ctx); err != nil {
		log.Warn("failed to ensure proxy_domain_bans indexes", "error", err)
	}

	// Background probe worker — drives the Recovery Test phase of the proxy
	// lifecycle. Picks up banned scopes whose next_probe_at has fired and
	// sends a real GET to the domain to decide whether the ban has lifted.
	probeWorker := proxy.NewProbeWorker(banRepo, log)
	probeWorker.Start(ctx)
	defer probeWorker.Stop()

	// Per-machine routing defaults — when set, scrapers can use plain
	// `proxy=localhost:8006` and routing kicks in based on this machine's
	// configured identity. See ROUTING_DEFAULT_MACHINE / ROUTING_DEFAULT_COUNTRY.
	if cfg.RoutingDefaultMachine != "" {
		proxy.SetRoutingDefaults(cfg.RoutingDefaultMachine, cfg.RoutingDefaultCountry)
		log.Info("routing defaults configured",
			"machine_id", cfg.RoutingDefaultMachine,
			"country", cfg.RoutingDefaultCountry,
		)
	}

	// Per-country aux listeners — open one extra port per (machine, country)
	// pair so scrapers that won't send Proxy-Authorization (Chrome under SB
	// UC mode) can still hit a country-specific port and have routing
	// engage. See AUX_LISTENERS in config.
	if len(cfg.AuxListeners) > 0 {
		specs := make([]proxy.AuxListenerSpec, 0, len(cfg.AuxListeners))
		for _, l := range cfg.AuxListeners {
			specs = append(specs, proxy.AuxListenerSpec{
				MachineID: l.MachineID,
				Country:   l.Country,
				Port:      l.Port,
			})
		}
		if err := proxy.StartAuxListeners(cfg.RoutingDefaultMachine, cfg.AuxListenAddr, cfg.ProxyPort, specs, log); err != nil {
			return fmt.Errorf("failed to start aux listeners: %w", err)
		}
	}

	// Create servers
	proxyServer, err := proxy.New(cfg.ProxyPort, log, proxyRepo, settingsRepo, assignmentRepo, banRepo)
	if err != nil {
		return fmt.Errorf("failed to create proxy server: %w", err)
	}
	apiServer := api.New(cfg, log, db)

	// Set proxy server reference in API server for reload functionality
	apiServer.SetProxyServer(proxyServer)

	// Start servers in goroutines
	errChan := make(chan error, 2)

	// Start proxy server
	go func() {
		if err := proxyServer.Start(); err != nil {
			errChan <- fmt.Errorf("proxy server error: %w", err)
		}
	}()

	// Start API server
	go func() {
		if err := apiServer.Start(); err != nil {
			errChan <- fmt.Errorf("API server error: %w", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errChan:
		log.Error("server error", "error", err)
		return err
	case sig := <-quit:
		log.Info("received shutdown signal", "signal", sig.String())
	}

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Info("shutting down servers...")

	// Shutdown both servers
	var shutdownWg sync.WaitGroup
	shutdownErrors := make(chan error, 2)

	shutdownWg.Go(func() {
		if err := proxyServer.Shutdown(ctx); err != nil {
			shutdownErrors <- fmt.Errorf("proxy server shutdown error: %w", err)
		}
	})

	shutdownWg.Go(func() {
		if err := apiServer.Shutdown(ctx); err != nil {
			shutdownErrors <- fmt.Errorf("API server shutdown error: %w", err)
		}
	})

	// Wait for shutdown to complete
	shutdownWg.Wait()
	close(shutdownErrors)

	// Collect any shutdown errors
	var shutdownErr error
	for err := range shutdownErrors {
		if shutdownErr == nil {
			shutdownErr = err
		} else {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}

	if shutdownErr != nil {
		log.Error("shutdown completed with errors", "error", shutdownErr)
		return shutdownErr
	}

	log.Info("shutdown completed successfully")
	return nil
}

