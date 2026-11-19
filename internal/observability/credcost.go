package observability

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// How a credential's real money is formed (migration 0151, #120): by its seat
// price, by usage charges, or by both, chosen per credential and dated. The
// cost cells carry the bases valid in the window and the usage charges that
// reach into it; a credential with neither is priced exactly as before 0151.
//
//   - seat: the plan prices the meter's points (stacked.go), as before.
//   - usage: the usage charges are the money. The plan prices nothing while
//     this basis holds, so the meter's points read in it carry no dollars
//     (they count as unpriced): the seat is not what was paid for them.
//   - seat_and_usage: the plan prices the meter and the charges add to it.
//
// A usage charge stays in its currency. Its dollar figure exists only where a
// person gave a rate with its date, or where it is in dollars already; covey
// never converts on its own.

// Cost bases (runtimes.Basis*, repeated here: observability does not import
// the runtimes store).
const (
	BasisSeat         = "seat"
	BasisUsage        = "usage"
	BasisSeatAndUsage = "seat_and_usage"
)

// CredentialBasis is a credential's basis, valid over [ValidFrom, ValidTo).
type CredentialBasis struct {
	SecretKey  string     `json:"secret_key"`
	SecretSlot int        `json:"secret_slot"`
	Basis      string     `json:"basis"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to"`
}

// WindowCharge is a usage charge that reaches into the window, with the part
// of it the window holds.
type WindowCharge struct {
	ID         uuid.UUID `json:"id"`
	SecretKey  string    `json:"secret_key"`
	SecretSlot int       `json:"secret_slot"`
	// Amount over [From, To) in Currency, as entered.
	Amount   float64   `json:"amount"`
	Currency string    `json:"currency"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	// FXToUSD/FXDate: the rate a person gave and its day; nil on a USD charge
	// and on one entered without a rate.
	FXToUSD *float64 `json:"fx_to_usd"`
	FXDate  *string  `json:"fx_date"`
	Note    string   `json:"note"`
	// WindowAmount is the part of Amount over the part of [From, To) inside
	// the window [since, now), pro rata by time, in Currency.
	WindowAmount float64 `json:"window_amount"`
	// CountedAmount is the part of WindowAmount over the times a usage basis
	// (usage, seat_and_usage) held: the real money, in Currency. A charge over
	// a time without one (no basis, or seat) is listed but counted nowhere.
	CountedAmount float64 `json:"counted_amount"`
	// CountedUSD is CountedAmount in dollars: the amount itself for a USD
	// charge, at the given rate otherwise; nil without a rate.
	CountedUSD *float64 `json:"counted_usd"`
}

// costBasesFor returns the organisation's bases valid at any point since
// `since`, per credential in order of validity.
func (s *Store) costBasesFor(ctx context.Context, orgID uuid.UUID, since time.Time) ([]CredentialBasis, error) {
	rows, err := s.pool.Query(ctx, `SELECT secret_key, secret_slot, basis, valid_from, valid_to
		FROM credential_cost_bases
		WHERE org_id = $1 AND (valid_to IS NULL OR valid_to > $2)
		ORDER BY secret_key, secret_slot, valid_from`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialBasis
	for rows.Next() {
		var b CredentialBasis
		if err := rows.Scan(&b.SecretKey, &b.SecretSlot, &b.Basis, &b.ValidFrom, &b.ValidTo); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// usageChargesFor returns the charges that reach into [since, now), each
// with its window and counted parts.
func (s *Store) usageChargesFor(ctx context.Context, orgID uuid.UUID, since, now time.Time, bases []CredentialBasis) ([]WindowCharge, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, secret_key, secret_slot, amount::float8, currency, period_from, period_to,
		fx_to_usd::float8, to_char(fx_date, 'YYYY-MM-DD'), note
		FROM credential_usage_charges
		WHERE org_id = $1 AND period_to > $2 AND period_from < $3
		ORDER BY secret_key, secret_slot, period_from, created_at`, orgID, since, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WindowCharge
	for rows.Next() {
		var c WindowCharge
		if err := rows.Scan(&c.ID, &c.SecretKey, &c.SecretSlot, &c.Amount, &c.Currency, &c.From, &c.To,
			&c.FXToUSD, &c.FXDate, &c.Note); err != nil {
			return nil, err
		}
		out = append(out, chargeInWindow(c, since, now, bases))
	}
	return out, rows.Err()
}

// span is a half-open interval of time; a zero end is open.
type span struct{ from, to time.Time }

// forever stands for an open end.
var forever = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

func endOf(t *time.Time) time.Time {
	if t == nil {
		return forever
	}
	return *t
}

// basisSpans are the times the credential's basis was one of `want`.
func basisSpans(bases []CredentialBasis, key string, slot int, want ...string) []span {
	var out []span
	for _, b := range bases {
		if b.SecretKey != key || b.SecretSlot != slot {
			continue
		}
		for _, w := range want {
			if b.Basis == w {
				out = append(out, span{b.ValidFrom, endOf(b.ValidTo)})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from.Before(out[j].from) })
	return out
}

// overlap is the length of [a, b) ∩ [c, d).
func overlap(a, b, c, d time.Time) time.Duration {
	if c.After(a) {
		a = c
	}
	if d.Before(b) {
		b = d
	}
	if !b.After(a) {
		return 0
	}
	return b.Sub(a)
}

// chargeInWindow fills in the window and counted parts of a charge: pro rata
// by time over the part of its span inside [since, now), and of that the part
// a usage basis held.
func chargeInWindow(c WindowCharge, since, now time.Time, bases []CredentialBasis) WindowCharge {
	whole := c.To.Sub(c.From)
	c.WindowAmount, c.CountedAmount, c.CountedUSD = 0, 0, nil
	if whole <= 0 {
		return c
	}
	in := overlap(c.From, c.To, since, now)
	if in == whole {
		c.WindowAmount = c.Amount
	} else {
		c.WindowAmount = c.Amount * float64(in) / float64(whole)
	}
	var counted time.Duration
	for _, sp := range basisSpans(bases, c.SecretKey, c.SecretSlot, BasisUsage, BasisSeatAndUsage) {
		// The bases of one credential never overlap: the store closes one
		// where the next begins.
		from, to := sp.from, sp.to
		if from.Before(since) {
			from = since
		}
		if to.After(now) {
			to = now
		}
		counted += overlap(c.From, c.To, from, to)
	}
	switch {
	case counted == whole:
		c.CountedAmount = c.Amount
	case counted > 0:
		c.CountedAmount = c.Amount * float64(counted) / float64(whole)
	}
	if c.Currency == "USD" {
		usd := c.CountedAmount
		c.CountedUSD = &usd
	} else if c.FXToUSD != nil && c.FXDate != nil {
		usd := c.CountedAmount * *c.FXToUSD
		c.CountedUSD = &usd
	}
	return c
}

// seatPlans are the plans as they price the meter: a plan prices nothing
// while its credential's basis is usage, so its validity is cut there. With
// no such basis the plans come back as they are, the same slice.
func seatPlans(plans []CredentialPlan, bases []CredentialBasis) []CredentialPlan {
	cut := false
	for _, b := range bases {
		if b.Basis == BasisUsage {
			cut = true
			break
		}
	}
	if !cut {
		return plans
	}
	out := make([]CredentialPlan, 0, len(plans))
	for _, p := range plans {
		pieces := []span{{p.ValidFrom, endOf(p.ValidTo)}}
		for _, u := range basisSpans(bases, p.SecretKey, p.SecretSlot, BasisUsage) {
			var next []span
			for _, sp := range pieces {
				if !u.from.Before(sp.to) || !u.to.After(sp.from) {
					next = append(next, sp)
					continue
				}
				if sp.from.Before(u.from) {
					next = append(next, span{sp.from, u.from})
				}
				if u.to.Before(sp.to) {
					next = append(next, span{u.to, sp.to})
				}
			}
			pieces = next
		}
		for _, sp := range pieces {
			q := p
			q.ValidFrom = sp.from
			q.ValidTo = nil
			if !sp.to.Equal(forever) {
				to := sp.to
				q.ValidTo = &to
			}
			out = append(out, q)
		}
	}
	return out
}
