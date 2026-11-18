package billing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// testKey is a made-up admin key; the tests look for it in every error.
const testKey = "sk-ant-admin01-TESTKEY-not-a-real-one"

// now is the fixed clock of every test: the fixtures' buckets lie a day back.
var now = time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// replay is a fake analytics endpoint: answer is called per request and
// writes the response; seen keeps every request's query.
type replay struct {
	mu     sync.Mutex
	seen   []*http.Request
	answer func(n int, w http.ResponseWriter, r *http.Request)
}

func (rp *replay) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rp.mu.Lock()
		rp.seen = append(rp.seen, r)
		n := len(rp.seen)
		rp.mu.Unlock()
		if r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-beta") != betaHeader ||
			r.Header.Get("anthropic-version") != apiVersion {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rp.answer(n, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(srv *httptest.Server, slept *[]time.Duration) *Client {
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), Key: testKey,
		Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			if slept != nil {
				*slept = append(*slept, d)
			}
			return nil
		}}
}

func hourQuery() Query {
	return Query{Start: now.Add(-48 * time.Hour), End: now, Bucket: Hour,
		GroupBy: []string{"model", "api_key_id", "workspace_id", "service_tier", "cost_type", "token_type"}}
}

// Two pages, joined by has_more / next_page: every row of both arrives, and
// the second request asks for the page the first one named.
func TestCostFollowsNextPage(t *testing.T) {
	rp := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "page_REDACTED_2" {
			w.Write(fixture(t, "cost_page2.json"))
			return
		}
		w.Write(fixture(t, "cost_page1.json"))
	}}
	srv := rp.server(t)
	res, err := client(srv, nil).Cost(context.Background(), hourQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Requests != 2 {
		t.Fatalf("rows = %d, requests = %d, want 3 rows from 2 requests", len(res.Rows), res.Requests)
	}
	q := rp.seen[0].URL.Query()
	if q.Get("limit") != "1000" || q.Get("time_bucket_width") != "1h" || len(q["group_by[]"]) != 6 {
		t.Errorf("first request: %v", q)
	}
	if res.Rows[0].Group.APIKeyID != "apikey_test" || res.Rows[0].Group.TokenType != "output_tokens" ||
		res.Rows[2].Group.Model != "" || res.Rows[2].Group.CostType != "web_search" {
		t.Errorf("groups: %+v / %+v", res.Rows[0].Group, res.Rows[2].Group)
	}
	if !res.Landed || res.DataRefreshedAt == nil || !res.DataRefreshedAt.Equal(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("landed %v, refreshed %v", res.Landed, res.DataRefreshedAt)
	}
}

// A 410 on page 2: the data refreshed under the cursor. What page 1 gave is
// thrown away and the read starts again; the result is exactly the second
// pass, not page 1 twice.
func TestCostRestartsAfterGone(t *testing.T) {
	gone := false
	rp := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Write(fixture(t, "cost_page1.json"))
			return
		}
		if !gone {
			gone = true
			w.WriteHeader(http.StatusGone)
			return
		}
		w.Write(fixture(t, "cost_page2.json"))
	}}
	srv := rp.server(t)
	res, err := client(srv, nil).Cost(context.Background(), hourQuery())
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests != 4 {
		t.Errorf("requests = %d, want 4 (page 1, 410, page 1 again, page 2)", res.Requests)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %d, want exactly the 3 of the second pass", len(res.Rows))
	}
	want := []string{"123.456789", "20.000000", "1.000000"}
	for i, r := range res.Rows {
		if r.AmountCents != want[i] {
			t.Errorf("row %d amount = %q, want %q", i, r.AmountCents, want[i])
		}
	}

	// A cursor that keeps expiring gives up after three restarts.
	always := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Write(fixture(t, "cost_page1.json"))
			return
		}
		w.WriteHeader(http.StatusGone)
	}}
	_, err = client(always.server(t), nil).Cost(context.Background(), hourQuery())
	if !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("err = %v, want ErrCursorExpired", err)
	}
	if len(always.seen) != 8 {
		t.Errorf("requests = %d, want 8 (four passes of two)", len(always.seen))
	}
}

// 429 with retry-after: 1 — the client waits what the server asked, once,
// through the injected Sleep, and then gets its answer.
func TestRetryAfterIsHonoured(t *testing.T) {
	rp := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("retry-after", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write(fixture(t, "usage_page1.json"))
	}}
	var slept []time.Duration
	res, err := client(rp.server(t), &slept).Usage(context.Background(), Query{Start: now.Add(-48 * time.Hour), End: now, Bucket: Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept %v, want once for 1s", slept)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	u := res.Rows[0]
	if u.UncachedInput != 60 || u.CacheWrite1h != 95179 || u.CacheRead != 1781843 || u.Output != 34246 ||
		u.WebSearchRequests != 1 || u.Requests != 31 {
		t.Errorf("usage row = %+v", u)
	}

	// 5xx without retry-after: exponential backoff, five tries, then the error.
	broken := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}}
	slept = nil
	_, err = client(broken.server(t), &slept).Usage(context.Background(), Query{Start: now.Add(-time.Hour), End: now, Bucket: Hour})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("err = %v, want the 502", err)
	}
	if len(broken.seen) != 5 || len(slept) != 4 || slept[0] != time.Second || slept[3] != 8*time.Second {
		t.Errorf("tries %d, slept %v", len(broken.seen), slept)
	}
}

// Cents stay the exact text the API wrote: '123.456789' is never a float.
func TestCentsStayExact(t *testing.T) {
	for in, want := range map[string]string{`"123.456789"`: "123.456789", `"41280.000000"`: "41280.000000",
		`123.456789`: "123.456789", `"-0.000001"`: "-0.000001"} {
		got, err := cents([]byte(in))
		if err != nil || got != want {
			t.Errorf("cents(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`null`, `""`, `"1e3"`, `"12,5"`, `{}`} {
		if _, err := cents([]byte(bad)); err == nil {
			t.Errorf("cents(%s) accepted", bad)
		}
	}
}

// A 70-day range is three requests of at most 31 days, contiguous, every end
// on the hour — the first one moved up to the hour, the last one up to the
// end of the current hour.
func TestRangeSplitAt31Days(t *testing.T) {
	rp := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "not_landed.json"))
	}}
	start := now.Add(-70*24*time.Hour + 17*time.Minute)
	res, err := client(rp.server(t), nil).Cost(context.Background(), Query{Start: start, End: now, Bucket: Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(rp.seen) != 3 {
		t.Fatalf("requests = %d, want 3", len(rp.seen))
	}
	var prev time.Time
	for i, r := range rp.seen {
		s, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("starting_at"))
		e, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("ending_at"))
		if err1 != nil || err2 != nil {
			t.Fatalf("request %d: %v %v", i, err1, err2)
		}
		if s.Minute() != 0 || s.Second() != 0 || e.Minute() != 0 || e.Second() != 0 {
			t.Errorf("request %d not on the hour: %s .. %s", i, s, e)
		}
		if e.Sub(s) > 31*24*time.Hour || !e.After(s) {
			t.Errorf("request %d spans %s", i, e.Sub(s))
		}
		if i > 0 && !s.Equal(prev) {
			t.Errorf("request %d starts %s, the one before ended %s", i, s, prev)
		}
		prev = e
	}
	if !res.Window.Start.Equal(start.Truncate(time.Hour)) || !res.Window.End.Equal(now.Truncate(time.Hour).Add(time.Hour)) {
		t.Errorf("window = %+v", res.Window)
	}

	// The floor: before 2026-01-01 nothing is asked; 365 days back neither.
	c := &Client{Now: func() time.Time { return now }}
	w, err := c.Windows(Query{Start: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Bucket: Day})
	if err != nil || len(w) != 0 {
		t.Errorf("a range before the floor gave %v, %v", w, err)
	}
	w, _ = c.Windows(Query{Start: time.Date(2025, 12, 20, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), Bucket: Day})
	if len(w) != 1 || !w[0].Start.Equal(Floor) {
		t.Errorf("clamped to the floor: %v", w)
	}
	late := &Client{Now: func() time.Time { return time.Date(2027, 3, 1, 10, 0, 0, 0, time.UTC) }}
	w, _ = late.Windows(Query{Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), Bucket: Day})
	if len(w) == 0 || !w[0].Start.Equal(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("365 days back (aligned up to the day): %v", w)
	}
	if _, err := c.Windows(Query{Start: now.Add(-time.Hour), End: now, Bucket: "5m"}); err == nil {
		t.Error("an unknown bucket width was accepted")
	}
}

// An error says the status and a bounded piece of the body — never the key,
// even when the server echoes it, and never a header.
func TestErrorNeverCarriesTheKey(t *testing.T) {
	rp := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rp.seen = append(rp.seen, r)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key: ` +
			r.Header.Get("x-api-key") + `"}}` + strings.Repeat(" padding", 100)))
	}))
	defer srv.Close()
	_, err := client(srv, nil).Cost(context.Background(), hourQuery())
	if err == nil {
		t.Fatal("a 401 was not an error")
	}
	msg := err.Error()
	if strings.Contains(msg, testKey) || strings.Contains(msg, "x-api-key: sk-") || strings.Contains(msg, "sk-ant-admin") {
		t.Fatalf("the error carries the key: %s", msg)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || len(apiErr.Body) > errBodyBytes {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "[redacted]") {
		t.Errorf("error = %s", msg)
	}
	if len(rp.seen) != 1 {
		t.Errorf("a 401 was retried: %d requests", len(rp.seen))
	}

	// A refused connection names no key either.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	_, err = (&Client{BaseURL: dead.URL, Key: testKey, Now: func() time.Time { return now },
		Sleep: func(context.Context, time.Duration) error { return nil }}).Cost(context.Background(), hourQuery())
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("connection error = %v", err)
	}
}

// Empty data with data_refreshed_at null is "nothing has landed", not zero.
func TestNotLandedIsNotZero(t *testing.T) {
	rp := &replay{answer: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "not_landed.json"))
	}}
	res, err := client(rp.server(t), nil).Usage(context.Background(), Query{Start: now.Add(-48 * time.Hour), End: now, Bucket: Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.Landed || res.DataRefreshedAt != nil {
		t.Fatalf("rows %d, landed %v, refreshed %v — want none, false, nil", len(res.Rows), res.Landed, res.DataRefreshedAt)
	}
}
