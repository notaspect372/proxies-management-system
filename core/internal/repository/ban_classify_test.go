package repository

import (
	"testing"
	"time"
)

// TestClassifyBan covers the permanent / recovering / cooldown classification
// derived from proxy_domain_bans fields. Uses explicit env values so the test
// is independent of any ambient PERMANENT_* configuration.
func TestClassifyBan(t *testing.T) {
	t.Setenv("PERMANENT_TRIAL_THRESHOLD", "5")
	t.Setenv("PERMANENT_MIN_AGE", "24h")

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)   // older than min age
	recent := now.Add(-1 * time.Hour) // younger than min age
	ptr := func(tm time.Time) *time.Time { return &tm }

	cases := []struct {
		name           string
		row            CooldownRow
		wantClass      string
		wantNeverRecov bool
	}{
		{
			name:           "first trial not done yet → cooldown",
			row:            CooldownRow{State: StateBanned, ProbeAttempt: 0, BannedAt: ptr(old)},
			wantClass:      ClassCooldown,
			wantNeverRecov: true,
		},
		{
			name:           "tried but below trial threshold → recovering",
			row:            CooldownRow{State: StateBanned, ProbeAttempt: 2, BannedAt: ptr(old)},
			wantClass:      ClassRecovering,
			wantNeverRecov: true,
		},
		{
			name:           "enough trials + old + never recovered → permanent",
			row:            CooldownRow{State: StateBanned, ProbeAttempt: 5, BannedAt: ptr(old)},
			wantClass:      ClassPermanent,
			wantNeverRecov: true,
		},
		{
			name:           "enough trials but too young → recovering",
			row:            CooldownRow{State: StateBanned, ProbeAttempt: 7, BannedAt: ptr(recent)},
			wantClass:      ClassRecovering,
			wantNeverRecov: true,
		},
		{
			name:           "enough trials + old but recovered this episode → recovering",
			row:            CooldownRow{State: StateBanned, ProbeAttempt: 6, BannedAt: ptr(old), SuccessfulSinceRecovery: 2},
			wantClass:      ClassRecovering,
			wantNeverRecov: false,
		},
		{
			name:           "missing banned_at cannot be permanent → recovering",
			row:            CooldownRow{State: StateBanned, ProbeAttempt: 9, BannedAt: nil},
			wantClass:      ClassRecovering,
			wantNeverRecov: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotClass, gotNever := classifyBan(c.row, now)
			if gotClass != c.wantClass {
				t.Errorf("classification = %q, want %q", gotClass, c.wantClass)
			}
			if gotNever != c.wantNeverRecov {
				t.Errorf("neverRecovered = %v, want %v", gotNever, c.wantNeverRecov)
			}
		})
	}
}
