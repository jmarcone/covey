package observability

import (
	"testing"
	"time"
)

// The real money of a credential by its basis (0151, #120): the charges'
// window and counted parts, and the plans as they price the meter.

var t0 = time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

func basis(b string, from time.Time, to *time.Time) CredentialBasis {
	return CredentialBasis{SecretKey: "work", SecretSlot: 0, Basis: b, ValidFrom: from, ValidTo: to}
}

func charge(amount float64, currency string, from, to time.Time) WindowCharge {
	return WindowCharge{SecretKey: "work", Amount: amount, Currency: currency, From: from, To: to}
}

// A EUR charge for 24 h inside the window, under a usage basis: all of it is
// counted, in EUR; no dollar figure without a rate, and with one the amount
// at that rate.
func TestChargeEURWithAndWithoutRate(t *testing.T) {
	since, now := t0.Add(-48*time.Hour), t0.Add(48*time.Hour)
	bases := []CredentialBasis{basis(BasisUsage, t0.Add(-72*time.Hour), nil)}
	c := chargeInWindow(charge(180, "EUR", t0, t0.Add(24*time.Hour)), since, now, bases)
	if c.WindowAmount != 180 || c.CountedAmount != 180 || c.CountedUSD != nil {
		t.Errorf("without a rate: window %v counted %v usd %v; want 180, 180, none", c.WindowAmount, c.CountedAmount, c.CountedUSD)
	}
	rate, day := 1.08, "2026-11-18"
	in := charge(180, "EUR", t0, t0.Add(24*time.Hour))
	in.FXToUSD, in.FXDate = &rate, &day
	c = chargeInWindow(in, since, now, bases)
	if c.CountedUSD == nil || !near(*c.CountedUSD, 194.4) || c.CountedAmount != 180 {
		t.Errorf("with a rate: counted %v usd %v; want 180 and 194.40", c.CountedAmount, c.CountedUSD)
	}
	// A rate without its date is no rate.
	in.FXDate = nil
	if c = chargeInWindow(in, since, now, bases); c.CountedUSD != nil {
		t.Errorf("a rate without its date gave %v dollars", *c.CountedUSD)
	}
	// A USD charge is its own dollar figure.
	c = chargeInWindow(charge(50, "USD", t0, t0.Add(time.Hour)), since, now, bases)
	if c.CountedUSD == nil || *c.CountedUSD != 50 {
		t.Errorf("USD charge: usd %v, want 50", c.CountedUSD)
	}
}

// Only the part of the span inside the window counts, pro rata by time; a
// charge still running counts up to now.
func TestChargeProRataInWindow(t *testing.T) {
	bases := []CredentialBasis{basis(BasisSeatAndUsage, t0.Add(-72*time.Hour), nil)}
	// The window starts 6 h into the charge: 18 of 24 hours.
	c := chargeInWindow(charge(180, "EUR", t0, t0.Add(24*time.Hour)), t0.Add(6*time.Hour), t0.Add(48*time.Hour), bases)
	if !near(c.WindowAmount, 135) || !near(c.CountedAmount, 135) {
		t.Errorf("window starting inside: %v / %v, want 135", c.WindowAmount, c.CountedAmount)
	}
	// Now is 12 h into the charge: half of it.
	c = chargeInWindow(charge(180, "EUR", t0, t0.Add(24*time.Hour)), t0.Add(-time.Hour), t0.Add(12*time.Hour), bases)
	if !near(c.WindowAmount, 90) || !near(c.CountedAmount, 90) {
		t.Errorf("charge still running: %v / %v, want 90", c.WindowAmount, c.CountedAmount)
	}
}

// The basis per period: a charge counts only over the times a usage basis
// held. No basis, or seat, lists it and counts nothing.
func TestChargeCountsUnderAUsageBasisOnly(t *testing.T) {
	since, now := t0.Add(-48*time.Hour), t0.Add(48*time.Hour)
	ch := charge(240, "EUR", t0, t0.Add(24*time.Hour))
	if c := chargeInWindow(ch, since, now, nil); c.WindowAmount != 240 || c.CountedAmount != 0 || c.CountedUSD != nil {
		t.Errorf("no basis: window %v counted %v; want 240 listed, 0 counted", c.WindowAmount, c.CountedAmount)
	}
	if c := chargeInWindow(ch, since, now, []CredentialBasis{basis(BasisSeat, since, nil)}); c.CountedAmount != 0 {
		t.Errorf("seat basis counted %v", c.CountedAmount)
	}
	// seat until t0+6h, usage until t0+18h, seat_and_usage after: 6 h are
	// not counted, 18 are.
	bases := []CredentialBasis{
		basis(BasisSeat, since, tp(t0.Add(6*time.Hour))),
		basis(BasisUsage, t0.Add(6*time.Hour), tp(t0.Add(18*time.Hour))),
		basis(BasisSeatAndUsage, t0.Add(18*time.Hour), nil),
	}
	if c := chargeInWindow(ch, since, now, bases); c.WindowAmount != 240 || !near(c.CountedAmount, 180) {
		t.Errorf("mixed bases: window %v counted %v; want 240, 180", c.WindowAmount, c.CountedAmount)
	}
	// Another credential's basis counts nothing here.
	other := []CredentialBasis{{SecretKey: "home", Basis: BasisUsage, ValidFrom: since}}
	if c := chargeInWindow(ch, since, now, other); c.CountedAmount != 0 {
		t.Errorf("another credential's basis counted %v", c.CountedAmount)
	}
}

// Without a usage basis the plans price the meter as they are — the same
// slice; a usage basis cuts the plan's validity where it holds.
func TestSeatPlansCutWhereUsageHolds(t *testing.T) {
	plans := []CredentialPlan{
		{SecretKey: "work", Amount: 200, ValidFrom: t0.Add(-100 * time.Hour)},
		{SecretKey: "home", Amount: 100, ValidFrom: t0.Add(-100 * time.Hour)},
	}
	for _, bs := range [][]CredentialBasis{nil, {basis(BasisSeat, t0, nil)}, {basis(BasisSeatAndUsage, t0, nil)}} {
		if got := seatPlans(plans, bs); &got[0] != &plans[0] || len(got) != 2 {
			t.Errorf("bases %v: the plans were rebuilt", bs)
		}
	}
	got := seatPlans(plans, []CredentialBasis{
		basis(BasisUsage, t0, tp(t0.Add(24*time.Hour))),
		basis(BasisSeat, t0.Add(24*time.Hour), nil),
	})
	if _, ok := planAt(got, "work", 0, t0.Add(time.Hour)); ok {
		t.Error("the plan prices a reading under the usage basis")
	}
	if p, ok := planAt(got, "work", 0, t0.Add(-time.Hour)); !ok || p.Amount != 200 {
		t.Error("the plan no longer prices a reading before the usage basis")
	}
	if p, ok := planAt(got, "work", 0, t0.Add(25*time.Hour)); !ok || p.Amount != 200 {
		t.Error("the plan no longer prices a reading after the usage basis")
	}
	if _, ok := planAt(got, "home", 0, t0.Add(time.Hour)); !ok {
		t.Error("another credential's plan was cut")
	}
	// An open usage basis cuts the open plan for good.
	got = seatPlans(plans, []CredentialBasis{basis(BasisUsage, t0, nil)})
	if _, ok := planAt(got, "work", 0, t0.Add(1000*time.Hour)); ok {
		t.Error("an open usage basis left the plan open")
	}
}
