package models

import "time"

// ProxyAssignment records a sticky binding between a (machine_id, domain) pair
// and a single proxy. Workers call /api/v1/proxy and we keep returning the
// same proxy until the worker explicitly releases it or the proxy goes
// unhealthy.
type ProxyAssignment struct {
	ID            int64     `json:"id"`
	MachineID     string    `json:"machine_id"`
	Domain        string    `json:"domain"`
	ProxyID       int       `json:"proxy_id"`
	TargetCountry string    `json:"target_country"`
	AssignedAt    time.Time `json:"assigned_at"`
	LastUsedAt    time.Time `json:"last_used_at"`
	RequestCount  int64     `json:"request_count"`
}

// AssignmentWithProxy joins an assignment with the proxy details that scrapers
// and the dashboard care about.
type AssignmentWithProxy struct {
	MachineID     string    `json:"machine_id"`
	Domain        string    `json:"domain"`
	TargetCountry string    `json:"target_country"`
	AssignedAt    time.Time `json:"assigned_at"`
	LastUsedAt    time.Time `json:"last_used_at"`
	RequestCount  int64     `json:"request_count"`

	ProxyID         int      `json:"proxy_id"`
	ProxyAddress    string   `json:"proxy_address"`
	ProxyProtocol   string   `json:"proxy_protocol"`
	ProxyUsername   *string  `json:"proxy_username,omitempty"`
	ProxyStatus     string   `json:"proxy_status"`
	ProxyCategory   *string  `json:"proxy_category,omitempty"`
	ProxyCountry    *string  `json:"proxy_country,omitempty"`
	ProxyCost       *float64 `json:"proxy_cost,omitempty"`
	SuccessRate     float64  `json:"success_rate"`
	AvgResponseTime int      `json:"avg_response_time"`
}

// ProxyCheckoutResponse is what /api/v1/proxy returns to a scraper.
type ProxyCheckoutResponse struct {
	ProxyID  int     `json:"proxy_id"`
	Address  string  `json:"address"`
	Protocol string  `json:"protocol"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
	Country  *string `json:"country,omitempty"`
	Category *string `json:"category,omitempty"`

	// URL is a ready-to-use proxy URL (with embedded credentials when present).
	// e.g. "http://user:pass@1.2.3.4:8080"
	URL string `json:"url"`

	// Sticky describes whether this proxy was reused from an existing
	// assignment for (machine_id, domain) or freshly picked.
	Sticky bool `json:"sticky"`

	MachineID     string `json:"machine_id"`
	Domain        string `json:"domain"`
	TargetCountry string `json:"target_country,omitempty"`
}

// InfrastructureResponse is what /api/v1/infrastructure returns — the fleet
// topology with live proxy-assignment data grouped by target country.
type InfrastructureResponse struct {
	Machines []InfrastructureMachine `json:"machines"`
}

type InfrastructureMachine struct {
	ID             string                       `json:"id"`
	Name           string                       `json:"name"`
	Hostname       string                       `json:"hostname"`
	Kind           string                       `json:"kind"`
	VMs            []FleetVM                    `json:"vms"`
	CountryGroups  []InfrastructureCountryGroup `json:"country_groups"`
	TotalAssigned  int                          `json:"total_assignments"`
}

// InfrastructureCountryGroup buckets a machine's assignments by the target
// country the scraper specified at checkout time.
type InfrastructureCountryGroup struct {
	TargetCountry string                `json:"target_country"`
	ActiveCount   int                   `json:"active_count"`
	TotalCount    int                   `json:"total_count"`
	Assignments   []AssignmentWithProxy `json:"assignments"`
}
