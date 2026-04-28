// Package geoip resolves a country name from an IP using ip-api.com.
//
// ip-api.com is free, no API key, ~45 req/min from a single source IP. We
// rate-limit ourselves to stay well under that cap so a 100-proxy backfill
// finishes in ~2.5 minutes without getting blocked.
package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	client = &http.Client{Timeout: 6 * time.Second}

	rateMu  sync.Mutex
	lastHit time.Time
)

// minInterval is the floor between two outbound calls. 1.5s ≈ 40 req/min,
// safely under ip-api's 45 req/min cap.
const minInterval = 1500 * time.Millisecond

type result struct {
	Status  string `json:"status"`
	Country string `json:"country"`
	Message string `json:"message"`
}

// IPFromAddress extracts the bare IP from a "host:port" or "host" string.
// Returns "" if the host part isn't a parseable IPv4/IPv6 literal.
func IPFromAddress(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	} else if i := strings.LastIndex(addr, ":"); i != -1 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) == nil {
		// Hostname (not a literal IP) — caller will need DNS first if they
		// want a country. We deliberately don't resolve here to avoid
		// surprising round-trips inside the request hot path.
		return ""
	}
	return host
}

// LookupCountry returns the human-readable country name for an IP, e.g.
// "Taiwan", "United States". Returns "" with a non-nil error if the lookup
// fails.
func LookupCountry(ctx context.Context, ip string) (string, error) {
	if ip == "" {
		return "", fmt.Errorf("empty ip")
	}

	throttle()

	url := "http://ip-api.com/json/" + ip + "?fields=status,country,message"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ip-api returned %d", resp.StatusCode)
	}

	var r result
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Status != "success" {
		return "", fmt.Errorf("geoip lookup failed: %s", r.Message)
	}
	return r.Country, nil
}

func throttle() {
	rateMu.Lock()
	defer rateMu.Unlock()
	if since := time.Since(lastHit); since < minInterval {
		time.Sleep(minInterval - since)
	}
	lastHit = time.Now()
}
