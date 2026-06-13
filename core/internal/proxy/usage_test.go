package proxy

import "testing"

// TestIsInfraDomain covers the static denylist pre-filter that skips obvious
// ads/trackers/push infra before any ban accounting or domain confirmation.
func TestIsInfraDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   bool
	}{
		{"mtalk.google.com", true},          // exact infra entry
		{"fonts.gstatic.com", true},         // subdomain of gstatic.com
		{"www.google-analytics.com", true},  // subdomain
		{"ads.doubleclick.net", true},       // subdomain
		{"GSTATIC.COM", true},               // case-insensitive
		{"www.cian.ru", false},              // real scrape target
		{"rumah123.com", false},             // real scrape target
		{"notgstatic.com", false},           // must not match by substring
		{"gstatic.com.evil.com", false},     // suffix trick must not match
		{"", false},                         // empty
	}
	for _, c := range cases {
		if got := isInfraDomain(c.domain); got != c.want {
			t.Errorf("isInfraDomain(%q) = %v, want %v", c.domain, got, c.want)
		}
	}
}
