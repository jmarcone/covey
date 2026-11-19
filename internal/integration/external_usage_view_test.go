package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/identity"
	"covey/internal/observability"
)

// The detailed view of the sessions outside covey (#120, part 3/3) reads
// GET /api/v1/cost/external-usage: the outside cells one level finer than
// /cost/cells, per model. These tests hold that the finer cells add up to
// /cost/cells' cells exactly, that an unknown model has no figure, that the
// 10-hour buckets are sums of their 10-minute ones, that the meter share is
// /cost/cells' own, who may read it, and that no agents' figure moves.

const externalViewPath = "/api/v1/cost/external-usage"

func viewGet(t *testing.T, c *apiClient, query string) observability.ExternalModelUsage {
	t.Helper()
	var v observability.ExternalModelUsage
	if err := json.Unmarshal(rawGet(t, c, externalViewPath+"?"+query), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// viewRow is one upload row with its own token counts.
func viewRow(id string, ts time.Time, model, origin string, in, out, cr, w5, w1 int64, reported any) map[string]any {
	r := map[string]any{"host": "laptop", "session_id": "s-1", "message_id": id,
		"ts": ts.UTC().Format(time.RFC3339Nano), "model": model,
		"input": in, "output": out, "cache_read": cr, "cache_write_5m": w5, "cache_write_1h": w1}
	if origin != "" {
		r["origin"] = origin
	}
	if reported != nil {
		r["reported_usd"] = reported
	}
	return r
}

type viewKey struct {
	period time.Time
	source string
	origin string
}

func originKey(o *string) string {
	if o == nil {
		return "<nil>"
	}
	return *o
}

// sumByModel adds the finer cells up to /cost/cells' grain.
func sumByModel(cells []observability.ExternalModelCell) map[viewKey]*observability.ExternalCell {
	out := map[viewKey]*observability.ExternalCell{}
	for _, c := range cells {
		k := viewKey{c.Period.UTC(), c.Source, originKey(c.Origin)}
		e, ok := out[k]
		if !ok {
			e = &observability.ExternalCell{Period: k.period, Source: c.Source, Origin: c.Origin,
				Measured: map[string]float64{}, Tokens: map[string]int64{}}
			out[k] = e
		}
		e.Calls += c.Calls
		for ty, n := range c.Tokens {
			e.Tokens[ty] += n
		}
		for ty, usd := range c.Measured {
			e.Measured[ty] += usd
		}
		if c.ListUSD != nil {
			v := *c.ListUSD
			if e.ListUSD != nil {
				v += *e.ListUSD
			}
			e.ListUSD = &v
		}
		if c.ReportedUSD != nil {
			v := *c.ReportedUSD
			if e.ReportedUSD != nil {
				v += *e.ReportedUSD
			}
			e.ReportedUSD = &v
		}
		e.ReportedCalls += c.ReportedCalls
		e.UnpricedTokens += c.UnpricedTokens
		e.UnpricedCalls += c.UnpricedCalls
	}
	return out
}

func usdEq(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Both are float sums of the same F1 figures, added in another order.
	return math.Abs(*a-*b) <= 1e-9
}

// outsideFixture: a subscription seat with a plan, a covey line and meter
// readings, and outside calls of several models, origins and sources. It
// returns the hour the calls start in.
func outsideFixture(t *testing.T, s *stack, admin *apiClient, withRows bool) time.Time {
	t.Helper()
	ctx := context.Background()
	agent := s.newSupportAgent("nebenan")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	h := time.Now().UTC().Truncate(time.Hour).Add(-5 * time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": h.Add(-48 * time.Hour)}, http.StatusOK)
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	f.line(t, cred, h.Add(10*time.Minute), 50_000)
	f.line(t, cred, h.Add(70*time.Minute), 80_000)
	if err := s.obs.AddCost(ctx, agent.ID, nil, 1.25, observability.Tokens{Input: 1000, Output: 200}, "claude-opus-5-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{h.Add(-time.Hour), 10}, {h.Add(30 * time.Minute), 14}, {h.Add(90 * time.Minute), 17}, {h.Add(150 * time.Minute), 18}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	if !withRows {
		return h
	}
	uploadOutside(t, admin, h, true)
	return h
}

func uploadOutside(t *testing.T, admin *apiClient, h time.Time, link bool) {
	t.Helper()
	models := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-5-7"} // the last is unknown to the list
	origins := []string{"main", "subagent", "workflow", ""}
	home := []map[string]any{}
	for i := 0; i < 36; i++ {
		var reported any
		if i%5 == 0 {
			reported = 0.01 * float64(i+1)
		}
		home = append(home, viewRow(fmt.Sprintf("h%d", i), h.Add(time.Duration(i)*7*time.Minute+13*time.Second),
			models[i%3], origins[i%4], int64(10+i), int64(300*i+7), int64(100_000+i*1_000), int64(i%2*500), int64(i%3*200), reported))
	}
	up := extUpload("operator: home", home...)
	if link {
		up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": 0}
		// The laptop has sent every call through now: its readings are split.
		up["reported_through"] = map[string]any{"laptop": time.Now().UTC().Format(time.RFC3339Nano)}
	}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	work := []map[string]any{}
	for i := 0; i < 9; i++ {
		work = append(work, viewRow(fmt.Sprintf("w%d", i), h.Add(time.Duration(i)*23*time.Minute),
			[]string{"claude-haiku-5-5", "claude-sonnet-5-7"}[i%2], origins[i%3], 5, int64(40*i), 9_000, 0, 120, nil))
	}
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work", work...), http.StatusOK)
}

// Σ over models equals /cost/cells' external cells per (period, source,
// origin), in calls, every token type, list $, cost types, reported $ and
// unpriced counts; the sources and the meter share are /cost/cells' own.
func TestExternalUsageByModelAddsUpToTheCells(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	outsideFixture(t, s, admin, true)

	for _, bucket := range []string{"10min", "hour", "10hour", "day"} {
		q := "days=1&bucket=" + bucket
		view := viewGet(t, admin, q)
		cells := cellsExternal(t, admin, q)
		if view.Bucket != bucket {
			t.Errorf("%s: bucket = %q", bucket, view.Bucket)
		}
		if len(view.Models) != 4 {
			t.Errorf("%s: models = %+v, want opus, sonnet, haiku and the unknown one", bucket, view.Models)
		}
		got := sumByModel(view.Cells)
		if len(got) != len(cells.Cells) {
			t.Fatalf("%s: %d (period, source, origin) groups per model, %d cells", bucket, len(got), len(cells.Cells))
		}
		for _, want := range cells.Cells {
			k := viewKey{want.Period.UTC(), want.Source, originKey(want.Origin)}
			g, ok := got[k]
			if !ok {
				t.Errorf("%s: no model cells for %+v", bucket, k)
				continue
			}
			if g.Calls != want.Calls || g.UnpricedTokens != want.UnpricedTokens || g.UnpricedCalls != want.UnpricedCalls ||
				g.ReportedCalls != want.ReportedCalls {
				t.Errorf("%s %+v: calls/unpriced/reported calls %d/%d/%d/%d, want %d/%d/%d/%d", bucket, k,
					g.Calls, g.UnpricedTokens, g.UnpricedCalls, g.ReportedCalls, want.Calls, want.UnpricedTokens, want.UnpricedCalls, want.ReportedCalls)
			}
			for _, ty := range []string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h"} {
				if g.Tokens[ty] != want.Tokens[ty] {
					t.Errorf("%s %+v: %s tokens %d, want %d", bucket, k, ty, g.Tokens[ty], want.Tokens[ty])
				}
			}
			if !usdEq(g.ListUSD, want.ListUSD) || !usdEq(g.ReportedUSD, want.ReportedUSD) {
				t.Errorf("%s %+v: list %v reported %v, want %v %v", bucket, k, g.ListUSD, g.ReportedUSD, want.ListUSD, want.ReportedUSD)
			}
			for ty, usd := range want.Measured {
				if math.Abs(g.Measured[ty]-usd) > 1e-9 {
					t.Errorf("%s %+v: measured %s %v, want %v", bucket, k, ty, g.Measured[ty], usd)
				}
			}
		}
		// Every token type's $ adds up to the cell's list $ (F1).
		for _, c := range view.Cells {
			if c.ListUSD == nil {
				continue
			}
			var sum float64
			for _, v := range c.TypeUSD {
				sum += v
			}
			if math.Abs(sum-*c.ListUSD) > 1e-9 {
				t.Errorf("%s: %s %v: type $ add up to %v, list $ %v", bucket, c.Model, c.Period, sum, *c.ListUSD)
			}
		}
		if a, b := mustJSON(t, view.Meter), mustJSON(t, cells.Meter); a != b {
			t.Errorf("%s: the meter differs from /cost/cells':\nview  %s\ncells %s", bucket, a, b)
		}
		if a, b := mustJSON(t, view.Sources), mustJSON(t, cells.Sources); a != b {
			t.Errorf("%s: the sources differ from /cost/cells':\nview  %s\ncells %s", bucket, a, b)
		}
		if view.Meter.Share == nil {
			t.Errorf("%s: no meter share with a linked source and readings", bucket)
		}
	}

	// ?source= narrows the cells, not the sources or the meter.
	all := viewGet(t, admin, "days=1&bucket=hour")
	work := viewGet(t, admin, "days=1&bucket=hour&source="+strings.ReplaceAll("operator: work", " ", "%20"))
	if work.Source == nil || *work.Source != "operator: work" {
		t.Errorf("source = %v", work.Source)
	}
	if len(work.Cells) == 0 {
		t.Fatal("no cells for operator: work")
	}
	n := 0
	for _, c := range all.Cells {
		if c.Source == "operator: work" {
			n++
		}
	}
	for _, c := range work.Cells {
		if c.Source != "operator: work" {
			t.Errorf("a cell of %q with ?source=operator: work", c.Source)
		}
	}
	if len(work.Cells) != n {
		t.Errorf("%d cells with ?source=, %d of the source without it", len(work.Cells), n)
	}
	if mustJSON(t, work.Meter) != mustJSON(t, all.Meter) || len(work.Sources) != 2 {
		t.Errorf("?source= changed the meter or the sources: %d sources", len(work.Sources))
	}
	resp := admin.do(http.MethodGet, externalViewPath+"?source="+strings.Repeat("s", 65), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an overlong source: HTTP %d, want 400", resp.StatusCode)
	}
}

// A model the list does not know: list_usd is JSON null (never 0), no
// cost-type figures, and its tokens and calls count as unpriced.
func TestExternalUsageByModelUnknownModelIsUnpriced(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	at := time.Now().Add(-30 * time.Minute)
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work",
		viewRow("u1", at, "claude-sonnet-5-7", "main", 10, 50, 2000, 0, 0, nil),
		viewRow("p1", at, "claude-opus-5-5", "main", 10, 50, 2000, 0, 0, nil)), http.StatusOK)

	var raw struct {
		Models []observability.ExternalModelName `json:"models"`
		Cells  []map[string]json.RawMessage      `json:"cells"`
	}
	if err := json.Unmarshal(rawGet(t, admin, externalViewPath+"?days=1&bucket=10min"), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Cells) != 2 {
		t.Fatalf("%d cells, want 2", len(raw.Cells))
	}
	for _, c := range raw.Cells {
		var model string
		_ = json.Unmarshal(c["model"], &model)
		var unpriced int64
		_ = json.Unmarshal(c["unpriced_tokens"], &unpriced)
		switch model {
		case "claude-sonnet-5-7":
			if string(c["list_usd"]) != "null" || string(c["type_usd"]) != "{}" || string(c["measured"]) != "{}" || unpriced != 2060 {
				t.Errorf("unknown model: list_usd %s type_usd %s measured %s unpriced %d; want null, {}, {}, 2060",
					c["list_usd"], c["type_usd"], c["measured"], unpriced)
			}
		case "claude-opus-5-5":
			if string(c["list_usd"]) == "null" || unpriced != 0 {
				t.Errorf("priced model: list_usd %s unpriced %d", c["list_usd"], unpriced)
			}
		default:
			t.Errorf("unexpected model %q", model)
		}
	}
	for _, m := range raw.Models {
		if m.Priced != (m.Model == "claude-opus-5-5") {
			t.Errorf("model %q priced = %v", m.Model, m.Priced)
		}
	}
}

// F2 at 10 hours: every 10-hour cell of a model equals the sum of the
// 10-minute cells inside it, in tokens exactly and in $.
func TestExternalUsageByModelTenHoursAreSumsOfTenMinutes(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	h := time.Now().UTC().Truncate(time.Hour).Add(-5 * time.Hour)
	uploadOutside(t, admin, h, false)

	tens := viewGet(t, admin, "days=1&bucket=10min")
	hours := viewGet(t, admin, "days=1&bucket=10hour")
	type k struct {
		p                     time.Time
		source, origin, model string
	}
	type agg struct {
		calls  int64
		tokens map[string]int64
		usd    *float64
	}
	sums := map[k]*agg{}
	for _, c := range tens.Cells {
		if c.Period.UnixNano()%int64(10*time.Minute) != 0 {
			t.Errorf("10min period %v is not on a 10-minute boundary", c.Period)
		}
		key := k{time.Unix(0, c.Period.UnixNano()-c.Period.UnixNano()%int64(10*time.Hour)).UTC(), c.Source, originKey(c.Origin), c.Model}
		a, ok := sums[key]
		if !ok {
			a = &agg{tokens: map[string]int64{}}
			sums[key] = a
		}
		a.calls += c.Calls
		for ty, n := range c.Tokens {
			a.tokens[ty] += n
		}
		if c.ListUSD != nil {
			v := *c.ListUSD
			if a.usd != nil {
				v += *a.usd
			}
			a.usd = &v
		}
	}
	if len(hours.Cells) != len(sums) {
		t.Fatalf("%d 10-hour cells, %d groups of 10-minute cells", len(hours.Cells), len(sums))
	}
	for _, c := range hours.Cells {
		if c.Period.UnixNano()%int64(10*time.Hour) != 0 {
			t.Errorf("10hour period %v is not on a 10-hour boundary", c.Period)
		}
		a := sums[k{c.Period.UTC(), c.Source, originKey(c.Origin), c.Model}]
		if a == nil {
			t.Errorf("no 10-minute cells for %v %s %s", c.Period, c.Source, c.Model)
			continue
		}
		if a.calls != c.Calls || !usdEq(a.usd, c.ListUSD) {
			t.Errorf("%v %s %s: calls %d $ %v, sum of 10 min %d %v", c.Period, c.Source, c.Model, c.Calls, c.ListUSD, a.calls, a.usd)
		}
		for ty, n := range c.Tokens {
			if a.tokens[ty] != n {
				t.Errorf("%v %s %s: %s %d, sum of 10 min %d", c.Period, c.Source, c.Model, ty, n, a.tokens[ty])
			}
		}
	}
}

// Who may read it: every role of the organisation (anyRole, like
// /cost/cells), nobody without a session or key.
func TestExternalUsageByModelAccess(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	admin.expect(http.MethodPost, externalPath, extUpload("operator: home",
		viewRow("a1", time.Now().Add(-time.Hour), "claude-opus-5-5", "main", 1, 2, 3, 0, 0, nil)), http.StatusOK)

	s.mitglied(t, "auditor@test.local", "Auditor", identity.RoleAuditor, "auditor-passwort")
	auditor := login(t, s, "auditor@test.local", "auditor-passwort")
	if v := viewGet(t, auditor, "days=1&bucket=hour"); len(v.Cells) != 1 {
		t.Errorf("an auditor reads %d cells, want 1", len(v.Cells))
	}
	resp, err := http.Get(s.http.URL + externalViewPath + "?days=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a session: HTTP %d, want 401", resp.StatusCode)
	}
}

// Two organisations with a source of the same name: each reads its own rows
// only, with and without ?source=; the other's never show, not as a cell, a
// source, a model or a meter step.
func TestExternalUsageByModelStaysInItsOrganisation(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	at := time.Now().Add(-time.Hour)
	admin.expect(http.MethodPost, externalPath, extUpload("operator: home",
		viewRow("a1", at, "claude-opus-5-5", "main", 1, 2, 3, 0, 0, nil),
		viewRow("a2", at, "claude-opus-5-5", "subagent", 1, 2, 3, 0, 0, nil)), http.StatusOK)

	second := uuid.New()
	if _, err := s.pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, 'Zweite GmbH')`, second); err != nil {
		t.Fatal(err)
	}
	var account uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT account_id FROM humans WHERE id = $1`, s.adminID).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO humans (id, org_id, account_id, email, display_name, password_hash, role)
		VALUES ($1, $2, $3, 'admin@test.local', 'Admin', ' ', 'org_admin')`, uuid.New(), second, account); err != nil {
		t.Fatal(err)
	}
	other := login(t, s, "admin@test.local", "admin-passwort")
	other.expect(http.MethodPost, "/api/v1/auth/switch-org", map[string]any{"org_id": second.String()}, http.StatusOK)

	// Before the second organisation has a row: nothing of the first's.
	for _, q := range []string{"days=1&bucket=hour", "days=1&bucket=10min&source=operator%3A%20home"} {
		if v := viewGet(t, other, q); len(v.Cells) != 0 || len(v.Sources) != 0 || len(v.Models) != 0 || len(v.Meter.Steps) != 0 {
			t.Errorf("%s: the second organisation reads %d cells, %d sources, %d models, %d steps of the first",
				q, len(v.Cells), len(v.Sources), len(v.Models), len(v.Meter.Steps))
		}
	}

	other.expect(http.MethodPost, externalPath, extUpload("operator: home",
		viewRow("b1", at, "claude-haiku-5-5", "main", 1, 2, 3, 0, 0, nil)), http.StatusOK)
	check := func(who string, c *apiClient, calls int64, model string) {
		t.Helper()
		for _, q := range []string{"days=1&bucket=hour", "days=1&bucket=hour&source=operator%3A%20home"} {
			v := viewGet(t, c, q)
			var n int64
			for _, cell := range v.Cells {
				n += cell.Calls
				if cell.Model != model {
					t.Errorf("%s %s: a cell of %s", who, q, cell.Model)
				}
			}
			if n != calls || len(v.Sources) != 1 || v.Sources[0].Calls != calls || len(v.Models) != 1 || v.Models[0].Model != model {
				t.Errorf("%s %s: %d calls, sources %+v, models %+v; want %d calls of %s only", who, q, n, v.Sources, v.Models, calls, model)
			}
		}
	}
	check("first", admin, 2, "claude-opus-5-5")
	check("second", other, 1, "claude-haiku-5-5")
}

// F4: /cost/cells (without its external field), /cost/models and /cost/org
// are byte-identical with and without outside rows, and reading the new
// endpoint changes nothing at all.
func TestExternalUsageByModelLeavesTheAgentsFiguresAlone(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	h := outsideFixture(t, s, admin, false)

	paths := []string{"/api/v1/cost/cells?days=1&bucket=hour", "/api/v1/cost/cells?days=1&bucket=10min",
		"/api/v1/cost/models?days=1", "/api/v1/cost/org?days=1&bucket=hour", "/api/v1/cost/org?days=1&bucket=10hour"}
	snapshot := func(withExternal bool) map[string]string {
		out := map[string]string{}
		for _, p := range paths {
			body := rawGet(t, admin, p)
			if !withExternal && strings.HasPrefix(p, "/api/v1/cost/cells") {
				var m map[string]json.RawMessage
				if err := json.Unmarshal(body, &m); err != nil {
					t.Fatal(err)
				}
				delete(m, "external")
				body, _ = json.Marshal(m)
			}
			out[p] = string(body)
		}
		return out
	}
	before := snapshot(false)
	uploadOutside(t, admin, h, true)
	after := snapshot(false)
	for _, p := range paths {
		if before[p] != after[p] {
			t.Errorf("%s changed with outside rows present:\nbefore %s\nafter  %s", p, before[p], after[p])
		}
	}
	whole := snapshot(true)
	for _, b := range []string{"10min", "hour", "10hour", "day", "week"} {
		viewGet(t, admin, "days=1&bucket="+b)
	}
	if again := snapshot(true); fmt.Sprint(again) != fmt.Sprint(whole) {
		t.Error("reading /cost/external-usage changed another endpoint's answer")
	}
}
