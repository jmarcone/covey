package httpapi

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestCostRangeSpan: a range spans at most 31 days and one hour — a whole
// local month across the autumn clock change (October 2026 in Berlin: 1 Oct
// 00:00 CEST to 1 Nov 00:00 CET) is 31 days and one hour, and is one month.
// web/src/pages/costRange.ts (validRange) draws the same line.
func TestCostRangeSpan(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	ask := func(from time.Time) error {
		q := url.Values{"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
		r := httptest.NewRequest("GET", "/cost/cells?"+q.Encode(), nil)
		_, since, until, err := costRange(r, now)
		if err == nil && (!since.Equal(from) || !until.Equal(to)) {
			t.Fatalf("from %s: window %s..%s, want %s..%s", from, since, until, from, to)
		}
		return err
	}
	berlinOctober := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	if got := to.Sub(berlinOctober); got != 31*24*time.Hour+time.Hour {
		t.Fatalf("October in Berlin spans %s", got)
	}
	for _, c := range []struct {
		name string
		span time.Duration
		code string
	}{
		{"31 days", 31 * 24 * time.Hour, ""},
		{"31 days and one hour", 31*24*time.Hour + time.Hour, ""},
		{"31 days, one hour and a minute", 31*24*time.Hour + time.Hour + time.Minute, "range_span"},
	} {
		err := ask(to.Add(-c.span))
		var re rangeError
		switch {
		case c.code == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.code != "" && (!errors.As(err, &re) || re.Code != c.code):
			t.Errorf("%s: got %v, want %s", c.name, err, c.code)
		}
	}
}
