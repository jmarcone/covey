package integration

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
	"covey/internal/runtimes"
)

// workOverage is the rate_limit_event the work machine's Enterprise seat
// (billed per token against a monthly limit) printed on 2026-11-18: no
// unifiedWindows, one window named by rateLimitType "overage", resetting on
// the first of the month.
const workOverage = `{"type":"rate_limit_event","session_id":"w1","rate_limit_info":{"status":"allowed_warning","resetsAt":1796083200,"rateLimitType":"overage","utilization":0.78,"overageStatus":"allowed_warning","overageResetsAt":1796083200,"isUsingOverage":false,"overageInUse":true,"surpassedThreshold":0.75}}`

// A monthly "overage" window is a meter of its own: filed under its own
// window, never priced as the weekly seat meter (even where the credential
// has a weekly plan), and its baseline found though it lies more than the
// weekly lookback (eight days) before the range.
func TestOverageIsMonthlyMeter(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("ueberzug")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "claude_code_oauth_token", Kind: "subscription"}

	if err := s.obs.AddInbandReadings(ctx, agent.OrgID, seat, json.RawMessage(workOverage)); err != nil {
		t.Fatal(err)
	}
	var (
		window, status string
		week, five     *float64
		resets         *time.Time
	)
	if err := s.pool.QueryRow(ctx, `SELECT window_kind, status, week_percent, window_percent, resets_at
		FROM usage_readings WHERE org_id = $1 AND runtime_id = $2`, agent.OrgID, seat.RuntimeID).
		Scan(&window, &status, &week, &five, &resets); err != nil {
		t.Fatal(err)
	}
	if window != "overage" || status != "allowed_warning" || week == nil || *week != 78 || five != nil {
		t.Errorf("reading: window %q status %q week %v five %v, want overage at 78", window, status, week, five)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); resets == nil || !resets.Equal(want) {
		t.Errorf("resets_at = %v, want %v", resets, want)
	}

	// A weekly plan on the same credential prices nothing of the monthly
	// window. The first reading moves to ten days before the range.
	since := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	if _, err := s.pool.Exec(ctx, `INSERT INTO credential_plans (org_id, secret_key, secret_slot, plan_name, amount, period, valid_from)
		VALUES ($1, 'claude_code_oauth_token', 0, 'Max', 200, 'month', $2)`, agent.OrgID, since.Add(-60*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE usage_readings SET fetched_at = $3 WHERE org_id = $1 AND runtime_id = $2`,
		agent.OrgID, seat.RuntimeID, since.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i, pct := range []float64{80, 83} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: seat,
			Source: observability.SourceInband, WindowKind: "overage", Supported: true, WindowPercent: -1, WeekPercent: pct,
			ResetsAt: *resets, FetchedAt: since.Add(time.Duration(i+1) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "hour", since)
	if err != nil {
		t.Fatal(err)
	}
	var cov *observability.MeterCoverage
	for i := range cells.Meter {
		if cells.Meter[i].WindowKind == "overage" {
			cov = &cells.Meter[i]
		}
	}
	if cov == nil {
		t.Fatalf("no coverage of the overage window: %+v", cells.Meter)
	}
	if cov.USD != nil || cov.UnpricedPoints != 0 {
		t.Errorf("overage coverage priced as the weekly seat: usd %v, unpriced %v", cov.USD, cov.UnpricedPoints)
	}
	if !cov.BaselineBefore || cov.BaselinePercent != 78 || cov.Points != 5 {
		t.Errorf("baseline: before %v at %v%%, points %v; want the reading ten days back (78 %%), 5 points",
			cov.BaselineBefore, cov.BaselinePercent, cov.Points)
	}
	for _, st := range cells.Subscription {
		if st.WindowKind == "overage" && (st.USD != nil || st.UnpricedPoints != 0) {
			t.Errorf("overage step priced as weekly: %+v", st)
		}
	}
	if n := len(cells.External.Meter.Credentials); n != 0 {
		t.Errorf("external meter seats = %d, want none (the weekly split must not read a monthly meter)", n)
	}
}

// A monthly usage limit (0152) prices the monthly meter in its own currency:
// real spend = Δutilisation × limit, counted under the usage basis, with a
// rate only where one was given. An export is listed beside it, never counted,
// and may cover the same time as a charge.
func TestUsageLimitPricesMonthlyMeter(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Arbeitssitz", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "work-seat", "Work")
	seat := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	since := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	long := since.Add(-60 * 24 * time.Hour)

	if _, err := s.runtimes.SetUsageLimit(ctx, s.orgID, rt.ID, ord, runtimes.UsageLimit{
		Amount: 400, Currency: "EUR", WindowKind: "seven_day"}); !errors.As(err, new(runtimes.InvalidCost)) {
		t.Errorf("a limit on the weekly window: %v, want refused", err)
	}
	rate, day := 1.08, time.Now().UTC().Format("2006-01-02")
	lim, err := s.runtimes.SetUsageLimit(ctx, s.orgID, rt.ID, ord, runtimes.UsageLimit{
		Amount: 400, Currency: "eur", FXToUSD: &rate, FXDate: &day, ValidFrom: long})
	if err != nil {
		t.Fatal(err)
	}
	if lim.WindowKind != "overage" || lim.Currency != "EUR" || lim.ValidTo != nil {
		t.Errorf("limit stored: %+v", lim)
	}
	if _, err := s.runtimes.SetCostBasis(ctx, s.orgID, rt.ID, ord, runtimes.BasisUsage, long); err != nil {
		t.Fatal(err)
	}
	for i, r := range []struct {
		at  time.Time
		pct float64
	}{{since.Add(-10 * 24 * time.Hour), 78}, {since.Add(time.Hour), 80}, {since.Add(2 * time.Hour), 83}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: seat,
			Source: observability.SourceInband, WindowKind: "overage", Supported: true, WindowPercent: -1, WeekPercent: r.pct,
			ResetsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), FetchedAt: r.at}); err != nil {
			t.Fatalf("reading %d: %v", i, err)
		}
	}

	// Usage exports: one beside a charge over the same span; two exports
	// over the same time are one check entered twice.
	if _, err := s.runtimes.AddUsageCharge(ctx, s.orgID, rt.ID, ord, runtimes.UsageCharge{
		Amount: 21, Currency: "EUR", From: since, To: since.Add(3 * time.Hour), Kind: "export"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runtimes.AddUsageCharge(ctx, s.orgID, rt.ID, ord, runtimes.UsageCharge{
		Amount: 10, Currency: "USD", From: since, To: since.Add(3 * time.Hour)}); err != nil {
		t.Errorf("a charge beside an export: %v", err)
	}
	if _, err := s.runtimes.AddUsageCharge(ctx, s.orgID, rt.ID, ord, runtimes.UsageCharge{
		Amount: 5, Currency: "EUR", From: since.Add(time.Hour), To: since.Add(4 * time.Hour), Kind: "export"}); !errors.Is(err, runtimes.ErrChargeOverlaps) {
		t.Errorf("an export over another: %v, want ErrChargeOverlaps", err)
	}
	if _, err := s.runtimes.AddUsageCharge(ctx, s.orgID, rt.ID, ord, runtimes.UsageCharge{
		Amount: 5, Currency: "EUR", From: since, To: since.Add(time.Hour), Kind: "invoice"}); !errors.As(err, new(runtimes.InvalidCost)) {
		t.Errorf("an unknown kind: %v, want refused", err)
	}

	cells, err := s.obs.CostCellsFor(ctx, s.orgID, "hour", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells.UsageLimits) != 1 {
		t.Errorf("usage limits in the cells = %d, want 1", len(cells.UsageLimits))
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	var meter, export, charge *observability.WindowCharge
	for i := range cells.UsageCharges {
		c := &cells.UsageCharges[i]
		switch c.Kind {
		case observability.ChargeKindMeter:
			meter = c
		case observability.ChargeKindExport:
			export = c
		case "":
			charge = c
		}
	}
	if meter == nil || export == nil || charge == nil {
		t.Fatalf("charges: %+v", cells.UsageCharges)
	}
	if meter.Currency != "EUR" || meter.WindowKind != "overage" || meter.LimitAmount != 400 ||
		meter.Points == nil || *meter.Points != 5 || meter.CountedPoints == nil || *meter.CountedPoints != 5 {
		t.Errorf("meter charge: %+v", *meter)
	}
	if !near(meter.CountedAmount, 20) || !near(meter.WindowAmount, 20) || meter.CountedUSD == nil || !near(*meter.CountedUSD, 21.6) {
		t.Errorf("meter money: counted %v window %v usd %v; want 20 EUR = 21.60 USD", meter.CountedAmount, meter.WindowAmount, meter.CountedUSD)
	}
	if meter.CountedLow == nil || meter.CountedHigh == nil || !near(*meter.CountedLow, 16) || !near(*meter.CountedHigh, 24) {
		t.Errorf("meter range: %v..%v, want 16..24 (one month segment, ±1 point)", meter.CountedLow, meter.CountedHigh)
	}
	if !meter.From.Equal(since.Add(-10*24*time.Hour)) || !meter.To.Equal(since.Add(2*time.Hour)) {
		t.Errorf("meter span %v..%v", meter.From, meter.To)
	}
	if export.CountedAmount != 0 || export.CountedUSD != nil || !near(export.WindowAmount, 21) {
		t.Errorf("export counted: %+v", *export)
	}
	if !near(charge.CountedAmount, 10) {
		t.Errorf("charge beside the export: counted %v, want 10", charge.CountedAmount)
	}
}
