package repository

import (
	"testing"
	"time"
)

// TestProbeBackoffLadder pins the doubling trial schedule. A scope is first
// trialed initialCooldown after the ban, then each failed trial doubles the
// wait until maxCooldown clamps it.
func TestProbeBackoffLadder(t *testing.T) {
	if initialCooldown != 30*time.Minute {
		t.Fatalf("first trial should fire 30m after the ban, got %v", initialCooldown)
	}

	cases := []struct {
		failedTrials int
		want         time.Duration
	}{
		{1, 60 * time.Minute},
		{2, 120 * time.Minute},
		{3, 240 * time.Minute},
		{4, 480 * time.Minute},
		{5, 960 * time.Minute},
		{6, maxCooldown}, // 1920m would exceed the daily cap
		{9, maxCooldown},
		{50, maxCooldown}, // runaway counter must not overflow the shift
	}
	for _, c := range cases {
		if got := probeBackoff(c.failedTrials); got != c.want {
			t.Errorf("probeBackoff(%d) = %v, want %v", c.failedTrials, got, c.want)
		}
	}
}

// TestProbeBackoffNeverZero guards the floor: a non-positive attempt count is
// treated as the first failure rather than scheduling an immediate retry loop.
func TestProbeBackoffNeverZero(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := probeBackoff(n); got != 60*time.Minute {
			t.Errorf("probeBackoff(%d) = %v, want 60m", n, got)
		}
	}
}

// TestMedian covers the odd/even split used by the per-site estimate.
func TestMedian(t *testing.T) {
	cases := []struct {
		in   []int64
		want int64
	}{
		{[]int64{}, 0},
		{[]int64{5}, 5},
		{[]int64{1, 3}, 2},
		{[]int64{1, 2, 3}, 2},
		{[]int64{1, 2, 3, 10}, 2},
	}
	for _, c := range cases {
		if got := median(c.in); got != c.want {
			t.Errorf("median(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
