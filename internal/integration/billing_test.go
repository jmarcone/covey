package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/billing"
	"covey/internal/daemon"
	"covey/internal/observability"
	"covey/internal/orchestrator"
	"covey/internal/secrets"
)

// fakeAdminKey is a made-up Admin API key; the tests look for it in every
// answer covey gives.
const fakeAdminKey = "sk-ant-admin01-FAKE-billing-test-key-0000"

// billedHour is one hour of the bill as the fake Usage and Cost Analytics API
// holds it: per token type the amount and the list price in cents, and the
// tokens. One hour per day and combination, so a day's bucket is that hour.
type billedHour struct {
	start                    time.Time
	apiKey, model            string
	tier                     string
	costs                    map[string][2]string // token_type (or "web_search") -> amount, list_amount
	in, w5, w1, rd, out, web int64
}

// fakeAnalytics serves /v1/organizations/analytics/{cost,usage} from hours,
// per bucket width and requested range, the way the API shapes its rows.
type fakeAnalytics struct {
	mu        sync.Mutex
	hours     []billedHour
	refreshed time.Time
	status    int // non-zero: every request answers this
	requests  int
	currency  string // of every cost row; empty: USD
	// at, when set, is the sync's clock (the client's Now); else the wall
	// clock.
	at time.Time
	// Per bucket width (1h, 1d): fail answers every request of that width
	// with the status, empty answers it with no rows and no
	// data_refreshed_at (a read that has not landed); widthRequests counts.
	fail          map[string]int
	empty         map[string]bool
	widthRequests map[string]int
}

// now is the client's clock.
func (f *fakeAnalytics) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.at.IsZero() {
		return f.at
	}
	return time.Now()
}

func (f *fakeAnalytics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Header.Get("x-api-key") != fakeAdminKey || r.Header.Get("anthropic-beta") != "analytics-usage-cost-2026-08-24" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid key `+r.Header.Get("x-api-key")+`"}}`)
		return
	}
	q := r.URL.Query()
	if f.widthRequests == nil {
		f.widthRequests = map[string]int{}
	}
	f.widthRequests[q.Get("time_bucket_width")]++
	if st := f.fail[q.Get("time_bucket_width")]; st != 0 {
		w.WriteHeader(st)
		io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"failed"}}`)
		return
	}
	if f.empty[q.Get("time_bucket_width")] {
		io.WriteString(w, `{"data":[],"has_more":false,"next_page":null,"data_refreshed_at":null}`)
		return
	}
	from, _ := time.Parse(time.RFC3339, q.Get("starting_at"))
	to, _ := time.Parse(time.RFC3339, q.Get("ending_at"))
	width := map[string]time.Duration{"1h": time.Hour, "1d": 24 * time.Hour}[q.Get("time_bucket_width")]
	group := func(h billedHour, costType, tokenType any) map[string]any {
		return map[string]any{"model": h.model, "api_key_id": h.apiKey, "workspace_id": "wrkspc_test",
			"service_tier": h.tier, "cost_type": costType, "token_type": tokenType, "speed": nil, "inference_geo": nil}
	}
	data := []any{}
	for _, h := range f.hours {
		if h.start.Before(from) || !h.start.Before(to) {
			continue
		}
		bs := h.start.Truncate(width)
		row := func(g map[string]any) map[string]any {
			return map[string]any{"starting_at": bs.Format(time.RFC3339), "ending_at": bs.Add(width).Format(time.RFC3339), "group": g}
		}
		if strings.HasSuffix(r.URL.Path, "/cost") {
			for tt, c := range h.costs {
				var g map[string]any
				if tt == "web_search" {
					g = group(h, "web_search", nil)
				} else {
					g = group(h, "tokens", tt)
				}
				x := row(g)
				currency := f.currency
				if currency == "" {
					currency = "USD"
				}
				x["cost"] = map[string]any{"currency": currency, "amount": c[0], "list_amount": c[1], "requests": nil}
				data = append(data, x)
			}
			continue
		}
		x := row(group(h, nil, nil))
		x["usage"] = map[string]any{"uncached_input_tokens": h.in, "cache_read_input_tokens": h.rd, "output_tokens": h.out,
			"cache_creation":  map[string]any{"ephemeral_5m_input_tokens": h.w5, "ephemeral_1h_input_tokens": h.w1},
			"server_tool_use": map[string]any{"web_search_requests": h.web}, "requests": 1}
		data = append(data, x)
	}
	var refreshed any
	if len(data) > 0 {
		refreshed = f.refreshed.Format(time.RFC3339)
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data, "has_more": false, "next_page": nil, "data_refreshed_at": refreshed})
}

// billingStack is a stack whose poller reads the fake.
func billingStack(t *testing.T, fake *fakeAnalytics) *stack {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return newStackWith(t, stackOpts{afterOrch: func(o *orchestrator.Orchestrator) {
		o.BillingClient = func(key string) *billing.Client {
			return &billing.Client{BaseURL: srv.URL, Key: key, Now: fake.now,
				Sleep: func(context.Context, time.Duration) error { return nil }}
		}
	}})
}

// The poller, the mapping and the reconciliation end to end: an API-key
// credential mapped to apikey_test, Claude Code lines on it over two UTC days,
// the bill of the same days from the fake. The first day matches to the cent;
// the second is past data_refreshed_at and ran in fast mode.
func TestBillingSyncAndReconcile(t *testing.T) {
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	dayA, dayB := today.Add(-48*time.Hour), today.Add(-24*time.Hour)
	fake := &fakeAnalytics{refreshed: dayB.Add(12 * time.Hour)}
	// Day A, 10:00: what session[0]'s line consumed (Opus and Haiku side
	// calls), priced per token type in cents; 10 % discount on Opus.
	fake.hours = []billedHour{
		{start: dayA.Add(10 * time.Hour), apiKey: "apikey_test", model: opus, tier: "standard",
			costs: map[string][2]string{
				"uncached_input_tokens":     {"0.021600", "0.024000"},
				"output_tokens":             {"61.642800", "68.492000"},
				"cache_read_input_tokens":   {"32.073174", "35.636860"},
				"ephemeral_1h_input_tokens": {"68.528880", "76.143200"},
			}, in: 60, w1: 95179, rd: 1781843, out: 34246},
		{start: dayA.Add(10 * time.Hour), apiKey: "apikey_test", model: haiku, tier: "standard",
			costs: map[string][2]string{
				"uncached_input_tokens": {"0.296810", "0.296810"},
				"output_tokens":         {"0.177600", "0.177600"},
				"web_search":            {"1.000000", "1.000000"},
			}, in: 29681, out: 3552, web: 1},
		// Day B: session[4]'s line, which ran in fast mode.
		{start: dayB.Add(9 * time.Hour), apiKey: "apikey_test", model: opus, tier: "standard",
			costs: map[string][2]string{"output_tokens": {"24.426000", "24.426000"}}, out: 12213},
		// Someone else's key on the same account, with an amount that must
		// survive to the cent.
		{start: dayA.Add(14 * time.Hour), apiKey: "apikey_other", model: opus, tier: "standard",
			costs: map[string][2]string{"output_tokens": {"123.456789", "123.456789"}}, out: 61728},
	}
	s := billingStack(t, fake)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("rechnung")

	// Nothing to read without the key.
	admin.expect(http.MethodPost, "/api/v1/cost/billing-sync", nil, http.StatusConflict)
	if st := admin.expect(http.MethodGet, "/api/v1/cost/billing-sync", nil, http.StatusOK); st["configured"] != false {
		t.Fatalf("billing-sync without a key: %v", st)
	}
	if err := s.secrets.Put(ctx, s.orgID, secrets.AdminKey, fakeAdminKey); err != nil {
		t.Fatal(err)
	}

	// The credential: an API key on a Claude Code runtime, stored five days
	// ago (its mapping starts when the value was written).
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredAPIKey, "anthropic_api_key", "sk-ant-api03-billing-test", "Schlüssel")
	if _, err := s.pool.Exec(ctx, `UPDATE secrets SET updated_at = $2 WHERE org_id = $1 AND key = 'anthropic_api_key'`,
		s.orgID, today.Add(-5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	place := fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/billing-key", rt.ID, ord)
	admin.expect(http.MethodPatch, place, map[string]string{"api_key_id": "not an id!"}, http.StatusBadRequest)
	m := admin.expect(http.MethodPatch, place, map[string]string{"api_key_id": "apikey_test"}, http.StatusOK)
	if m["secret_key"] != "anthropic_api_key" || m["valid_to"] != nil {
		t.Fatalf("mapping = %v", m)
	}

	// Claude Code's lines on the credential: day A 10:20, day B 09:30 (fast).
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "anthropic_api_key", Kind: daemon.CredAPIKey}
	line := func(n int, at time.Time, fast bool) {
		t.Helper()
		task := uuid.New()
		raw := session[n].json("sess-"+task.String(), n, true)
		if fast {
			raw = strings.Replace(raw, `"speed":"standard"`, `"speed":"fast"`, 1)
		}
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, cred, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE model_usage SET created_at = $2 WHERE task_id = $1`, task, at); err != nil {
			t.Fatal(err)
		}
	}
	line(0, dayA.Add(10*time.Hour+20*time.Minute), false)
	line(4, dayB.Add(9*time.Hour+30*time.Minute), true)
	// A run on the credential that left no result line.
	cut := uuid.New()
	id, err := s.obs.BookCost(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &cut, USD: 0.5, Model: opus, Credential: cred})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE cost_entries SET created_at = $2 WHERE id = $1`, id, dayA.Add(11*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Sync twice: the second replaces the first, row for row.
	count := func() (c, u int) {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM billed_cost WHERE org_id = $1),
			(SELECT count(*) FROM billed_usage WHERE org_id = $1)`, s.orgID).Scan(&c, &u); err != nil {
			t.Fatal(err)
		}
		return
	}
	s.orch.SyncBilling(ctx)
	c1, u1 := count()
	// 1h and 1d: 4 + 3 + 1 + 1 cost rows and 4 usage rows, each width.
	if c1 != 18 || u1 != 8 {
		t.Fatalf("after the first sync: %d cost rows, %d usage rows; want 18 and 8", c1, u1)
	}
	st := admin.expect(http.MethodGet, "/api/v1/cost/billing-sync", nil, http.StatusOK)
	if st["configured"] != true || st["last_status"] != "ok" || st["last_error"] != "" || st["last_daily_at"] == nil {
		t.Fatalf("billing-sync = %v", st)
	}
	s.orch.SyncBilling(ctx)
	if c2, u2 := count(); c2 != c1 || u2 != u1 {
		t.Fatalf("the second sync left %d / %d rows, want %d / %d", c2, u2, c1, u2)
	}
	var cents string
	if err := s.pool.QueryRow(ctx, `SELECT amount_cents::text FROM billed_cost
		WHERE org_id = $1 AND api_key_id = 'apikey_other' AND bucket_width = '1h'`, s.orgID).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != "123.45678900" {
		t.Errorf("amount_cents = %s, want 123.456789 exactly", cents)
	}

	// The reconciliation over the API: money as strings.
	rec := admin.expect(http.MethodGet, "/api/v1/cost/reconcile?days=5", nil, http.StatusOK)
	days, _ := rec["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("reconcile days = %v", rec["days"])
	}
	a, b := days[0].(map[string]any), days[1].(map[string]any)
	if a["day"] != dayA.Format("2006-01-02") || a["api_key_id"] != "apikey_test" || a["complete"] != true {
		t.Errorf("day A = %v", a)
	}
	if a["runs_without_result"] != float64(1) {
		t.Errorf("day A runs without result = %v, want 1", a["runs_without_result"])
	}
	models := map[string]map[string]any{}
	for _, x := range a["models"].([]any) {
		mm := x.(map[string]any)
		models[mm["model"].(string)] = mm
	}
	o := models[opus]
	// reported is Claude Code's costUSD as model_usage keeps it (six places).
	for k, want := range map[string]string{"reported_usd": "1.80296100", "list_usd": "1.80296060",
		"billed_list_usd": "1.80296060", "billed_usd": "1.62266454", "residual_usd": "0.00000000",
		"discount_usd": "0.18029606", "residual_label": "match"} {
		if o[k] != want {
			t.Errorf("day A opus %s = %v, want %s", k, o[k], want)
		}
	}
	for _, x := range o["tokens"].([]any) {
		td := x.(map[string]any)
		if td["diff"] != float64(0) {
			t.Errorf("day A opus tokens %v differ", td)
		}
	}
	h := models[haiku]
	if h["list_usd"] != "0.01474410" || h["billed_list_usd"] != "0.01474410" || h["residual_label"] != "match" {
		t.Errorf("day A haiku = %v", h)
	}
	if b["complete"] != false {
		t.Errorf("day B is past data_refreshed_at, but complete: %v", b)
	}
	bo := b["models"].([]any)[0].(map[string]any)
	if bo["not_comparable"] != true || !strings.Contains(bo["not_comparable_reason"].(string), "speed fast") ||
		bo["residual_label"] != "not_comparable" {
		t.Errorf("day B opus = %v", bo)
	}

	// A revised amount replaces the old one: the hourly re-read is the
	// fresher read of the day, so the reconciliation follows it.
	fake.mu.Lock()
	fake.hours[0].costs["output_tokens"] = [2]string{"61.000000", "68.492000"}
	fake.mu.Unlock()
	s.orch.SyncBilling(ctx)
	if err := s.pool.QueryRow(ctx, `SELECT amount_cents::text FROM billed_cost WHERE org_id = $1 AND bucket_width = '1h'
		AND api_key_id = 'apikey_test' AND model = $2 AND token_type = 'output_tokens' AND bucket_start = $3`,
		s.orgID, opus, dayA.Add(10*time.Hour)).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != "61.00000000" {
		t.Errorf("revised amount = %s, want 61", cents)
	}
	if c3, _ := count(); c3 != c1 {
		t.Errorf("the revision changed the row count: %d, want %d", c3, c1)
	}
	got, err := s.obs.Reconcile(ctx, s.orgID, dayA, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, mm := range got.Days[0].Models {
		if mm.Model == opus && mm.BilledUSD != "1.61623654" {
			t.Errorf("day A opus billed after the revision = %s, want 1.61623654", mm.BilledUSD)
		}
	}

	// The bill per day and the key list: apikey_other is other use.
	billed := admin.expect(http.MethodGet, "/api/v1/cost/billed?days=5&bucket=day", nil, http.StatusOK)
	if pts, _ := billed["points"].([]any); len(pts) != 4 {
		t.Errorf("billed points = %v", billed["points"])
	}
	keys := admin.expect(http.MethodGet, "/api/v1/cost/billing-keys", nil, http.StatusOK)
	var other, mapped bool
	for _, x := range keys["keys"].([]any) {
		k := x.(map[string]any)
		switch k["api_key_id"] {
		case "apikey_other":
			other = k["secret_key"] == nil && k["billed_usd"] == "1.23456789"
		case "apikey_test":
			mapped = k["secret_key"] == "anthropic_api_key"
		}
	}
	if !other || !mapped {
		t.Errorf("billing keys = %v", keys["keys"])
	}

	// A failed read keeps the rows and says why — without the key.
	fake.mu.Lock()
	fake.status = http.StatusUnauthorized
	fake.mu.Unlock()
	admin.expect(http.MethodPost, "/api/v1/cost/billing-sync", nil, http.StatusBadGateway)
	if c4, u4 := count(); c4 != c1 || u4 != u1 {
		t.Errorf("a failed sync changed the rows: %d / %d", c4, u4)
	}
	st = admin.expect(http.MethodGet, "/api/v1/cost/billing-sync", nil, http.StatusOK)
	lastErr, _ := st["last_error"].(string)
	if st["last_status"] != "failed" || !strings.Contains(lastErr, "401") || strings.Contains(lastErr, fakeAdminKey) ||
		st["last_success_at"] == nil {
		t.Errorf("billing-sync after a failure = %v", st)
	}

	// The mapping ends with its value: a rotation closes it.
	if err := s.secrets.Replace(ctx, secrets.Ref{OrgID: s.orgID, Key: "anthropic_api_key"}, "sk-ant-api03-rotated",
		secrets.Lifetime{}); err != nil {
		t.Fatal(err)
	}
	var closed bool
	if err := s.pool.QueryRow(ctx, `SELECT bool_and(valid_to IS NOT NULL) FROM credential_billing_keys
		WHERE org_id = $1 AND secret_key = 'anthropic_api_key'`, s.orgID).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Error("the mapping stayed open after the value was replaced")
	}
}

// The bill beside the lines covers the same window as the lines: whole billed
// buckets from the first one at or after `since` — hours within the hourly
// reach, UTC days before it — and the reported and recomputed figures of the
// credential are cut to exactly that window. A bucket that straddles `since`
// is not billed in part; a mapped credential without a bill is listed as
// mapped, with nothing billed.
func TestBillingWindowSameAsTheLines(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("rechnungsfenster")
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)
	start := since.Truncate(time.Hour).Add(time.Hour)
	if since.Truncate(time.Hour).Equal(since) {
		start = since
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from)
		VALUES ($1, 'anthropic_api_key', 0, 'apikey_win', $2), ($1, 'anthropic_api_key', 1, 'apikey_quiet', $2)`,
		s.orgID, now.Add(-10*24*time.Hour))
	bill := func(width string, at time.Time, key, amount, list string, fetched time.Time) {
		t.Helper()
		d := map[string]time.Duration{"1h": time.Hour, "1d": 24 * time.Hour}[width]
		exec(`INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
			amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,$2,$3,$4,$5,'claude-opus-5-5','tokens','output_tokens',$6::numeric * 100,$7::numeric * 100,$8,$9)`,
			s.orgID, width, at, at.Add(d), key, amount, list, now.Add(-6*time.Hour), fetched)
	}
	hourly, daily := now.Add(-time.Hour), now.Add(-2*time.Hour)           // the hourly read is the fresher one
	bill("1h", since.Truncate(time.Hour), "apikey_win", "7", "7", hourly) // straddles since: not in the 24 h window
	bill("1h", start.Add(2*time.Hour), "apikey_win", "3", "4", hourly)
	bill("1h", start.Add(10*time.Hour), "apikey_win", "2", "2", hourly)
	bill("1h", start.Add(3*time.Hour), "apikey_other", "50", "50", hourly) // not mapped: other use
	bill("1d", since.Truncate(24*time.Hour), "apikey_win", "100", "100", daily)
	weekAgo := now.Add(-7 * 24 * time.Hour)
	bill("1d", weekAgo.Truncate(24*time.Hour), "apikey_win", "20", "20", daily) // straddles a 7-day since
	bill("1d", weekAgo.Truncate(24*time.Hour).Add(24*time.Hour), "apikey_win", "10", "10", daily)

	cred := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "anthropic_api_key", Kind: daemon.CredAPIKey}
	line := func(at time.Time, outputTokens int) {
		t.Helper()
		task := uuid.New()
		l := fmt.Sprintf(`{"type":"result","session_id":"s-%s","modelUsage":{"claude-opus-5-5":{"costUSD":1,"outputTokens":%d}}}`, task, outputTokens)
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, cred, json.RawMessage(l)); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE model_usage SET created_at = $1 WHERE task_id = $2`, at, task)
	}
	line(start.Add(-time.Second), 250000)              // before the window's first whole hour ($5)
	line(start.Add(2*time.Hour+5*time.Minute), 125000) // $2.50
	line(start.Add(10*time.Hour), 50000)               // $1

	total := func(w observability.BilledWindow, slot int) observability.BilledTotal {
		t.Helper()
		for _, b := range w.Totals {
			if b.SecretKey == "anthropic_api_key" && b.SecretSlot == slot {
				return b
			}
		}
		t.Fatalf("no total for slot %d in %+v", slot, w.Totals)
		return observability.BilledTotal{}
	}
	day, err := s.obs.BilledWindowFor(ctx, s.orgID, since, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if day.Width != billing.Hour || !day.Start.Equal(start) || len(day.Totals) != 2 {
		t.Fatalf("24 h window: %s from %v with %d totals, want 1h from %v and 2", day.Width, day.Start, len(day.Totals), start)
	}
	if b := total(day, 0); !b.HasBilled || b.BilledUSD != "5.00000000" || b.BilledListUSD != "6.00000000" ||
		math.Abs(b.ReportedUSD-2) > 1e-9 || math.Abs(b.ListUSD-3.5) > 1e-9 {
		t.Errorf("24 h window, the mapped key: %+v — want billed 5, list 6, reported 2, recomputed 3.50", b)
	}
	if q := total(day, 1); q.HasBilled || q.BilledUSD != "0.00000000" {
		t.Errorf("a mapped credential without a bill: %+v", q)
	}

	week, err := s.obs.BilledWindowFor(ctx, s.orgID, weekAgo, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := weekAgo.Truncate(24 * time.Hour).Add(24 * time.Hour); week.Width != billing.Day || !week.Start.Equal(want) {
		t.Fatalf("7-day window: %s from %v, want 1d from %v", week.Width, week.Start, want)
	}
	// The whole days from the first midnight after since: the 1d row of that
	// day, and the days the hourly read is fresher for, from their hours.
	if b := total(week, 0); b.BilledUSD != "22.00000000" || b.BilledListUSD != "23.00000000" ||
		math.Abs(b.ReportedUSD-3) > 1e-9 || math.Abs(b.ListUSD-8.5) > 1e-9 {
		t.Errorf("7-day window, the mapped key: %+v — want billed 22, list 23, reported 3, recomputed 8.50", b)
	}

	got := admin.expect(http.MethodGet, "/api/v1/cost/billed?days=1&bucket=hour", nil, http.StatusOK)
	w, _ := got["window"].(map[string]any)
	if w == nil || w["width"] != "1h" || len(w["totals"].([]any)) != 2 {
		t.Errorf("GET /cost/billed window = %v", got["window"])
	}
}

// Empty data with data_refreshed_at null has not landed: the sync says so and
// replaces nothing.
func TestBillingNotLanded(t *testing.T) {
	ctx := context.Background()
	fake := &fakeAnalytics{}
	s := billingStack(t, fake)
	if err := s.secrets.Put(ctx, s.orgID, secrets.AdminKey, fakeAdminKey); err != nil {
		t.Fatal(err)
	}
	if err := s.orch.SyncBillingOrg(ctx, s.orgID, true); err != nil {
		t.Fatal(err)
	}
	st, err := s.obs.BillingSync(ctx, s.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastStatus != "not_landed" || st.DataRefreshedAt != nil {
		t.Errorf("status = %+v, want not_landed without data_refreshed_at", st)
	}
}

// anthropic_admin_key is org-wide only: no assignment, no agent's own secret,
// no runtime credential; an admin key under another name is refused; and no
// answer — the secrets, the runtimes, any cost endpoint — carries its value.
func TestBillingAdminKeyReserved(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("ohne-admin-key")

	// The save check: a non-admin value under the admin key is flagged,
	// without a network call (the prefix says what it is).
	got := admin.expect(http.MethodPut, "/api/v1/secrets/"+secrets.AdminKey,
		map[string]any{"value": "sk-ant-api03-not-an-admin-key-000000"}, http.StatusOK)
	check, _ := got["check"].(map[string]any)
	if check["checked"] != true || check["valid"] != false || !strings.Contains(fmt.Sprint(check["hint"]), "anthropic_api_key") {
		t.Errorf("check of an API key under anthropic_admin_key = %v", check)
	}
	if err := s.secrets.Put(ctx, s.orgID, secrets.AdminKey, fakeAdminKey); err != nil {
		t.Fatal(err)
	}

	// Every way towards an agent is refused with a hint.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/v1/secrets/" + secrets.AdminKey + "/agents/" + agent.ID.String(), nil},
		{http.MethodPut, "/api/v1/agents/" + agent.ID.String() + "/secrets/" + secrets.AdminKey, map[string]any{"value": fakeAdminKey}},
		{http.MethodPut, "/api/v1/agents/" + agent.ID.String() + "/secrets/ANTHROPIC_ADMIN_KEY", map[string]any{"value": "x-value-1234567"}},
		{http.MethodPut, "/api/v1/secrets/anthropic_api_key", map[string]any{"value": fakeAdminKey}},
	} {
		resp := admin.do(c.method, c.path, c.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "anthropic_admin_key") {
			t.Errorf("%s %s: HTTP %d %s, want 400 with a hint", c.method, c.path, resp.StatusCode, body)
		}
	}
	rt := admin.expect(http.MethodPost, "/api/v1/runtime-instances",
		map[string]any{"engine": "claude-code", "display_name": "Claude"}, http.StatusOK)
	resp := admin.do(http.MethodPost, "/api/v1/runtime-instances/"+rt["id"].(string)+"/credentials",
		map[string]any{"kind": daemon.CredAPIKey, "secret_key": secrets.AdminKey})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("admin key as a runtime credential: HTTP %d, want 400", resp.StatusCode)
	}
	if _, err := s.runtimes.AddCredential(ctx, s.orgID, uuid.MustParse(rt["id"].(string)), daemon.CredAPIKey, secrets.AdminKey, 0, ""); err == nil {
		t.Error("the store took the admin key as a runtime credential")
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM secret_assignments WHERE key = $1)
		+ (SELECT count(*) FROM secrets WHERE agent_id IS NOT NULL AND lower(key) = $1)
		+ (SELECT count(*) FROM runtime_credentials WHERE secret_key = $1)`, secrets.AdminKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the admin key reached %d agent paths", n)
	}

	// It is sensitive from the first write, and no answer carries it.
	for _, path := range []string{"/api/v1/secrets", "/api/v1/runtime-instances", "/api/v1/cost/billed",
		"/api/v1/cost/billed?bucket=hour", "/api/v1/cost/reconcile", "/api/v1/cost/billing-sync",
		"/api/v1/cost/billing-keys", "/api/v1/cost/cells", "/api/v1/cost/models", "/api/v1/cost/usage-readings"} {
		resp := admin.do(http.MethodGet, path, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		if strings.Contains(string(body), fakeAdminKey) || strings.Contains(string(body), "FAKE-billing") {
			t.Errorf("GET %s carries the admin key", path)
		}
	}
	for _, p := range admin.expectList(http.MethodGet, "/api/v1/secrets", nil, http.StatusOK) {
		if p["key"] == secrets.AdminKey && (p["sensitive"] != true || p["value"] != nil) {
			t.Errorf("admin key preview = %v", p)
		}
	}
}
