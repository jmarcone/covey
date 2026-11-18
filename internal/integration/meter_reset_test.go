package integration

import (
	"testing"
	"time"
)

// Without reset times a drop of more than one point begins a new week, also
// a small one (6 % then 2 %); a drop of one is a lagging reading. Where the
// previous reading's reset time is still ahead, no new week has begun,
// whatever the figure.
func TestMeterResetWithoutResetTimes(t *testing.T) {
	s := newStack(t)
	since := time.Now().Add(-5 * time.Hour).Truncate(time.Hour)
	week1 := since.Add(48 * time.Hour)
	got := meterPointsOf(t, s, since, map[string][]meterReading{
		"small-week": {{min: -30, pct: 6}, {min: 30, pct: 2}},
		"one-down":   {{min: -30, pct: 10}, {min: 10, pct: 9}, {min: 20, pct: 11}},
		"ahead":      {{min: -30, pct: 30, resets: week1}, {min: 10, pct: 20}, {min: 20, pct: 31}},
	})
	if got["small-week"] != 2 {
		t.Errorf("6 %% then 2 %%: %v points, want a new week of 2", got["small-week"])
	}
	if got["one-down"] != 1 {
		t.Errorf("10, 9, 11: %v points, want 1 (the 9 lags)", got["one-down"])
	}
	if got["ahead"] != 1 {
		t.Errorf("a drop while the reset is still ahead: %v points, want 1", got["ahead"])
	}
}
