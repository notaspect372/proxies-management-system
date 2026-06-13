package proxy

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/alpkeskin/rota/core/internal/models"
)

// RoutingHints carries the machine_id + country a scraper has encoded into the
// proxy URL credentials, e.g. http://main_machine_vm1:Taiwan@host:8006.
//
// When present we route via the sticky AssignmentRepository.Checkout instead
// of the random/round-robin selector.
type RoutingHints struct {
	MachineID string
	Country   string // optional; "" means no country filter
}

// Per-machine defaults. When set via env, requests without a
// Proxy-Authorization header are treated as carrying these hints. Set once at
// startup via SetRoutingDefaults, then read-only.
var (
	defaultMachineID string
	defaultCountry   string
)

// SetRoutingDefaults stores the machine identity for this Rota instance.
// Empty machineID disables the default-injection behaviour.
func SetRoutingDefaults(machineID, country string) {
	defaultMachineID = machineID
	defaultCountry = country
}

// parseRoutingHints reads the Proxy-Authorization header. If the username
// matches a known fleet machine_id we treat it as routing hints. If no header
// is present and a default machine is configured, we synthesize hints from
// that default. Anything else returns ok=false so the existing rotation/auth
// flow runs unchanged.
func parseRoutingHints(req *http.Request) (*RoutingHints, bool) {
	if req == nil {
		return nil, false
	}
	header := req.Header.Get("Proxy-Authorization")
	if header == "" {
		// Fall back to the per-machine default when configured.
		if defaultMachineID != "" && models.IsValidMachineID(defaultMachineID) {
			return &RoutingHints{
				MachineID: defaultMachineID,
				Country:   defaultCountry,
			}, true
		}
		return nil, false
	}
	if !strings.HasPrefix(header, "Basic ") {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if err != nil {
		return nil, false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) == 0 || parts[0] == "" {
		return nil, false
	}
	machineID := parts[0]
	if !models.IsValidMachineID(machineID) {
		return nil, false
	}
	country := ""
	if len(parts) == 2 {
		country = parts[1]
	}
	return &RoutingHints{MachineID: machineID, Country: country}, true
}

// stripProxyAuth removes the routing header so it isn't forwarded upstream.
func stripProxyAuth(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Del("Proxy-Authorization")
}

// hostOnly trims an optional :port suffix so we can use the result as a
// stable "domain" key for assignments.
func hostOnly(addr string) string {
	if addr == "" {
		return ""
	}
	if i := strings.LastIndex(addr, ":"); i != -1 && !strings.Contains(addr[i+1:], "]") {
		return addr[:i]
	}
	return addr
}

// portOf returns the port component of "host:port", or "" if none is present.
// Used to remember which port the scraper actually reached so the recovery
// probe can test that exact port (e.g. mtalk.google.com:5228, not :443).
func portOf(addr string) string {
	if addr == "" {
		return ""
	}
	if i := strings.LastIndex(addr, ":"); i != -1 && !strings.Contains(addr[i+1:], "]") {
		return addr[i+1:]
	}
	return ""
}
