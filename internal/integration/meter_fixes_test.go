package integration

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// The meter's points are priced where they were read, and the server says so
// per step AND per credential: a plan that begins mid-window prices only the
// points read after it, the points before it are unpriced, and the coverage
// carries the sum of its steps' own figures — so no reader has to re-price the
// whole movement at one plan.
func TestMeterCoveragePricedPerReading(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("zaehler-preis")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	since := time.Now().Add(-5 * time.Hour).Truncate(time.Hour)
	change := since.Add(time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": change}, http.StatusOK)

	// 10 before the window; 12 while no plan was valid (2 unpriced points);
	// 15 and 16 after the plan began (4 priced points).
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{since.Add(-30 * time.Minute), 10}, {since.Add(30 * time.Minute), 12},
		{change.Add(30 * time.Minute), 15}, {change.Add(40 * time.Minute), 16}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	perPoint := 200.0 * 12 / 52 / 100
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "day", since)
	if err != nil {
		t.Fatal(err)
	}
	var stepUSD, stepUnpriced float64
	for _, st := range cells.Subscription {
		if st.USD != nil {
			stepUSD += *st.USD
		}
		stepUnpriced += st.UnpricedPoints
	}
	if len(cells.Meter) != 1 {
		t.Fatalf("coverage = %+v, want the one credential", cells.Meter)
	}
	c := cells.Meter[0]
	if c.Points != 6 || c.USD == nil || math.Abs(*c.USD-4*perPoint) > 1e-9 || c.UnpricedPoints != 2 {
		t.Errorf("coverage: %v points, usd %v, %v unpriced — want 6, %v, 2", c.Points, c.USD, c.UnpricedPoints, 4*perPoint)
	}
	if c.USD != nil && (math.Abs(*c.USD-stepUSD) > 1e-9 || c.UnpricedPoints != stepUnpriced) {
		t.Errorf("coverage %v / %v is not the sum of its steps %v / %v", *c.USD, c.UnpricedPoints, stepUSD, stepUnpriced)
	}

	// Over the API, under the names the steps already use.
	got := admin.expect(http.MethodGet, "/api/v1/cost/cells?days=1&bucket=hour", nil, http.StatusOK)
	meter, _ := got["meter"].([]any)
	if len(meter) != 1 {
		t.Fatalf("GET /cost/cells meter = %v", got["meter"])
	}
	m := meter[0].(map[string]any)
	if usd, ok := m["usd"].(float64); !ok || math.Abs(usd-4*perPoint) > 1e-9 || m["unpriced_points"] != float64(2) {
		t.Errorf("GET /cost/cells meter usd = %v, unpriced_points = %v", m["usd"], m["unpriced_points"])
	}
	for _, x := range got["subscription"].([]any) {
		st := x.(map[string]any)
		if _, ok := st["usd"]; !ok {
			t.Errorf("a step without usd: %v", st)
		}
		if _, ok := st["unpriced_points"]; !ok {
			t.Errorf("a step without unpriced_points: %v", st)
		}
	}

	// Without any plan the coverage is unpriced, not $0.
	bare := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "ohne_plan", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{since.Add(-30 * time.Minute), 10}, {since.Add(30 * time.Minute), 13}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: bare,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	cells, err = s.obs.CostCellsFor(ctx, agent.OrgID, "day", since)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cells.Meter {
		if m.SecretKey == "ohne_plan" && (m.USD != nil || m.UnpricedPoints != 3) {
			t.Errorf("coverage without a plan: %+v — want usd nil and 3 unpriced points", m)
		}
	}
}
