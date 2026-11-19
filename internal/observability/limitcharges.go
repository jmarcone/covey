package observability

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Pay-per-token seats (migration 0152): an Enterprise seat billed per token
// against a monthly money limit reports its meter in band as a window of its
// own ("overage", reset on the first of the month). With the limit stored, a
// point of that window is Amount/100 in the limit's currency, so the seat's
// real spend over a range is the meter's movement × limit / 100 — the same
// readings, step rule and reset rule as the weekly meter (meterQuery), but
// money in its own currency, never the weekly plan's dollars.
//
// The money is a usage charge of kind "meter" in the cells, one per
// credential × window × limit, beside the charges entered by hand: counted
// where a usage basis (usage, seat_and_usage) held when the reading was
// taken, like every usage charge, and listed but counted nowhere otherwise.

// UsageLimit is a credential's monthly usage limit, valid over
// [ValidFrom, ValidTo).
type UsageLimit struct {
	SecretKey  string     `json:"secret_key"`
	SecretSlot int        `json:"secret_slot"`
	WindowKind string     `json:"window_kind"`
	Amount     float64    `json:"amount"`
	Currency   string     `json:"currency"`
	FXToUSD    *float64   `json:"fx_to_usd"`
	FXDate     *string    `json:"fx_date"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to"`
}

// usageLimitsFor returns the organisation's limits valid at any point since
// `since` (and the 35 days of baseline before it), per credential in order of
// validity.
func (s *Store) usageLimitsFor(ctx context.Context, orgID uuid.UUID, since time.Time) ([]UsageLimit, error) {
	rows, err := s.pool.Query(ctx, `SELECT secret_key, secret_slot, window_kind, amount::float8, currency,
		fx_to_usd::float8, to_char(fx_date, 'YYYY-MM-DD'), valid_from, valid_to
		FROM credential_usage_limits
		WHERE org_id = $1 AND (valid_to IS NULL OR valid_to > $2)
		ORDER BY secret_key, secret_slot, valid_from`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageLimit
	for rows.Next() {
		var l UsageLimit
		if err := rows.Scan(&l.SecretKey, &l.SecretSlot, &l.WindowKind, &l.Amount, &l.Currency,
			&l.FXToUSD, &l.FXDate, &l.ValidFrom, &l.ValidTo); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// limitAt is the limit of a credential's window valid at t.
func limitAt(limits []UsageLimit, key string, slot int, window string, t time.Time) (UsageLimit, bool) {
	for _, l := range limits {
		if l.SecretKey == key && l.SecretSlot == slot && l.WindowKind == window &&
			!t.Before(l.ValidFrom) && (l.ValidTo == nil || t.Before(*l.ValidTo)) {
			return l, true
		}
	}
	return UsageLimit{}, false
}

// meterLimitCharges prices the readings (meterRows: partition and time
// order, each with its step) of every window that has a limit. A step counts
// at the limit valid when it was read; its span is (the reading before, the
// reading]. The first reading of a credential is its baseline and moved
// nothing; the readings of one partition from more than one source are kept
// apart like the weekly meter's (two sources read one meter: the one with the
// most readings is taken, usage_cmd before manual before inband on a tie).
func meterLimitCharges(rows []meterRow, since time.Time, limits []UsageLimit, bases []CredentialBasis) []WindowCharge {
	type part struct {
		start, end int
	}
	// The partitions, and per credential × window the source read.
	type cw struct {
		key    string
		slot   int
		window string
	}
	best := map[cw]part{}
	rank := func(s string) int {
		switch s {
		case SourceUsageCmd:
			return 0
		case SourceManual:
			return 1
		case SourceInband:
			return 2
		}
		return 3
	}
	inRange := func(p part) int {
		n := 0
		for _, r := range rows[p.start:p.end] {
			if !r.at.Before(since) {
				n++
			}
		}
		return n
	}
	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && rows[end].key == rows[start].key && rows[end].slot == rows[start].slot &&
			rows[end].source == rows[start].source && rows[end].window == rows[start].window {
			end++
		}
		r := rows[start]
		k := cw{r.key, r.slot, r.window}
		if hasLimit(limits, k.key, k.slot, k.window) {
			p := part{start, end}
			if cur, ok := best[k]; !ok || inRange(p) > inRange(cur) ||
				(inRange(p) == inRange(cur) && rank(r.source) < rank(rows[cur.start].source)) {
				best[k] = p
			}
		}
		start = end
	}

	var out []WindowCharge
	for k, p := range best {
		rs := rows[p.start:p.end]
		type acc struct {
			c        WindowCharge
			segments map[int64]bool
		}
		byLimit := map[time.Time]*acc{}
		var order []time.Time
		for i, r := range rs {
			if r.at.Before(since) || i == 0 {
				continue
			}
			l, ok := limitAt(limits, k.key, k.slot, k.window, r.at)
			if !ok {
				continue
			}
			a, ok := byLimit[l.ValidFrom]
			if !ok {
				zero, zeroC := 0.0, 0.0
				a = &acc{c: WindowCharge{Kind: ChargeKindMeter, SecretKey: k.key, SecretSlot: k.slot,
					Currency: l.Currency, From: rs[i-1].at, To: r.at, FXToUSD: l.FXToUSD, FXDate: l.FXDate,
					WindowKind: k.window, LimitAmount: l.Amount, Points: &zero, CountedPoints: &zeroC},
					segments: map[int64]bool{}}
				byLimit[l.ValidFrom] = a
				order = append(order, l.ValidFrom)
			}
			a.c.To = r.at
			amount := r.points * l.Amount / 100
			*a.c.Points += r.points
			a.c.Amount += amount
			a.c.WindowAmount += amount
			if b := basisAt(bases, k.key, k.slot, r.at); b == BasisUsage || b == BasisSeatAndUsage {
				*a.c.CountedPoints += r.points
				a.c.CountedAmount += amount
				a.segments[r.week] = true
			}
		}
		for _, from := range order {
			a := byLimit[from]
			c := a.c
			n := float64(len(a.segments))
			perPoint := c.LimitAmount / 100
			low, high := max(0, *c.CountedPoints-n)*perPoint, (*c.CountedPoints+n)*perPoint
			c.CountedLow, c.CountedHigh = &low, &high
			if c.Currency == "USD" {
				usd := c.CountedAmount
				c.CountedUSD = &usd
			} else if c.FXToUSD != nil && c.FXDate != nil {
				usd := c.CountedAmount * *c.FXToUSD
				c.CountedUSD = &usd
			}
			out = append(out, c)
		}
	}
	sortCharges(out)
	return out
}

// hasLimit says whether a credential's window has any limit at all.
func hasLimit(limits []UsageLimit, key string, slot int, window string) bool {
	for _, l := range limits {
		if l.SecretKey == key && l.SecretSlot == slot && l.WindowKind == window {
			return true
		}
	}
	return false
}

// sortCharges orders meter charges by credential, window and start, so the
// cells do not depend on map order.
func sortCharges(cs []WindowCharge) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.SecretKey != b.SecretKey {
			return a.SecretKey < b.SecretKey
		}
		if a.SecretSlot != b.SecretSlot {
			return a.SecretSlot < b.SecretSlot
		}
		if a.WindowKind != b.WindowKind {
			return a.WindowKind < b.WindowKind
		}
		return a.From.Before(b.From)
	})
}
