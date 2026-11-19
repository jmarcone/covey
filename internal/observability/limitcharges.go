package observability

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pay-per-token seats (migration 0152): an Enterprise seat billed per token
// against a monthly money limit reports its meter in band as a window of its
// own ("overage", reset on the first of the month). With the limit stored, a
// point of that window is Amount/100 in the limit's currency, so the seat's
// real spend over a range is the rise of utilisation × limit within each
// month — money in its own currency, never the weekly plan's dollars. Unlike
// the weekly meter, whose sources stay apart, every source of one meter
// (inband, manual, usage_cmd) is one series here: a person's readings before
// and after a run belong to the same month as the run's own.
//
// A credential has one limit at a time (it names the window it reads). The
// money is a usage charge of kind "meter" in the cells, one per credential ×
// limit row, beside the charges entered by hand: counted where a usage basis
// (usage, seat_and_usage) held when the reading was taken, like every usage
// charge, and listed but counted nowhere otherwise. Over the spans the meter
// priced, the meter IS the money: a charge entered by hand over them is
// listed, that part superseded, and not counted again (usageChargesFor).
// Where the meter read nothing, the hand-entered charge stays the money.

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

// monthReading is one reading of a monthly meter, all its sources merged.
type monthReading struct {
	at  time.Time
	pct float64
	// points is the rise of the month's running maximum of utilisation (the
	// whole reading on a new month's first): meter points, not money.
	points   float64
	month    int
	newMonth bool
	// boundary is where a new month began — the reset the month before named
	// — when it lies between the reading before and this one; zero otherwise.
	boundary time.Time
}

// sourceRank orders readings taken at the same moment: usage_cmd, manual,
// inband.
func sourceRank(s string) int {
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

// monthlySeries merges the readings of one credential × window from every
// source into one series in time order and finds its months:
//
//   - the provider's own reset time wins: a manual reading whose reset
//     disagrees by more than an hour with the non-manual readings around it
//     is dropped (manualResetAgrees);
//   - a reading whose reset time lies more than an hour before the latest one
//     seen so far belongs to a month already over and is dropped;
//   - a month begins where the reset time advances by more than an hour; a
//     reading without one begins a month when the month's reset has passed,
//     and where no reset time is known at all, when it dropped by more than
//     one point (the meter shows whole percents: a drop of one is a lag).
//
// meterQuery already dropped stale and unsupported readings, and those a
// source's own later readings mark as from an old month.
func monthlySeries(rs []meterRow) []monthReading {
	sorted := append([]meterRow(nil), rs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].at.Equal(sorted[j].at) {
			return sorted[i].at.Before(sorted[j].at)
		}
		return sourceRank(sorted[i].source) < sourceRank(sorted[j].source)
	})
	// The reset a non-manual source (in band, usage_cmd) names before each
	// reading (the latest so far, so a late reading of an old month is no
	// reference) and the next one after it: what the provider itself said.
	prevAuto := make([]*time.Time, len(sorted))
	nextAuto := make([]*time.Time, len(sorted))
	var last *time.Time
	for i, r := range sorted {
		prevAuto[i] = last
		if r.source != SourceManual && r.resets != nil && (last == nil || r.resets.After(*last)) {
			last = r.resets
		}
	}
	last = nil
	for i := len(sorted) - 1; i >= 0; i-- {
		nextAuto[i] = last
		if sorted[i].source != SourceManual && sorted[i].resets != nil {
			last = sorted[i].resets
		}
	}
	var out []monthReading
	var seen, mark *time.Time // latest reset seen at all; latest of this month
	var top, prevPct float64
	var prevAt time.Time
	month := 0
	for i, r := range sorted {
		if r.source == SourceManual && !manualResetAgrees(r, prevAuto[i], nextAuto[i]) {
			// A person typed a reset the provider's own readings around it
			// contradict: dropped, and never the "latest seen", which would
			// make every later true reading look stale.
			continue
		}
		if r.resets != nil && seen != nil && r.resets.Before(seen.Add(-time.Hour)) {
			continue
		}
		if r.resets != nil && (seen == nil || r.resets.After(*seen)) {
			seen = r.resets
		}
		m := monthReading{at: r.at, pct: r.pct}
		if len(out) == 0 {
			top, mark = r.pct, r.resets
		} else {
			var newMonth bool
			switch {
			case r.resets != nil && mark != nil:
				newMonth = r.resets.After(mark.Add(time.Hour))
			case r.resets == nil && mark != nil:
				newMonth = !mark.After(r.at)
			default:
				newMonth = r.pct < prevPct-1
			}
			if newMonth {
				month++
				m.newMonth, m.points = true, r.pct
				if mark != nil && mark.After(prevAt) && mark.Before(r.at) {
					m.boundary = *mark
				}
				top, mark = r.pct, r.resets
			} else {
				m.points = max(0, r.pct-top)
				top = max(top, r.pct)
				if r.resets != nil && (mark == nil || r.resets.After(*mark)) {
					mark = r.resets
				}
			}
		}
		m.month = month
		prevPct, prevAt = r.pct, r.at
		out = append(out, m)
	}
	return out
}

// manualResetAgrees says whether a manual reading's reset time fits the
// resets the non-manual readings before (prev) and after (next) it name: it
// matches one of them to the hour, or there is none to contradict it, or the
// one before has passed by the time it was read and none after says
// otherwise (the reading may name the next month's reset).
func manualResetAgrees(r meterRow, prev, next *time.Time) bool {
	if r.resets == nil || (prev == nil && next == nil) {
		return true
	}
	near := func(t *time.Time) bool {
		if t == nil {
			return false
		}
		d := r.resets.Sub(*t)
		return d <= time.Hour && d >= -time.Hour
	}
	if near(prev) || near(next) {
		return true
	}
	return next == nil && prev != nil && !prev.After(r.at) && r.resets.After(*prev)
}

// meterLimitCharges prices the readings (meterRows) of every window that has
// a limit, all sources of a credential × window merged (monthlySeries). A
// step counts at the limit valid when it was read; its span is (the reading
// before, the reading], starting no earlier than the limit row's valid_from
// (a step across a limit change, or from a reading without a limit, is the
// new row's) nor than the month's start (a new month's money lies after the
// reset). Where the span began before the range, the range holds its part,
// pro rata by time, like a charge. The first reading is the baseline and
// moved nothing.
func meterLimitCharges(rows []meterRow, since time.Time, limits []UsageLimit, bases []CredentialBasis) []WindowCharge {
	type cw struct {
		key    string
		slot   int
		window string
	}
	series := map[cw][]meterRow{}
	var keys []cw
	for _, r := range rows {
		k := cw{r.key, r.slot, r.window}
		if !hasLimit(limits, k.key, k.slot, k.window) {
			continue
		}
		if _, ok := series[k]; !ok {
			keys = append(keys, k)
		}
		series[k] = append(series[k], r)
	}

	var out []WindowCharge
	for _, k := range keys {
		rs := monthlySeries(series[k])
		type acc struct {
			c        WindowCharge
			segments map[int]bool
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
			if i == 0 || r.newMonth {
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
			// The step's span: from the reading before, but not before the
			// month began (all of a new month's money lies after the reset)
			// nor before this limit row (a step from an unlimited reading or
			// across a limit change is this row's, from its start).
			from := rs[i-1].at
			if r.newMonth && !r.boundary.IsZero() {
				from = r.boundary
			}
			if l.ValidFrom.After(from) {
				from = l.ValidFrom
			}
			share := 1.0
			if from.Before(since) {
				// The span began before the range: the range holds its part,
				// pro rata by time, like a charge.
				share = float64(r.at.Sub(since)) / float64(r.at.Sub(from))
				from = since
			}
			a, ok := byLimit[l.ValidFrom]
			if !ok {
				zero, zeroC := 0.0, 0.0
				a = &acc{c: WindowCharge{Kind: ChargeKindMeter, SecretKey: k.key, SecretSlot: k.slot,
					Currency: l.Currency, From: from, To: r.at, FXToUSD: l.FXToUSD, FXDate: l.FXDate,
					WindowKind: k.window, LimitAmount: l.Amount, Points: &zero, CountedPoints: &zeroC},
					segments: map[int]bool{}}
				byLimit[l.ValidFrom] = a
				order = append(order, l.ValidFrom)
			}
			a.c.To = r.at
			a.c.Spans = append(a.c.Spans, span{from, r.at})
			amount, points := step*share, r.points*share
			*a.c.Points += points
			a.c.Amount += amount
			a.c.WindowAmount += amount
			// Counted pro rata by the part of the step's span a usage basis
			// held — the rule a hand-entered charge follows, so the counted
			// meter money and the superseded charge cover the same time.
			if u := usageShare(bases, k.key, k.slot, from, r.at); u > 0 {
				*a.c.CountedPoints += points * u
				a.c.CountedAmount += amount * u
				a.segments[r.month] = true
			}
		}
		for _, from := range order {
			a := byLimit[from]
			c := a.c
			c.Spans = mergeSpans(c.Spans)
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

// usageShare is the part of [from, to) a usage basis (usage, seat_and_usage)
// held, 0..1; for an empty span, whether one held at its end.
func usageShare(bases []CredentialBasis, key string, slot int, from, to time.Time) float64 {
	if !to.After(from) {
		if b := basisAt(bases, key, slot, to); b == BasisUsage || b == BasisSeatAndUsage {
			return 1
		}
		return 0
	}
	var held time.Duration
	for _, sp := range basisSpans(bases, key, slot, BasisUsage, BasisSeatAndUsage) {
		held += overlap(from, to, sp.from, sp.to)
	}
	if held >= to.Sub(from) {
		return 1
	}
	return float64(held) / float64(to.Sub(from))
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

// ErrReadingExists: a reading of that credential and window at that moment is
// filed already (from the same source); the new one was not stored.
var ErrReadingExists = errors.New("a manual reading of this credential and window at that fetched_at exists already")

// KnownReset is the reset time the provider itself (a non-manual source)
// named last for the credential at a place and its window: the latest
// supported, fresh reading with a reset fetched at or before `at` and no
// more than 35 days before it. Nil where there is none.
func (s *Store) KnownReset(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, window string, at time.Time) (*time.Time, error) {
	var out *time.Time
	err := s.pool.QueryRow(ctx, `SELECT ur.resets_at FROM usage_readings ur
		JOIN runtime_credentials rc ON rc.secret_key = ur.secret_key AND rc.secret_slot = ur.secret_slot
		JOIN runtimes r ON r.id = rc.runtime_id AND r.org_id = ur.org_id
		WHERE ur.org_id = $1 AND rc.runtime_id = $2 AND rc.ord = $3 AND ur.window_kind = $4
		  AND ur.source <> 'manual' AND ur.resets_at IS NOT NULL AND ur.supported AND NOT ur.stale
		  AND ur.fetched_at <= $5 AND ur.fetched_at >= $5::timestamptz - interval '35 days'
		ORDER BY ur.fetched_at DESC, ur.id DESC LIMIT 1`, orgID, runtimeID, ord, window, at).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return out, err
}

// AddManualReading files a meter reading a person read off the provider —
// Claude Code's /usage screen, or the rate_limit_event a one-line
// `claude -p … --output-format stream-json --verbose` prints — for the
// credential at a place: source manual, the window it names, the
// utilisation in percent, read at fetchedAt. A seat whose meter covey's own
// runs read only while they run gets its before and after readings this way.
// A second reading at the same moment is not stored: ErrReadingExists.
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
	tag, err := s.pool.Exec(ctx, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot,
		credential_kind, source, window_kind, supported, window_percent, week_percent, resets_at, status, stale,
		fetched_at, received_at)
		SELECT r.org_id, rc.runtime_id, rc.ord, rc.secret_key, rc.secret_slot, rc.kind, 'manual', $4::text, true,
			CASE WHEN $4::text LIKE 'five\_hour%' THEN $5::float8 END,
			CASE WHEN $4::text NOT LIKE 'five\_hour%' THEN $5::float8 END,
			$6, '', false, $7, now()
		FROM runtime_credentials rc JOIN runtimes r ON r.id = rc.runtime_id
		WHERE r.org_id = $1 AND rc.runtime_id = $2 AND rc.ord = $3
		ON CONFLICT DO NOTHING`, orgID, runtimeID, ord, window, pct, resetsAt, fetchedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrReadingExists
	}
	return nil
}
