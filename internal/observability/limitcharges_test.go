package observability

import (
	"math"
	"testing"
	"time"
)

// limitRows builds one partition's readings the way meterQuery returns them:
// pct per reading, the month (week column) changing where reset is set, the
// step the rise of the month's running maximum.
func limitRows(t0 time.Time, pcts []float64, resets map[int]bool, gap time.Duration) []meterRow {
	var rs []meterRow
	week, top := int64(0), 0.0
	for i, p := range pcts {
		r := meterRow{key: "claude_code_oauth_token", slot: 0, kind: "subscription", source: SourceInband,
			window: "overage", at: t0.Add(time.Duration(i) * gap), pct: p}
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

// Where a limit holds, a charge entered by hand over that time is not counted
// again: the meter is the money (outside review, major 2).
func TestChargeSupersededByLimit(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	c := WindowCharge{SecretKey: "claude_code_oauth_token", Amount: 100, Currency: "EUR", From: t0, To: t0.Add(10 * time.Hour)}
	half := t0.Add(5 * time.Hour)
	got := chargeInWindow(c, t0, t0.Add(24*time.Hour), usageAll(t0), []UsageLimit{limitOf(400, half, nil)})
	if math.Abs(got.CountedAmount-50) > 1e-9 || math.Abs(got.SupersededAmount-50) > 1e-9 {
		t.Errorf("counted %v, superseded %v; want 50 and 50", got.CountedAmount, got.SupersededAmount)
	}
	other := limitOf(400, t0, nil)
	other.SecretSlot = 1
	if got := chargeInWindow(c, t0, t0.Add(24*time.Hour), usageAll(t0), []UsageLimit{other}); got.CountedAmount != 100 {
		t.Errorf("another credential's limit superseded %v", got.SupersededAmount)
	}
}
