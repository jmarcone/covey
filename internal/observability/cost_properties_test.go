package observability

import (
	"math"
	mathrand "math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Properties of the cost figures that must hold for any input, in the order
// of the invariants the cost page rests on (#165): the parts of a seat's
// money sum to its total for any range; the money of a monthly meter is the
// same whatever limit its spend is expressed at (H16). Each is checked over
// random inputs from a fixed seed, so a failure repeats; a failing case is
// reported with its iteration. They were each shown to fail on a broken copy
// of the code (see the PR of #165).

// randomShareSteps is a random seat as TestSplitSeatInvariants builds it:
// steps of 10 minutes, some without movement, rate, outside or covey lists,
// some unreported, with resets, unpriced calls and unlined bookings.
func randomShareSteps(r *mathrand.Rand, ag []uuid.UUID) []shareStep {
	var ss []shareStep
	for i, n := 0, 1+r.Intn(25); i < n; i++ {
		st := shareStep{Reported: r.Intn(8) != 0}
		switch r.Intn(5) {
		case 0:
		case 1, 2:
			st.Points = float64(r.Intn(3))
		default:
			st.Points = float64(r.Intn(12))
		}
		switch r.Intn(6) {
		case 0:
		case 1:
			st.Rate = rate(2)
		default:
			st.Rate = rate(1)
		}
		if r.Intn(3) != 0 {
			st.LOut = float64(r.Intn(50))
		}
		if r.Intn(3) != 0 {
			st.Agents = map[uuid.UUID]float64{}
			for _, a := range ag {
				if r.Intn(2) == 0 {
					l := float64(1 + r.Intn(30))
					st.Agents[a] = l
					st.LCov += l
				}
			}
		}
		if r.Intn(15) == 0 {
			st.UnpricedCalls = 1
		}
		if r.Intn(20) == 0 {
			st.Reset = true
		}
		ss = append(ss, st)
	}
	at := tm("10:00")
	for i := range ss {
		ss[i].From, ss[i].At = at.Add(time.Duration(i)*10*time.Minute), at.Add(time.Duration(i+1)*10*time.Minute)
	}
	return ss
}

// For any range (any since) the seat's parts add up to its total: covey +
// outside + unattributed + unsplit = R, in every step and in the seat, and
// the seat's R is the sum of the R of the steps reported, nothing before
// since and nothing twice, and a range that begins before the first step is
// the whole seat. (How a chunk's unattributed part falls inside a cut range
// is an "at least" bound by design, sinceCuts: a step is not asserted to be
// cut as in the whole.)
func TestSplitSeatPartsSumToTheTotalForAnyRange(t *testing.T) {
	r := mathrand.New(mathrand.NewSource(165))
	ag := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	val := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	for iter := 0; iter < 5000; iter++ {
		ss := randomShareSteps(r, ag)
		whole, _ := splitSeat(meterSeat{"seat", 0}, []string{"src"}, ss)
		var since time.Time
		switch r.Intn(3) {
		case 1:
			since = ss[r.Intn(len(ss))].At.Add(-time.Second)
		case 2:
			since = ss[r.Intn(len(ss))].At.Add(time.Second)
		}
		tot, steps := splitSeatSince(meterSeat{"seat", 0}, []string{"src"}, ss, since)

		var sumR, parts float64
		for i, st := range steps {
			if st.At.Before(since) {
				t.Fatalf("iteration %d: step %d at %v is before since %v", iter, i, st.At, since)
			}
			if st.RealUSD == nil {
				continue
			}
			sumR += *st.RealUSD
			p := val(st.CoveyRealUSD) + val(st.ExternalRealUSD) + st.UnattributedRealUSD + st.UnsplitRealUSD
			parts += p
			if math.Abs(p-*st.RealUSD) > 1e-9 {
				t.Fatalf("iteration %d: step %d parts %v, R %v: %+v", iter, i, p, *st.RealUSD, st)
			}
		}
		seat := tot.CoveyRealUSD + tot.ExternalRealUSD + tot.UnattributedRealUSD + tot.UnsplitRealUSD
		if math.Abs(seat-tot.RealUSD) > 1e-9 || math.Abs(tot.RealUSD-sumR) > 1e-9 {
			t.Fatalf("iteration %d (since %v): seat parts %v, seat R %v, steps' R %v", iter, since, seat, tot.RealUSD, sumR)
		}
		// The whole range is the range from the first step on.
		if since.IsZero() && math.Abs(whole.RealUSD-tot.RealUSD) > 1e-12 {
			t.Fatalf("iteration %d: splitSeat %v, splitSeatSince(zero) %v", iter, whole.RealUSD, tot.RealUSD)
		}
	}
}

// H16: the money of a monthly meter is the spend, utilisation × limit, so
// the same spend written at another limit — every utilisation scaled by c,
// every limit by 1/c — is the same money: in the charges' amounts and in
// what counts, in the money's currency and in dollars, and over any range.
// The limits change mid-series at random (the money across a change is u2·L2
// − u1·L1), the reset times are the meter's own, so nothing but the unit of
// the limit differs between the two runs.
func TestMeterLimitChargesAreInvariantUnderReExpressingTheLimit(t *testing.T) {
	r := mathrand.New(mathrand.NewSource(165))
	t0 := time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC)
	fx := 1.08
	fxDate := "2026-03-01"
	for iter := 0; iter < 3000; iter++ {
		n := 2 + r.Intn(14)
		pcts := make([]float64, n)
		resets := map[int]bool{}
		for i := range pcts {
			switch {
			case i == 0:
				pcts[i] = float64(r.Intn(40))
			case r.Intn(8) == 0:
				resets[i], pcts[i] = true, float64(r.Intn(15))
			case r.Intn(6) == 0:
				pcts[i] = max(0, pcts[i-1]-float64(r.Intn(3))) // a lag
			default:
				pcts[i] = min(100, pcts[i-1]+float64(r.Intn(9)))
			}
		}
		gap := time.Duration(1+r.Intn(48)) * time.Hour
		rs := limitRows(t0, pcts, resets, gap)

		// One or two limits, the second from a random reading on.
		amounts := []float64{50 + float64(r.Intn(500))}
		from := []time.Time{t0.Add(-24 * time.Hour)}
		if r.Intn(2) == 0 {
			amounts = append(amounts, 50+float64(r.Intn(500)))
			from = append(from, rs[r.Intn(n)].at.Add(-time.Second))
		}
		c := []float64{0.25, 0.5, 2, 4, 10}[r.Intn(5)]
		limits := func(scale float64) []UsageLimit {
			var out []UsageLimit
			for i, a := range amounts {
				l := limitOf(a*scale, from[i], nil)
				if i+1 < len(amounts) {
					to := from[i+1]
					l.ValidTo = &to
				}
				l.FXToUSD, l.FXDate = &fx, &fxDate
				out = append(out, l)
			}
			return out
		}
		scaled := make([]meterRow, len(rs))
		copy(scaled, rs)
		for i := range scaled {
			scaled[i].pct, scaled[i].top = rs[i].pct/c, rs[i].top/c
		}
		var since time.Time
		if r.Intn(2) == 0 {
			since = rs[r.Intn(n)].at.Add(time.Duration(r.Intn(3)-1) * time.Hour)
		}
		bases := usageAll(t0.Add(-48 * time.Hour))
		a := meterLimitCharges(rs, since, limits(1), bases)
		b := meterLimitCharges(scaled, since, limits(c), bases)
		if len(a) != len(b) {
			t.Fatalf("iteration %d: %d charges, %d re-expressed at c = %v", iter, len(a), len(b), c)
		}
		for i := range a {
			ok := math.Abs(a[i].Amount-b[i].Amount) < 1e-9 && math.Abs(a[i].CountedAmount-b[i].CountedAmount) < 1e-9 &&
				math.Abs(a[i].WindowAmount-b[i].WindowAmount) < 1e-9 && (a[i].CountedUSD == nil) == (b[i].CountedUSD == nil) &&
				(a[i].CountedUSD == nil || math.Abs(*a[i].CountedUSD-*b[i].CountedUSD) < 1e-9)
			if !ok {
				t.Fatalf("iteration %d: charge %d is %+v, re-expressed at c = %v %+v; pcts %v, limits %v, since %v", iter, i, a[i], c, b[i],
					pcts, amounts, since)
			}
		}
	}
}
