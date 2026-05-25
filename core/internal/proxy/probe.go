package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
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

// runProbe sends a single GET through the proxy to the target domain and
// classifies the response. Returns passed=true only when the response looks
// like a real page from the target site (no transport error AND no obvious
// anti-bot marker AND not a redirect to a challenge page).
func (w *ProbeWorker) runProbe(ctx context.Context, t repository.ProbeTarget) probeVerdict {
	proxy := &models.Proxy{
		ID:       t.Scope.ProxyID,
		Address:  t.ProxyAddress,
		Protocol: t.ProxyProtocol,
		Username: t.ProxyUsername,
		Password: t.ProxyPassword,
	}
	transport, err := CreateProxyTransport(proxy)
	if err != nil {
		return probeVerdict{passed: false, reason: "transport: " + err.Error()}
	}
	// Don't pollute the proxy with TLS strict-checking failures — many
	// scraping targets have weird certs. Probe is a liveness test.
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

	// Try HTTPS first (most modern sites force it). If the connection fails
	// at the transport layer, fall back to HTTP — some sites still serve a
	// landing page over plain HTTP that's enough to tell us "the proxy got
	// through to the origin."
	verdict := w.probeURL(ctx, client, "https://"+t.Scope.TargetDomain+"/")
	if verdict.passed || verdict.statusCode > 0 {
		// Either passed, or we got SOME HTTP response — even an error page
		// is useful classification material. No reason to retry over HTTP.
		return verdict
	}
	return w.probeURL(ctx, client, "http://"+t.Scope.TargetDomain+"/")
}

func (w *ProbeWorker) probeURL(ctx context.Context, client *http.Client, url string) probeVerdict {
	reqCtx, cancel := context.WithTimeout(ctx, w.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return probeVerdict{passed: false, reason: "req build: " + err.Error()}
	}
	// Use a realistic UA — some sites will instantly block "Go-http-client".
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	start := time.Now()
	resp, err := client.Do(req)
	latencyMS := int(time.Since(start).Milliseconds())
	if err != nil {
		return probeVerdict{passed: false, reason: "transport: " + err.Error(), latencyMS: latencyMS}
	}
	defer resp.Body.Close()

	// Cap body read — anti-bot pages are small; we don't need a full real
	// page to classify. 64 KB is plenty.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	verdict := classifyResponse(resp, body)
	verdict.statusCode = resp.StatusCode
	verdict.latencyMS = latencyMS
	return verdict
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

func classifyResponse(resp *http.Response, body []byte) probeVerdict {
	// 5xx and other transport-ish failures => not passed.
	if resp.StatusCode >= 500 {
		return probeVerdict{passed: false, reason: "5xx response"}
	}
	// 403/429 are textbook block signals.
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
	// Body marker scan.
	lower := bytes.ToLower(body)
	for _, marker := range antiBotMarkers {
		if bytes.Contains(lower, []byte(marker)) {
			return probeVerdict{passed: false, reason: "marker:" + marker}
		}
	}
	// Implausibly small page heuristic — a real homepage is at least a few
	// KB. Many challenge pages are 2–4 KB stubs.
	if len(body) > 0 && len(body) < 1500 {
		// Allow tiny responses only if the status code looks intentional
		// (e.g. 204 / 304) — otherwise it's almost certainly a stub.
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
			return probeVerdict{passed: false, reason: "body too small"}
		}
	}
	// Looks like a real page.
	return probeVerdict{passed: true}
}
