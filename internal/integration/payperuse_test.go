package integration

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
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
// and may cover the same time as a charge; a charge entered by hand where the
// limit holds is superseded by the meter.
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

	// A reading entered by hand (source manual) for the credential at its
	// place; none for a place without a credential.
	month := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := s.obs.AddManualReading(ctx, s.orgID, rt.ID, ord+7, "overage", 81, &month, since.Add(90*time.Minute)); !errors.Is(err, observability.ErrNoCredential) {
		t.Errorf("manual reading at an empty place: %v", err)
	}
	if err := s.obs.AddManualReading(ctx, s.orgID, rt.ID, ord, "overage", 81, &month, since.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The same moment a second time is not stored, and says so.
	if err := s.obs.AddManualReading(ctx, s.orgID, rt.ID, ord, "overage", 82, &month, since.Add(90*time.Minute)); !errors.Is(err, observability.ErrReadingExists) {
		t.Errorf("a second manual reading at the same moment: %v, want ErrReadingExists", err)
	}
	var manual int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_readings WHERE org_id = $1 AND source = 'manual'
		AND window_kind = 'overage' AND week_percent = 81 AND secret_key = 'claude_code_oauth_token' AND credential_kind = 'subscription'`,
		s.orgID).Scan(&manual); err != nil || manual != 1 {
		t.Errorf("manual readings = %d (%v), want 1", manual, err)
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
	// 78 → 80 spans ten days and an hour, of which one hour lies in the range
	// (pro rata, like a charge); 80 → 83 lies wholly in it. 4 EUR a point.
	share := 1.0 / (10*24 + 1)
	want := 2*share*4 + 3*4.0
	if meter.Currency != "EUR" || meter.WindowKind != "overage" || meter.LimitAmount != 400 ||
		meter.Points == nil || !near(*meter.Points, 2*share+3) || meter.CountedPoints == nil || !near(*meter.CountedPoints, 2*share+3) {
		t.Errorf("meter charge: %+v", *meter)
	}
	if !near(meter.CountedAmount, want) || !near(meter.WindowAmount, want) || meter.CountedUSD == nil || !near(*meter.CountedUSD, want*1.08) {
		t.Errorf("meter money: counted %v window %v; want %v EUR at 1.08", meter.CountedAmount, meter.WindowAmount, want)
	}
	if meter.CountedLow == nil || meter.CountedHigh == nil || !near(*meter.CountedLow, want-4) || !near(*meter.CountedHigh, want+4) {
		t.Errorf("meter range: %v..%v, want ±4 EUR (one month segment, ±1 point)", meter.CountedLow, meter.CountedHigh)
	}
	if !meter.From.Equal(since) || !meter.To.Equal(since.Add(2*time.Hour)) {
		t.Errorf("meter span %v..%v, want the range's start to the last reading", meter.From, meter.To)
	}
	if export.CountedAmount != 0 || export.CountedUSD != nil || !near(export.WindowAmount, 21) || export.SupersededAmount != 0 {
		t.Errorf("export counted: %+v", *export)
	}
	// The hand-entered charge (three hours) lies where the limit holds, but
	// the meter's money covers only the first two (since → the last
	// reading): that part is superseded, the third hour stays counted.
	if !near(charge.CountedAmount, 10.0/3) || !near(charge.SupersededAmount, 20.0/3) {
		t.Errorf("charge under the limit: counted %v, superseded %v; want 10/3 and 20/3", charge.CountedAmount, charge.SupersededAmount)
	}
}

// Manual readings before and after a run that wrote its own in-band readings
// are one meter with them (outside review of af6ebb13, major 4): 30 → 50 at
// 400 EUR is 80 EUR, not the in-band source's 40 → 42 alone. A hand-entered
// charge is superseded only over the span the meter priced (major 1), and a
// limit without any readings supersedes nothing. POST /cost/usage-readings
// answers 409 for a second reading at the same moment (minor 5) and refuses
// a reset time that is not after the reading or too far ahead (minor 6).
func TestUsageLimitMergesManualAroundRun(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Arbeitssitz", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "work-seat", "Work")
	ord2 := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "other-seat", "Other")
	seat := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	since := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	long := since.Add(-60 * 24 * time.Hour)
	ref := since.Add(6 * time.Hour)
	month := time.Date(ref.Year(), ref.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	for _, o := range []int{ord, ord2} {
		if _, err := s.runtimes.SetUsageLimit(ctx, s.orgID, rt.ID, o, runtimes.UsageLimit{
			Amount: 400, Currency: "EUR", ValidFrom: long}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.runtimes.SetCostBasis(ctx, s.orgID, rt.ID, o, runtimes.BasisUsage, long); err != nil {
			t.Fatal(err)
		}
	}

	post := func(o int, window string, pct float64, resets, at time.Time, want int) {
		t.Helper()
		admin.expect(http.MethodPost, "/api/v1/cost/usage-readings", map[string]any{
			"runtime_id": rt.ID, "ord": o, "window_kind": window, "percent": pct,
			"resets_at": resets.Format(time.RFC3339), "fetched_at": at.Format(time.RFC3339)}, want)
	}
	post(ord, "overage", 30, month, since.Add(time.Hour), http.StatusCreated)
	for i, pct := range []float64{40, 41, 42} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: seat,
			Source: observability.SourceInband, WindowKind: "overage", Supported: true, WindowPercent: -1, WeekPercent: pct,
			ResetsAt: month, FetchedAt: since.Add(time.Duration(i+2) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	post(ord, "overage", 50, month, since.Add(5*time.Hour), http.StatusCreated)
	post(ord, "overage", 51, month, since.Add(5*time.Hour), http.StatusConflict)
	post(ord, "overage", 50, since, since.Add(5*time.Hour+time.Minute), http.StatusBadRequest)
	post(ord, "overage", 50, since.Add(40*24*time.Hour), since.Add(5*time.Hour+time.Minute), http.StatusBadRequest)
	post(ord, "seven_day", 50, since.Add(9*24*time.Hour), since.Add(5*time.Hour+time.Minute), http.StatusBadRequest)
	post(ord, "seven_day", 50, since.Add(7*24*time.Hour), since.Add(5*time.Hour+time.Minute), http.StatusCreated)

	// Hand-entered charges: over [since, since+5h) on the read seat, where
	// the meter priced since+1h → since+5h; and on the seat never read.
	for _, o := range []int{ord, ord2} {
		if _, err := s.runtimes.AddUsageCharge(ctx, s.orgID, rt.ID, o, runtimes.UsageCharge{
			Amount: 50, Currency: "EUR", From: since, To: since.Add(5 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}

	cells, err := s.obs.CostCellsFor(ctx, s.orgID, "hour", since)
	if err != nil {
		t.Fatal(err)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	var meters int
	for _, c := range cells.UsageCharges {
		switch {
		case c.Kind == observability.ChargeKindMeter:
			meters++
			if c.SecretSlot != 0 || !near(c.CountedAmount, 80) || c.Points == nil || !near(*c.Points, 20) ||
				!c.From.Equal(since.Add(time.Hour)) || !c.To.Equal(since.Add(5*time.Hour)) {
				t.Errorf("meter charge: %+v, want 80 EUR (30 → 50) over since+1h..since+5h", c)
			}
		case c.SecretSlot == 0:
			if !near(c.SupersededAmount, 40) || !near(c.CountedAmount, 10) {
				t.Errorf("charge on the read seat: counted %v, superseded %v; want 10 and 40", c.CountedAmount, c.SupersededAmount)
			}
		default:
			if !near(c.CountedAmount, 50) || c.SupersededAmount != 0 {
				t.Errorf("charge on the unread seat: counted %v, superseded %v; want 50 and 0", c.CountedAmount, c.SupersededAmount)
			}
		}
	}
	if meters != 1 {
		t.Errorf("meter charges = %d, want 1 (the unread seat has none): %+v", meters, cells.UsageCharges)
	}
}
