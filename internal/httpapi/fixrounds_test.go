package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestFixRoundsWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	window := func(query string) (time.Time, time.Time, string) {
		return fixRoundsWindow(httptest.NewRequest("GET", "/api/v1/cost/fix-rounds"+query, nil), now)
	}

	// Default: the last seven days, ending now.
	from, to, msg := window("")
	if msg != "" || !to.Equal(now) || !from.Equal(now.AddDate(0, 0, -7)) {
		t.Fatalf("default window = [%s, %s) %q", from, to, msg)
	}
	// Both given.
	from, to, msg = window("?from=2026-11-01T00:00:00Z&to=2026-11-08T00:00:00Z")
	if msg != "" || !from.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("explicit window = [%s, %s) %q", from, to, msg)
	}
	// Only to: seven days before it.
	from, _, msg = window("?to=2026-11-08T00:00:00%2B01:00")
	if msg != "" || !from.Equal(time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("to only: from = %s %q", from, msg)
	}
	for _, bad := range []string{
		"?from=yesterday",
		"?to=2026-11-08",
		"?from=2026-11-08T00:00:00Z&to=2026-11-08T00:00:00Z",
		"?from=2026-11-09T00:00:00Z&to=2026-11-08T00:00:00Z",
		"?from=2024-01-01T00:00:00Z&to=2026-11-08T00:00:00Z",
	} {
		if _, _, msg := window(bad); msg == "" {
			t.Fatalf("%s must be refused", bad)
		}
	}
}
