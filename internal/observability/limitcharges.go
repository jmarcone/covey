package observability

import (
	"context"
	"errors"
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
// A credential has one limit at a time (it names the window it reads). The
// money is a usage charge of kind "meter" in the cells, one per credential ×
// limit row, beside the charges entered by hand: counted where a usage basis
// (usage, seat_and_usage) held when the reading was taken, like every usage
// charge, and listed but counted nowhere otherwise. Where a limit holds, the
// meter IS the money: a charge entered by hand over that time is listed, its
// part superseded, and not counted again (usageChargesFor).

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
		// spent is the month's spend at each reading: its utilisation × the
		// limit valid when it was read (utilisation is a share of the limit
		// in force), and a step is the rise of its running maximum within
		// the month — so a step across a limit change is u2·L2 − u1·L1, not
		// Δu × L2, and a lagging reading adds nothing. A reading without a
		// limit names no money.
		// Where the reading before had no limit (one entered in mid-month),
		// its spend is read at this reading's limit: the month's spend
		// before the limit is not this limit's step.
		var prevSpent, prevTop float64
		prevPriced := false
		for i, r := range rs {
			l, ok := limitAt(limits, k.key, k.slot, k.window, r.at)
			newMonth := i > 0 && r.week != rs[i-1].week
			if i == 0 || newMonth {
				prevSpent, prevTop, prevPriced = 0, 0, true
			}
			spent := 0.0
			if ok {
				spent = r.pct * l.Amount / 100
				if !prevPriced {
					prevSpent, prevPriced = prevTop*l.Amount/100, true
				}
			}
			step := 0.0
			if i > 0 && ok {
				step = max(0, spent-prevSpent)
			}
			prevTop = max(prevTop, r.pct)
			if ok {
				prevSpent = max(prevSpent, spent)
			} else {
				prevPriced = false
			}
			if !ok || i == 0 || !r.at.After(since) {
				continue
			}
			from, share := rs[i-1].at, 1.0
			if from.Before(since) {
				// The step's span (from, at] began before the range: the
				// range holds its part, pro rata by time, like a charge.
				share = float64(r.at.Sub(since)) / float64(r.at.Sub(from))
				from = since
			}
			a, ok := byLimit[l.ValidFrom]
			if !ok {
				zero, zeroC := 0.0, 0.0
				a = &acc{c: WindowCharge{Kind: ChargeKindMeter, SecretKey: k.key, SecretSlot: k.slot,
					Currency: l.Currency, From: from, To: r.at, FXToUSD: l.FXToUSD, FXDate: l.FXDate,
					WindowKind: k.window, LimitAmount: l.Amount, Points: &zero, CountedPoints: &zeroC},
					segments: map[int64]bool{}}
				byLimit[l.ValidFrom] = a
				order = append(order, l.ValidFrom)
			}
			a.c.To = r.at
			amount, points := step*share, r.points*share
			*a.c.Points += points
			a.c.Amount += amount
			a.c.WindowAmount += amount
			if b := basisAt(bases, k.key, k.slot, r.at); b == BasisUsage || b == BasisSeatAndUsage {
				*a.c.CountedPoints += points
				a.c.CountedAmount += amount
				a.segments[r.week] = true
			}
		}
		for _, from := range order {
			a := byLimit[from]
			c := a.c
			// The meter shows whole percents: each month segment of the
			// counted movement is known to ±1 point of the limit.
			slack := float64(len(a.segments)) * c.LimitAmount / 100
			low, high := max(0, c.CountedAmount-slack), c.CountedAmount+slack
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

// ErrNoCredential: no credential of the organisation at that place.
var ErrNoCredential = errors.New("no credential of this organisation at that runtime and position")

// AddManualReading files a meter reading a person read off the provider —
// Claude Code's /usage screen, or the rate_limit_event a one-line
// `claude -p … --output-format stream-json --verbose` prints — for the
// credential at a place: source manual, the window it names, the
// utilisation in percent, read at fetchedAt. A seat whose meter covey's own
// runs read only while they run gets its before and after readings this way.
// The same reading filed twice is kept once.
func (s *Store) AddManualReading(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, window string, pct float64,
	resetsAt *time.Time, fetchedAt time.Time) error {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runtime_credentials rc JOIN runtimes r ON r.id = rc.runtime_id
		WHERE r.org_id = $1 AND rc.runtime_id = $2 AND rc.ord = $3)`, orgID, runtimeID, ord).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNoCredential
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot,
		credential_kind, source, window_kind, supported, window_percent, week_percent, resets_at, status, stale,
		fetched_at, received_at)
		SELECT r.org_id, rc.runtime_id, rc.ord, rc.secret_key, rc.secret_slot, rc.kind, 'manual', $4::text, true,
			CASE WHEN $4::text LIKE 'five\_hour%' THEN $5::float8 END,
			CASE WHEN $4::text NOT LIKE 'five\_hour%' THEN $5::float8 END,
			$6, '', false, $7, now()
		FROM runtime_credentials rc JOIN runtimes r ON r.id = rc.runtime_id
		WHERE r.org_id = $1 AND rc.runtime_id = $2 AND rc.ord = $3
		ON CONFLICT DO NOTHING`, orgID, runtimeID, ord, window, pct, resetsAt, fetchedAt)
	return err
}
