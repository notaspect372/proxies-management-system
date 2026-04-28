// Package services hosts background workers that aren't tied to a request.
//
// CountryDetector fills in proxies' `country` field by looking up their IP via
// ip-api.com. Runs once on startup, then every interval. The auto-loop is
// gentle: it only touches proxies whose country is still empty.
package services

import (
	"context"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/geoip"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

type CountryDetector struct {
	repo     *repository.ProxyRepository
	logger   *logger.Logger
	interval time.Duration
	stop     chan struct{}
	once     sync.Once
}

func NewCountryDetector(repo *repository.ProxyRepository, log *logger.Logger) *CountryDetector {
	return &CountryDetector{
		repo:     repo,
		logger:   log,
		interval: 5 * time.Minute,
		stop:     make(chan struct{}),
	}
}

// Start kicks off the background loop. Safe to call multiple times.
func (d *CountryDetector) Start(ctx context.Context) {
	d.once.Do(func() {
		go d.loop(ctx)
	})
}

// Stop terminates the background loop.
func (d *CountryDetector) Stop() {
	select {
	case <-d.stop:
	default:
		close(d.stop)
	}
}

// RunOnce scans once. force=true re-detects every proxy, even if country is
// already set (useful when the manual API trigger is hit).
//
// Returns counts: scanned, updated.
func (d *CountryDetector) RunOnce(ctx context.Context, force bool) (int, int, error) {
	proxies, err := d.repo.ListWithoutCountry(ctx, force, 500)
	if err != nil {
		return 0, 0, err
	}
	if len(proxies) == 0 {
		return 0, 0, nil
	}

	d.logger.Info("country detector: starting pass",
		"source", "country_detector",
		"candidates", len(proxies),
		"force", force,
	)

	updated := 0
	for _, p := range proxies {
		select {
		case <-ctx.Done():
			return len(proxies), updated, ctx.Err()
		case <-d.stop:
			return len(proxies), updated, nil
		default:
		}

		ip := geoip.IPFromAddress(p.Address)
		if ip == "" {
			d.logger.Debug("country detector: skipping non-IP address",
				"source", "country_detector",
				"proxy_id", p.ID,
				"address", p.Address,
			)
			continue
		}

		country, err := geoip.LookupCountry(ctx, ip)
		if err != nil {
			d.logger.Warn("country detector: lookup failed",
				"source", "country_detector",
				"proxy_id", p.ID,
				"ip", ip,
				"error", err,
			)
			continue
		}
		if country == "" {
			continue
		}

		if err := d.repo.SetCountry(ctx, p.ID, country); err != nil {
			d.logger.Warn("country detector: db update failed",
				"source", "country_detector",
				"proxy_id", p.ID,
				"error", err,
			)
			continue
		}
		updated++
	}

	d.logger.Info("country detector: pass complete",
		"source", "country_detector",
		"scanned", len(proxies),
		"updated", updated,
	)
	return len(proxies), updated, nil
}

func (d *CountryDetector) loop(ctx context.Context) {
	// First pass shortly after startup so newly imported batches get countries
	// without a 5-minute wait.
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-timer.C:
			if _, _, err := d.RunOnce(ctx, false); err != nil {
				d.logger.Warn("country detector pass aborted",
					"source", "country_detector",
					"error", err,
				)
			}
			timer.Reset(d.interval)
		}
	}
}
