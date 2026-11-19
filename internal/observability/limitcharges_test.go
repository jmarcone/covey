package observability

import (
	"math"
	"testing"
	"time"
)

// limitRows builds one partition's readings the way meterQuery returns them:
// pct per reading, the month (week column) changing where reset is set, the
// step the rise of the month's running maximum. Each reading names the reset
// at the first of the month after t0, a month later from each reset on.
func limitRows(t0 time.Time, pcts []float64, resets map[int]bool, gap time.Duration) []meterRow {
	var rs []meterRow
	week, top := int64(0), 0.0
	monthEnd := time.Date(t0.Year(), t0.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	for i, p := range pcts {
		if i > 0 && resets[i] {
			monthEnd = monthEnd.AddDate(0, 1, 0)
		}
		end := monthEnd
		r := meterRow{key: "claude_code_oauth_token", slot: 0, kind: "subscription", source: SourceInband,
			window: "overage", at: t0.Add(time.Duration(i) * gap), pct: p, resets: &end}
		switch {
		case i == 0:
			top = p
		case resets[i]:
			week++
			top, r.points, r.reset = p, p, true
		default:
			r.points = max(0, p-top)
			top = max(top, p)
		}
		r.top, r.week = top, week
		rs = append(rs, r)
	}
	return rs
}

func usageAll(from time.Time) []CredentialBasis {
	return []CredentialBasis{{SecretKey: "claude_code_oauth_token", Basis: BasisUsage, ValidFrom: from}}
}

func limitOf(amount float64, from time.Time, to *time.Time) UsageLimit {
	return UsageLimit{SecretKey: "claude_code_oauth_token", WindowKind: "overage", Amount: amount, Currency: "EUR",
		ValidFrom: from, ValidTo: to}
}

func counted(cs []WindowCharge) float64 {
	sum := 0.0
	for _, c := range cs {
		sum += c.CountedAmount
	}
	return sum
}

// Utilisation is a share of the limit in force: across a limit change the
// money is u2·L2 − u1·L1 (outside review of 9e689a19, major 1).
func TestMeterLimitChargesAcrossLimitChange(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	since := t0.Add(-time.Hour)
	change := t0.Add(30 * time.Minute)

	// Raised 400 → 800 EUR: 50 % of 400 = 200, then 25 % of 800 = 200 (no
	// new spend), 50 % = 400 (+200), 60 % = 480 (+80).
	rs := limitRows(t0, []float64{0, 50, 25, 50, 60}, nil, 20*time.Minute)
	limits := []UsageLimit{limitOf(400, t0.Add(-30*24*time.Hour), &change), limitOf(800, change, nil)}
	if got := counted(meterLimitCharges(rs, since, limits, usageAll(since))); math.Abs(got-480) > 1e-9 {
		t.Errorf("raised limit: %v EUR, want 480 (200 under 400, then 200 + 80 under 800)", got)
	}

	// Lowered 400 → 200 EUR: 50 % of 400 = 200 = 100 % of 200, nothing spent after.
	rs = limitRows(t0, []float64{0, 50, 100, 100}, nil, 20*time.Minute)
	limits = []UsageLimit{limitOf(400, t0.Add(-30*24*time.Hour), &change), limitOf(200, change, nil)}
	if got := counted(meterLimitCharges(rs, since, limits, usageAll(since))); math.Abs(got-200) > 1e-9 {
		t.Errorf("lowered limit: %v EUR, want 200", got)
	}

	// A limit entered in mid-month: the month's 40 % before it are not its
	// money; the steps read under it are (40 → 50 → 53 at 400 EUR).
	rs = limitRows(t0, []float64{40, 50, 53}, nil, time.Hour)
	limits = []UsageLimit{limitOf(400, t0.Add(30*time.Minute), nil)}
	if got := counted(meterLimitCharges(rs, since, limits, usageAll(since))); math.Abs(got-52) > 1e-9 {
		t.Errorf("limit from mid-month: %v EUR, want 52 (13 points at 4 EUR, not the 40 before)", got)
	}

	// A new month starts from 0: 90 % then a reset to 5 % counts the 5.
	rs = limitRows(t0, []float64{88, 90, 5}, map[int]bool{2: true}, time.Hour)
	limits = []UsageLimit{limitOf(400, t0.Add(-30*24*time.Hour), nil)}
	if got := counted(meterLimitCharges(rs, since, limits, usageAll(since))); math.Abs(got-28) > 1e-9 {
		t.Errorf("month reset: %v EUR, want 28 (2 points + 5 points at 4 EUR)", got)
	}
}

// A step whose span began before the range counts pro rata by time, like a
// charge entered by hand (outside review, minor 2); ranges side by side still
// add up to the whole.
func TestMeterLimitChargesProRata(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rs := limitRows(t0, []float64{0, 60}, nil, 15*24*time.Hour) // 60 points over 15 days
	limits := []UsageLimit{limitOf(400, t0, nil)}
	bases := usageAll(t0)
	split := t0.Add(14 * 24 * time.Hour)
	in := meterLimitCharges(rs, split, limits, bases)
	if got := counted(in); math.Abs(got-16) > 1e-9 {
		t.Errorf("last day of a 15-day step: %v EUR, want 16 (240 / 15)", got)
	}
	if len(in) != 1 || !in[0].From.Equal(split) {
		t.Errorf("span starts at %v, want the range's start", in)
	}
	if whole := counted(meterLimitCharges(rs, t0, limits, bases)); math.Abs(whole-240) > 1e-9 {
		t.Errorf("whole step: %v EUR, want 240", whole)
	}
}

// Over the spans the meter priced, a charge entered by hand is not counted
// again: the meter is the money (outside review, major 2).
func TestChargeSupersededByLimit(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	c := WindowCharge{SecretKey: "claude_code_oauth_token", Amount: 100, Currency: "EUR", From: t0, To: t0.Add(10 * time.Hour)}
	half := t0.Add(5 * time.Hour)
	meter := []WindowCharge{{Kind: ChargeKindMeter, SecretKey: "claude_code_oauth_token", From: half, To: t0.Add(20 * time.Hour)}}
	got := chargeInWindow(c, t0, t0.Add(24*time.Hour), usageAll(t0), meterSpans(meter, "claude_code_oauth_token", 0))
	if math.Abs(got.CountedAmount-50) > 1e-9 || math.Abs(got.SupersededAmount-50) > 1e-9 {
		t.Errorf("counted %v, superseded %v; want 50 and 50", got.CountedAmount, got.SupersededAmount)
	}
	if got := chargeInWindow(c, t0, t0.Add(24*time.Hour), usageAll(t0), meterSpans(meter, "claude_code_oauth_token", 1)); got.CountedAmount != 100 {
		t.Errorf("another credential's meter superseded %v", got.SupersededAmount)
	}
}

// A hand-entered charge is superseded only over the spans the meter's money
// covers, not wherever a limit row holds (outside review of af6ebb13, major
// 1): a limit without readings, or one whose window was never read, leaves
// the charge the money; readings on two days of the month supersede those
// two days only.
func TestChargeSupersededOnlyWhereMeterPriced(t *testing.T) {
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dec := nov.AddDate(0, 1, 0)
	now := dec
	c := WindowCharge{SecretKey: "claude_code_oauth_token", Amount: 300, Currency: "EUR", From: nov, To: dec}
	limits := []UsageLimit{limitOf(400, nov.AddDate(0, -1, 0), nil)}

	// No readings at all: no meter charge, nothing superseded.
	meter := meterLimitCharges(nil, nov, limits, usageAll(nov))
	if got := chargeInWindow(c, nov, now, usageAll(nov), meterSpans(meter, c.SecretKey, 0)); got.CountedAmount != 300 || got.SupersededAmount != 0 {
		t.Errorf("no readings: counted %v, superseded %v; want 300 and 0", got.CountedAmount, got.SupersededAmount)
	}

	// Readings only on Nov 20 and 21: one day of the month is the meter's.
	day20 := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	rs := limitRows(day20, []float64{10, 15}, nil, 24*time.Hour)
	meter = meterLimitCharges(rs, nov, limits, usageAll(nov))
	if len(meter) != 1 || !meter[0].From.Equal(day20) || !meter[0].To.Equal(day20.Add(24*time.Hour)) {
		t.Fatalf("meter charges: %+v", meter)
	}
	got := chargeInWindow(c, nov, now, usageAll(nov), meterSpans(meter, c.SecretKey, 0))
	if math.Abs(got.SupersededAmount-10) > 1e-9 || math.Abs(got.CountedAmount-290) > 1e-9 {
		t.Errorf("two days read: counted %v, superseded %v; want 290 and 10 (one day of 30)", got.CountedAmount, got.SupersededAmount)
	}

	// A limit on a window no reading names: no meter charge either.
	other := limitOf(400, nov.AddDate(0, -1, 0), nil)
	other.WindowKind = "monthly_other"
	meter = meterLimitCharges(rs, nov, []UsageLimit{other}, usageAll(nov))
	if len(meter) != 0 {
		t.Errorf("meter charges for an unread window: %+v", meter)
	}
	if got := chargeInWindow(c, nov, now, usageAll(nov), meterSpans(meter, c.SecretKey, 0)); got.CountedAmount != 300 || got.SupersededAmount != 0 {
		t.Errorf("unread window: counted %v, superseded %v; want 300 and 0", got.CountedAmount, got.SupersededAmount)
	}
}

// A new month's money lies after the reset: the step from the old month's
// last reading is not cut pro rata at a range that starts on the first
// (outside review of af6ebb13, minor 2).
func TestMeterLimitChargesNewMonthAfterReset(t *testing.T) {
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dec := nov.AddDate(0, 1, 0)
	rs := []meterRow{
		{key: "claude_code_oauth_token", source: SourceInband, window: "overage",
			at: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), pct: 90, resets: &nov},
		{key: "claude_code_oauth_token", source: SourceInband, window: "overage",
			at: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), pct: 10, resets: &dec},
	}
	limits := []UsageLimit{limitOf(400, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), nil)}
	got := meterLimitCharges(rs, nov, limits, usageAll(nov.AddDate(0, -2, 0)))
	if counted(got) != 40 {
		t.Errorf("new month from the 1st: %v EUR, want 40 (10 points at 4 EUR, all after the reset)", counted(got))
	}
	if len(got) != 1 || !got[0].From.Equal(nov) || got[0].Points == nil || *got[0].Points != 10 {
		t.Errorf("span/points: %+v, want from the reset, 10 points", got)
	}
	// A range from Nov 3 holds half of the new month's step (Nov 1 → Nov 5).
	if got := counted(meterLimitCharges(rs, nov.AddDate(0, 0, 2), limits, usageAll(nov.AddDate(0, -2, 0)))); math.Abs(got-20) > 1e-9 {
		t.Errorf("range from Nov 3: %v EUR, want 20", got)
	}
}

// A step from a reading without a limit starts at the limit's valid_from
// (outside review of af6ebb13, minor 3), and a step across a limit change is
// the new row's from its valid_from (nit 8).
func TestMeterLimitChargesSpanStartsAtLimit(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	start := t0.Add(10 * time.Hour)
	rs := limitRows(t0, []float64{40, 50}, nil, 20*time.Hour)
	limits := []UsageLimit{limitOf(400, start, nil)}
	got := meterLimitCharges(rs, t0.Add(-time.Hour), limits, usageAll(t0))
	if counted(got) != 40 || len(got) != 1 || !got[0].From.Equal(start) {
		t.Errorf("whole step: %+v, want 40 EUR from the limit's start", got)
	}
	// The range starts halfway between the limit's start and the reading:
	// half the step, pro rata from the limit's start (not from the reading
	// before, which would give a quarter).
	mid := t0.Add(15 * time.Hour)
	got = meterLimitCharges(rs, mid, limits, usageAll(t0))
	if math.Abs(counted(got)-20) > 1e-9 || !got[0].From.Equal(mid) {
		t.Errorf("from mid: %+v, want 20 EUR from the range's start", got)
	}

	// Across a limit change the step is the new row's, from its valid_from.
	change := t0.Add(5 * time.Hour)
	rs = limitRows(t0, []float64{10, 20}, nil, 10*time.Hour)
	limits = []UsageLimit{limitOf(400, t0.Add(-30*24*time.Hour), &change), limitOf(800, change, nil)}
	got = meterLimitCharges(rs, t0.Add(-time.Hour), limits, usageAll(t0))
	if len(got) != 1 || got[0].LimitAmount != 800 || !got[0].From.Equal(change) || math.Abs(counted(got)-120) > 1e-9 {
		t.Errorf("across a change: %+v, want 120 EUR (160 − 40) under 800 from the change", got)
	}
}

// All sources of one meter are one series (outside review of af6ebb13, major
// 4): manual readings before and after a run that wrote its own in-band
// readings price the whole movement, a reading of a month already over is
// dropped, and a reading without a reset time that lags is no new month.
func TestMeterLimitChargesMergesSources(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	nov, dec := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	row := func(source string, h int, pct float64, resets *time.Time) meterRow {
		return meterRow{key: "claude_code_oauth_token", source: source, window: "overage",
			at: t0.Add(time.Duration(h) * time.Hour), pct: pct, resets: resets}
	}
	// meterQuery's order: partition (source) first, then time.
	rs := []meterRow{
		row(SourceInband, 2, 40, &dec), row(SourceInband, 3, 41, &dec), row(SourceInband, 4, 42, &dec),
		row(SourceManual, 1, 30, &dec), row(SourceManual, 5, 50, &dec),
	}
	limits := []UsageLimit{limitOf(400, t0.Add(-30*24*time.Hour), nil)}
	since := t0.Add(-time.Hour)
	if got := counted(meterLimitCharges(rs, since, limits, usageAll(since))); math.Abs(got-80) > 1e-9 {
		t.Errorf("manual around a run: %v EUR, want 80 (30 → 50 at 4 EUR)", got)
	}

	// A late reading of October's month (reset Nov 1) is dropped, not read
	// as a new month at 95 %; a usage_cmd reading without a reset time two
	// points behind is a lag, not a new month.
	rs = append(rs, row(SourceInband, 6, 95, &nov), row(SourceUsageCmd, 7, 48, nil), row(SourceManual, 8, 52, &dec))
	got := meterLimitCharges(rs, since, limits, usageAll(since))
	if math.Abs(counted(got)-88) > 1e-9 || len(got) != 1 || *got[0].Points != 22 {
		t.Errorf("stale and lagging readings: %v EUR, %+v; want 88 (30 → 52), 22 points", counted(got), got)
	}

	// Where no reading names a reset, a drop of more than a point is a new
	// month: 50 then 5 counts the 5.
	rs = []meterRow{row(SourceManual, 1, 48, nil), row(SourceInband, 2, 50, nil), row(SourceManual, 3, 5, nil)}
	if got := counted(meterLimitCharges(rs, since, limits, usageAll(since))); math.Abs(got-28) > 1e-9 {
		t.Errorf("drop without reset times: %v EUR, want 28 (2 + 5 points)", got)
	}
}
