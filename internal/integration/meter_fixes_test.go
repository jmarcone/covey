package integration

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

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
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "day", since, time.Time{})
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
	cells, err = s.obs.CostCellsFor(ctx, agent.OrgID, "day", since, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cells.Meter {
		if m.SecretKey == "ohne_plan" && (m.USD != nil || m.UnpricedPoints != 3) {
			t.Errorf("coverage without a plan: %+v — want usd nil and 3 unpriced points", m)
		}
	}
}

// meterPointsOf files each case as its own secret and returns the points per
// secret over the window, summed over the steps, one source.
func meterPointsOf(t *testing.T, s *stack, since time.Time, cases map[string][]meterReading) map[string]float64 {
	t.Helper()
	ctx := context.Background()
	agent := s.newSupportAgent("zaehler-" + fmt.Sprint(len(cases)))
	for key, rs := range cases {
		rt := uuid.New()
		for _, r := range rs {
			cred := observability.CredentialRef{RuntimeID: rt, SecretKey: key, Kind: "subscription"}
			if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: cred,
				Supported: true, WindowPercent: -1, WeekPercent: r.pct, ResetsAt: r.resets,
				FetchedAt: since.Add(time.Duration(r.min) * time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "hour", since, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, st := range cells.Subscription {
		got[st.SecretKey] += st.Points
	}
	return got
}

// A reading of a week that is already over, arriving late (its reset time an
// hour or more before the one an earlier reading gave), is dropped. Kept, it
// rejoined the old week's level inside the new one and added the difference
// as points, and the next reading of the new week counted it from 0 again.
func TestMeterDropsLateReadingOfAnOldWeek(t *testing.T) {
	s := newStack(t)
	since := time.Now().Add(-5 * time.Hour).Truncate(time.Hour)
	week1 := since.Add(48 * time.Hour)
	week2 := week1.Add(7 * 24 * time.Hour)
	got := meterPointsOf(t, s, since, map[string][]meterReading{
		// 90 in week 1; week 2 begins at 2; a late 92 and 93 of week 1; 4.
		"late": {{min: -30, pct: 90, resets: week1}, {min: 10, pct: 2, resets: week2},
			{min: 20, pct: 92, resets: week1}, {min: 25, pct: 93, resets: week1}, {min: 30, pct: 4, resets: week2}},
		// Less than an hour apart is the same reset, read twice: kept.
		"jitter": {{min: -30, pct: 10, resets: week1}, {min: 10, pct: 12, resets: week1.Add(-30 * time.Minute)}},
	})
	if got["late"] != 4 {
		t.Errorf("late reading of the old week: %v points, want 4 (2 + 2 in the new week)", got["late"])
	}
	if got["jitter"] != 2 {
		t.Errorf("reset times 30 min apart: %v points, want 2", got["jitter"])
	}
}
