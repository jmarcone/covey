package observability

import (
	"testing"
	"time"
)

// The billed window is hourly from the UTC midnight the poller's hourly read
// starts at (now − 48 h, cut to its day), daily before it. A window of two
// days taken a moment before its now is still hourly; one that reaches into
// the day before the hourly read is daily.
func TestBilledWindowStartHourlyReach(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 58, 47, 0, time.UTC)
	reach := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name      string
		since     time.Time
		start     time.Time
		wantWidth string
	}{
		{"exactly two days", now.AddDate(0, 0, -2), time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC), "1h"},
		{"two days a moment early", now.AddDate(0, 0, -2).Add(-time.Millisecond), time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC), "1h"},
		{"the hourly read's midnight", reach, reach, "1h"},
		{"before the hourly read", reach.Add(-time.Second), reach, "1d"},
		{"three days", now.AddDate(0, 0, -3), reach, "1d"},
	} {
		start, width := billedWindowStart(c.since, now)
		if width != c.wantWidth || !start.Equal(c.start) {
			t.Errorf("%s: window from %v, %s — want from %v, %s", c.name, start, width, c.start, c.wantWidth)
		}
	}
}
