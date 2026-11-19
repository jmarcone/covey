package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// How a credential's real money is formed (0151, #120): the basis per
// credential (seat | usage | seat_and_usage), dated like the plan, and usage
// charges entered by hand, each in its own currency.

// costCells reads /cost/cells decoded.
func costCells(t *testing.T, c *apiClient, query string) observability.CostCells {
	t.Helper()
	var cells observability.CostCells
	if err := json.Unmarshal(rawGet(t, c, "/api/v1/cost/cells?"+query), &cells); err != nil {
		t.Fatal(err)
	}
	return cells
}

// withoutKeys drops top-level keys of a JSON object and re-encodes it.
func withoutKeys(t *testing.T, body []byte, keys ...string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		delete(m, k)
	}
	out, _ := json.Marshal(m)
	return string(out)
}

// weeklyStep is the seat's step of the weekly meter that moved.
func weeklyStep(t *testing.T, cells observability.CostCells) observability.MeterStep {
	t.Helper()
	for _, st := range cells.Subscription {
		if st.WindowKind == observability.WindowSevenDay && st.Points > 0 {
			return st
		}
	}
	t.Fatalf("no moved weekly step in %+v", cells.Subscription)
	return observability.MeterStep{}
}

func TestCredentialCostBasisAndUsageCharges(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("arbeitskonto")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	place := fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d", rt.ID, ord)
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	h := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	admin.expect(http.MethodPut, place+"/plan", map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD",
		"period": "month", "valid_from": h.Add(-72 * time.Hour)}, http.StatusOK)
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	f.line(t, cred, h.Add(10*time.Minute), 50_000)
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{h.Add(-time.Hour), 10}, {h.Add(30 * time.Minute), 14}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	perPoint := 200.0 * 12 / 52 / 100

	// The unchanged default: the agents' figures in /cost/cells, /cost/models
	// and /cost/org, byte for byte, with no basis and no charge — and the
	// cells carry neither field.
	paths := []string{"/api/v1/cost/cells?days=2&bucket=hour", "/api/v1/cost/cells?days=2&bucket=day",
		"/api/v1/cost/models?days=2", "/api/v1/cost/org?days=2&bucket=hour", "/api/v1/cost/org?days=2"}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, p := range paths {
			body := rawGet(t, admin, p)
			if strings.HasPrefix(p, "/api/v1/cost/cells") {
				out[p] = withoutKeys(t, body, "cost_bases", "usage_charges")
				out[p+"#raw"] = string(body)
			} else {
				out[p] = string(body)
			}
		}
		return out
	}
	before := snapshot()
	for _, p := range paths[:2] {
		if raw := before[p+"#raw"]; strings.Contains(raw, `"cost_bases"`) || strings.Contains(raw, `"usage_charges"`) {
			t.Errorf("%s carries the new fields with nothing set: %s", p, raw)
		}
	}
	if st := weeklyStep(t, costCells(t, admin, "days=2&bucket=hour")); st.USD == nil || math.Abs(*st.USD-4*perPoint) > 1e-9 {
		t.Fatalf("the default seat: step %+v, want 4 points at the plan", st)
	}
	same := func(when string) {
		t.Helper()
		after := snapshot()
		for _, p := range paths {
			if before[p] != after[p] {
				t.Errorf("%s: %s changed:\nbefore %s\nafter  %s", when, p, before[p], after[p])
			}
		}
	}

	// The basis seat is today's behaviour: nothing but the new field moves.
	b := admin.expect(http.MethodPut, place+"/cost-basis", map[string]any{"basis": "seat", "valid_from": h.Add(-60 * time.Hour)}, http.StatusOK)
	if b["basis"] != "seat" || b["secret_key"] != "claude_code_oauth_token" || b["valid_to"] != nil {
		t.Errorf("the stored basis: %v", b)
	}
	same("basis seat")
	if cb := costCells(t, admin, "days=2&bucket=hour").CostBases; len(cb) != 1 || cb[0].Basis != "seat" {
		t.Errorf("cost_bases = %+v, want the seat", cb)
	}

	// A EUR charge for 24 hours (the measured work account): stored in EUR;
	// under the seat basis it is listed, counted nowhere, and moves nothing.
	from, to := h.Add(-21*time.Hour), h.Add(3*time.Hour)
	ch := admin.expect(http.MethodPost, place+"/usage-charges", map[string]any{"amount": 180, "currency": "eur",
		"from": from, "to": to, "note": "measured, 24 h"}, http.StatusCreated)
	if ch["currency"] != "EUR" || ch["amount"] != 180.0 || ch["fx_to_usd"] != nil || ch["fx_date"] != nil || ch["note"] != "measured, 24 h" {
		t.Errorf("the stored charge: %v", ch)
	}
	same("a charge under the seat basis")
	uc := costCells(t, admin, "days=2&bucket=hour").UsageCharges
	if len(uc) != 1 || uc[0].WindowAmount != 180 || uc[0].CountedAmount != 0 || uc[0].CountedUSD != nil || uc[0].Currency != "EUR" {
		t.Errorf("under seat: usage_charges = %+v, want 180 EUR listed, 0 counted", uc)
	}

	// Validated like the plans.
	for _, c := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"currency", map[string]any{"amount": 1, "currency": "EURO", "from": from, "to": to}, "three-letter"},
		{"negative", map[string]any{"amount": -1, "currency": "EUR", "from": from, "to": to}, "non-negative"},
		{"no amount", map[string]any{"currency": "EUR", "from": from, "to": to}, "amount is required"},
		{"to before from", map[string]any{"amount": 1, "currency": "EUR", "from": to, "to": from}, "before to"},
		{"no span", map[string]any{"amount": 1, "currency": "EUR", "from": from}, "from and to are required"},
		{"rate without date", map[string]any{"amount": 1, "currency": "EUR", "from": from, "to": to, "fx_to_usd": 1.08}, "a rate needs its date"},
		{"USD at another rate", map[string]any{"amount": 1, "currency": "USD", "from": from, "to": to, "fx_to_usd": 0.9, "fx_date": "2026-11-18"}, "converts at 1"},
	} {
		resp := admin.do(http.MethodPost, place+"/usage-charges", c.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), c.want) {
			t.Errorf("%s: HTTP %d %s, want 400 with %q", c.name, resp.StatusCode, body, c.want)
		}
	}
	admin.expect(http.MethodPut, place+"/cost-basis", map[string]any{"basis": "both"}, http.StatusBadRequest)
	admin.expect(http.MethodPut, place+"/cost-basis", map[string]any{"basis": "usage", "valid_from": h.Add(-60 * time.Hour)}, http.StatusConflict)
	missing := fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d", rt.ID, ord+5)
	admin.expect(http.MethodPut, missing+"/cost-basis", map[string]any{"basis": "usage"}, http.StatusNotFound)
	admin.expect(http.MethodPost, missing+"/usage-charges", map[string]any{"amount": 1, "currency": "EUR", "from": from, "to": to}, http.StatusNotFound)
	s.mitglied(t, "auditor@test.local", "Auditor", identity.RoleAuditor, "auditor-passwort")
	auditor := login(t, s, "auditor@test.local", "auditor-passwort")
	auditor.expect(http.MethodPut, place+"/cost-basis", map[string]any{"basis": "usage"}, http.StatusForbidden)
	auditor.expect(http.MethodPost, place+"/usage-charges", map[string]any{"amount": 1, "currency": "EUR", "from": from, "to": to}, http.StatusForbidden)
	auditor.expect(http.MethodGet, place+"/usage-charges", nil, http.StatusForbidden)

	// From h−30h the seat is billed by usage: the charge counts in EUR, with
	// no dollar figure; the plan prices none of the meter's points any more.
	admin.expect(http.MethodPut, place+"/cost-basis", map[string]any{"basis": "usage", "valid_from": h.Add(-30 * time.Hour)}, http.StatusOK)
	cells := costCells(t, admin, "days=2&bucket=hour")
	if st := weeklyStep(t, cells); st.USD != nil || st.UnpricedPoints != 4 {
		t.Errorf("under usage: step %+v, want no dollars and 4 unpriced points", st)
	}
	if len(cells.Plans) != 1 || cells.Plans[0].ValidTo != nil {
		t.Errorf("the plans as stored changed: %+v", cells.Plans)
	}
	if uc := cells.UsageCharges; len(uc) != 1 || uc[0].CountedAmount != 180 || uc[0].CountedUSD != nil {
		t.Errorf("under usage: usage_charges = %+v, want 180 EUR counted, no USD", uc)
	}
	if cb := cells.CostBases; len(cb) != 2 || cb[0].Basis != "seat" || cb[0].ValidTo == nil ||
		!cb[0].ValidTo.Equal(h.Add(-30*time.Hour)) || cb[1].Basis != "usage" || cb[1].ValidTo != nil {
		t.Errorf("cost_bases = %+v, want seat closed at h-30h, then usage", cb)
	}

	// A charge with a rate and its date has a dollar figure at that rate.
	rated := admin.expect(http.MethodPost, place+"/usage-charges", map[string]any{"amount": 100, "currency": "EUR",
		"from": h.Add(-10 * time.Hour), "to": h.Add(-9 * time.Hour), "fx_to_usd": 1.08, "fx_date": "2026-11-18"}, http.StatusCreated)
	if rated["fx_to_usd"] != 1.08 || rated["fx_date"] != "2026-11-18" {
		t.Errorf("the rated charge: %v", rated)
	}
	for _, c := range costCells(t, admin, "days=2&bucket=day").UsageCharges {
		if c.Amount == 100 && (c.CountedUSD == nil || math.Abs(*c.CountedUSD-108) > 1e-9 || c.CountedAmount != 100) {
			t.Errorf("the rated charge in the cells: %+v, want 100 EUR and $108", c)
		}
	}

	// From h−1h seat and usage: the plan prices the meter again, and the
	// charges still count.
	admin.expect(http.MethodPut, place+"/cost-basis", map[string]any{"basis": "seat_and_usage", "valid_from": h.Add(-time.Hour)}, http.StatusOK)
	cells = costCells(t, admin, "days=2&bucket=hour")
	if st := weeklyStep(t, cells); st.USD == nil || math.Abs(*st.USD-4*perPoint) > 1e-9 {
		t.Errorf("under seat_and_usage: step %+v, want 4 points at the plan", st)
	}
	for _, c := range cells.UsageCharges {
		if c.CountedAmount != c.Amount {
			t.Errorf("under seat_and_usage: %+v not counted in full", c)
		}
	}
	hist := admin.expectList(http.MethodGet, place+"/cost-basis", nil, http.StatusOK)
	if len(hist) != 3 || hist[0]["basis"] != "seat_and_usage" || hist[2]["basis"] != "seat" {
		t.Errorf("the history, newest first: %v", hist)
	}

	// Listed and deleted; another credential's id is not found.
	list := admin.expectList(http.MethodGet, place+"/usage-charges", nil, http.StatusOK)
	if len(list) != 2 {
		t.Fatalf("usage charges: %v", list)
	}
	admin.expect(http.MethodDelete, fmt.Sprintf("%s/usage-charges/%s", place, uuid.New()), nil, http.StatusNotFound)
	admin.expect(http.MethodDelete, fmt.Sprintf("%s/usage-charges/%s", place, rated["id"]), nil, http.StatusNoContent)
	admin.expect(http.MethodDelete, fmt.Sprintf("%s/usage-charges/%s", place, rated["id"]), nil, http.StatusNotFound)
	if list := admin.expectList(http.MethodGet, place+"/usage-charges", nil, http.StatusOK); len(list) != 1 || list[0]["id"] != ch["id"] {
		t.Errorf("after the delete: %v", list)
	}
}
