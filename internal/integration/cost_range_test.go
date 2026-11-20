package integration

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
)

func rfc(t time.Time) string { return url.QueryEscape(t.UTC().Format(time.RFC3339)) }

// ?from= and ?to= replace the computed window, half-open: a row stamped at
// from is in, a row stamped at to is in the next window. Every figure the
// page reads must equal the SQL sum over the same [from, to), and the meter
// stops at the window's end — its last reading in the window is the window's
// last, not the reading that came after.
func TestCostRangeTotalsMatchSQLOverTheHalfOpenWindow(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("kosten-bereich")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")

	// An hour that lies wholly in the past, whatever the clock says.
	from := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	to := from.Add(time.Hour)
	edge := []struct {
		at     time.Duration
		in     bool
		tokens int
		usd    float64
	}{
		{-time.Minute, false, 1000, 0.5},
		{0, true, 2000, 1},                          // exactly from: in
		{30 * time.Minute, true, 4000, 2},           // inside
		{time.Hour - time.Second, true, 8000, 4},    // the last second
		{time.Hour, false, 16000, 8},                // exactly to: out
		{time.Hour + time.Minute, false, 32000, 16}, // after
	}
	for i, e := range edge {
		at := from.Add(e.at)
		if _, err := s.pool.Exec(ctx, `INSERT INTO model_usage (org_id, agent_id, event_id, model, input_tokens, output_tokens,
			engine_usd, secret_key, secret_slot, credential_kind, created_at)
			VALUES ($1, $2, $3, 'claude-opus-5-5', 10, $4, $5, 'claude_code_oauth_token', 0, 'subscription', $6)`,
			s.orgID, agent.ID, 9000+i, e.tokens, e.usd, at); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO cost_entries (agent_id, model, usd, input_tokens, output_tokens, created_at, secret_key, secret_slot)
			VALUES ($1, 'claude-opus-5-5', $2, 10, $3, $4, 'claude_code_oauth_token', 0)`, agent.ID, e.usd, e.tokens, at); err != nil {
			t.Fatal(err)
		}
	}
	var wantOut, wantUSD float64
	var wantEntries int
	for _, e := range edge {
		if e.in {
			wantOut += float64(e.tokens)
			wantUSD += e.usd
			wantEntries++
		}
	}
	// The expectation of the test is not the code's: the SQL sums the same
	// half-open window by itself.
	var sqlOut, sqlUSD float64
	var sqlEntries int
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(output_tokens),0)::float8 FROM model_usage
		WHERE org_id = $1 AND created_at >= $2 AND created_at < $3`, s.orgID, from, to).Scan(&sqlOut); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(ce.usd),0)::float8, count(*) FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		WHERE a.org_id = $1 AND ce.created_at >= $2 AND ce.created_at < $3`, s.orgID, from, to).Scan(&sqlUSD, &sqlEntries); err != nil {
		t.Fatal(err)
	}
	if sqlOut != wantOut || sqlUSD != wantUSD || sqlEntries != wantEntries {
		t.Fatalf("fixture: SQL says %v tokens, $%v, %d entries; the edges say %v, $%v, %d", sqlOut, sqlUSD, sqlEntries, wantOut, wantUSD, wantEntries)
	}

	// The meter: one reading before the window, two in it, one after. The
	// window's points are the two steps in it; the reading after the window
	// moves nothing in it.
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{from.Add(-30 * time.Minute), 8}, {from.Add(10 * time.Minute), 10}, {from.Add(30 * time.Minute), 12}, {to.Add(10 * time.Minute), 20}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}

	q := fmt.Sprintf("from=%s&to=%s", rfc(from), rfc(to))

	cells := admin.expect(http.MethodGet, "/api/v1/cost/cells?bucket=10min&"+q, nil, http.StatusOK)
	var cellOut, cellReported float64
	for _, c := range cells["cells"].([]any) {
		m := c.(map[string]any)
		cellReported += m["reported_usd"].(float64)
		if tok, ok := m["tokens"].(map[string]any); ok {
			cellOut += tok["output"].(float64)
		}
	}
	if cellOut != sqlOut || math.Abs(cellReported-sqlUSD) > 1e-9 {
		t.Errorf("/cost/cells: %v output tokens, reported $%v — SQL over [from, to) says %v and $%v", cellOut, cellReported, sqlOut, sqlUSD)
	}
	if got, _ := cells["until"].(string); got != to.UTC().Format(time.RFC3339) {
		t.Errorf("/cost/cells until = %q, want the asked to %s", got, to.UTC().Format(time.RFC3339))
	}
	meter, _ := cells["meter"].([]any)
	if len(meter) != 1 {
		t.Fatalf("/cost/cells meter = %v, want the one credential", cells["meter"])
	}
	cov := meter[0].(map[string]any)
	if cov["points"] != float64(4) || cov["last_percent"] != float64(12) {
		t.Errorf("meter in the window: %v points to %v %% — want 4 points (8→10→12), last reading 12 %%, not the one after to", cov["points"], cov["last_percent"])
	}
	if last, _ := cov["last_at"].(string); last == "" || mustTime(t, last).After(to) {
		t.Errorf("meter last_at = %q lies after to %v", last, to)
	}

	org := admin.expect(http.MethodGet, "/api/v1/cost/org?"+q, nil, http.StatusOK)
	if math.Abs(org["total_usd"].(float64)-sqlUSD) > 1e-9 || org["entries"] != float64(sqlEntries) {
		t.Errorf("/cost/org: $%v in %v entries — SQL says $%v in %d", org["total_usd"], org["entries"], sqlUSD, sqlEntries)
	}

	models := admin.expect(http.MethodGet, "/api/v1/cost/models?"+q, nil, http.StatusOK)
	var modelOut float64
	for _, r := range models["rows"].([]any) {
		modelOut += r.(map[string]any)["output_tokens"].(float64)
	}
	if modelOut != sqlOut {
		t.Errorf("/cost/models: %v output tokens, SQL says %v", modelOut, sqlOut)
	}

	// The relative range of the same hour reads more: the answer without a
	// from/to is not cut.
	open := admin.expect(http.MethodGet, "/api/v1/cost/org?days=1", nil, http.StatusOK)
	if open["total_usd"].(float64) <= sqlUSD {
		t.Errorf("/cost/org?days=1 = $%v, want more than the hour's $%v", open["total_usd"], sqlUSD)
	}

	// The readings endpoint stops at to as well.
	list := admin.expectList(http.MethodGet, "/api/v1/cost/usage-readings?"+q, nil, http.StatusOK)
	if len(list) != 2 {
		t.Errorf("/cost/usage-readings over the hour holds %d readings, want the two inside it", len(list))
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// An invalid range is a 400 with its reason and a code the page maps to its
// own words — never an empty answer.
func TestCostRangeRefusesAnInvalidRange(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	from := now.Add(-3 * time.Hour)
	cases := []struct{ name, query, code string }{
		{"to missing", "from=" + rfc(from), "range_incomplete"},
		{"from missing", "to=" + rfc(now), "range_incomplete"},
		{"not a time", "from=yesterday&to=" + rfc(now), "range_time"},
		{"no zone", "from=2026-11-20T16:00:00&to=2026-11-20T17:00:00", "range_time"},
		{"to before from", "from=" + rfc(now.Add(-time.Hour)) + "&to=" + rfc(from), "range_order"},
		{"empty", "from=" + rfc(from) + "&to=" + rfc(from), "range_order"},
		{"longer than 31 days", "from=" + rfc(now.Add(-40*24*time.Hour)) + "&to=" + rfc(now.Add(-time.Hour)), "range_span"},
		{"from in the future", "from=" + rfc(now.Add(time.Hour)) + "&to=" + rfc(now.Add(2*time.Hour)), "range_future"},
		{"10-minute buckets over 3 days", "bucket=10min&from=" + rfc(now.Add(-72*time.Hour)) + "&to=" + rfc(now), "range_bucket"},
	}
	for _, path := range []string{"cost/cells", "cost/org", "cost/models", "cost/usage-readings", "cost/billed",
		"cost/reconcile", "cost/runs", "cost/external-usage"} {
		for _, c := range cases {
			got := admin.expect(http.MethodGet, "/api/v1/"+path+"?"+c.query, nil, http.StatusBadRequest)
			if got["code"] != c.code || got["error"] == "" || got["error"] == nil {
				t.Errorf("%s with %s: %v — want code %s and a reason", path, c.name, got, c.code)
			}
		}
	}

	// Exactly 31 days is allowed, and a to a moment ahead is cut to now.
	admin.expect(http.MethodGet, "/api/v1/cost/org?from="+rfc(now.Add(-31*24*time.Hour))+"&to="+rfc(now), nil, http.StatusOK)
	got := admin.expect(http.MethodGet, "/api/v1/cost/cells?from="+rfc(from)+"&to="+rfc(now.Add(time.Hour)), nil, http.StatusOK)
	if until, _ := got["until"].(string); until == "" || mustTime(t, until).After(time.Now().Add(time.Second)) {
		t.Errorf("a to after now is cut to now, got until = %q", until)
	}
}

// The bill is known per whole bucket, so a window asked for as [from, to) ends
// where it starts: on whole hours. The bill, the reported lines and the list
// price cover the same span — never a bill over one span beside lines over
// another.
func TestBilledWindowEndsOnWholeBuckets(t *testing.T) {
	f := newBillingFixture(t, "rechnung-bereich")
	now := time.Now().UTC()
	h0 := now.Add(-6 * time.Hour).Truncate(time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from)
		VALUES ($1, 'anthropic_api_key', 0, 'apikey_bereich', $2)`, f.s.orgID, now.Add(-10*24*time.Hour))
	cred := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "anthropic_api_key", Kind: daemon.CredAPIKey}
	for i := range 3 { // three billed hours, $1 each
		at := h0.Add(time.Duration(i) * time.Hour)
		f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
			amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,'1h',$2,$3,'apikey_bereich','claude-opus-5-5','tokens','output_tokens',100,100,$4,$4)`,
			f.s.orgID, at, at.Add(time.Hour), now)
		f.line(t, cred, at.Add(10*time.Minute), 50000) // $1 at the list, one per hour
	}

	// From the middle of hour 0 to the middle of hour 2: only hour 1 lies
	// wholly in it.
	from, to := h0.Add(20*time.Minute), h0.Add(2*time.Hour+30*time.Minute)
	w, err := f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, from, to, now)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Start.Equal(h0.Add(time.Hour)) || !w.End.Equal(h0.Add(2*time.Hour)) {
		t.Errorf("window %v – %v, want hour 1 (%v – %v)", w.Start, w.End, h0.Add(time.Hour), h0.Add(2*time.Hour))
	}
	if len(w.Totals) != 1 {
		t.Fatalf("totals %+v", w.Totals)
	}
	if b := w.Totals[0]; b.BilledUSD != "1.00000000" || math.Abs(b.ReportedUSD-1) > 1e-9 || math.Abs(b.ListUSD-1) > 1e-9 {
		t.Errorf("totals %+v — want $1 billed beside $1 reported and $1 recomputed: the lines of hours 0 and 2 stay out", b)
	}
	series, err := f.s.obs.BilledSeries(f.ctx, f.s.orgID, "hour", from, to)
	if err != nil {
		t.Fatal(err)
	}
	// The series reads whole buckets that end by to: hour 0 (it began before
	// from, the page sums from Start on) and hour 1, not hour 2.
	if len(series.Points) != 2 || !series.Points[0].Bucket.Equal(h0) || !series.Points[1].Bucket.Equal(h0.Add(time.Hour)) {
		t.Errorf("series points %+v, want the buckets of hours 0 and 1", series.Points)
	}

	// An end inside the first hour leaves nothing whole.
	w, err = f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, h0.Add(10*time.Minute), h0.Add(50*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if !w.End.Equal(w.Start) || (len(w.Totals) == 1 && (w.Totals[0].BilledUSD != "0.00000000" || w.Totals[0].ReportedUSD != 0)) {
		t.Errorf("a window inside one hour holds no bucket: %v – %v, totals %+v", w.Start, w.End, w.Totals)
	}

	// Open ended, the same window reads to now as it did.
	w, err = f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, from, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !w.End.Equal(now) || w.Totals[0].BilledUSD != "2.00000000" {
		t.Errorf("open window: end %v, billed %v — want now and hours 1 and 2 ($2)", w.End, w.Totals[0].BilledUSD)
	}
}
