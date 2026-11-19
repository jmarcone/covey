package runtimes

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// --- How a credential's real money is formed (migration 0151, #120) ---
//
// Chosen per credential: by its seat price (the plans above price the
// subscription meter), by usage charges, or by both. Dated like the plans and
// kept on the secret value: a change adds a row and closes the one before, and
// no row is ever overwritten. A credential without a row keeps the behaviour
// from before 0151.
//
// Usage charges are money charged for what was used over a span, entered by
// hand: an invoice, or a figure measured until the real one is known. Each
// stays in its currency. A dollar figure exists only where a person gave a
// rate and its date; covey never converts on its own.

// Cost bases.
const (
	BasisSeat         = "seat"
	BasisUsage        = "usage"
	BasisSeatAndUsage = "seat_and_usage"
)

// CostBasis is one dated basis of a credential.
type CostBasis struct {
	// RuntimeID/Ord: where the basis was entered (nil once that is gone).
	RuntimeID  *uuid.UUID `json:"runtime_id"`
	Ord        *int       `json:"ord"`
	SecretKey  string     `json:"secret_key"`
	SecretSlot int        `json:"secret_slot"`
	// Basis: seat | usage | seat_and_usage.
	Basis string `json:"basis"`
	// Valid over [ValidFrom, ValidTo); ValidTo nil while it is the current one.
	ValidFrom time.Time  `json:"valid_from"`
	ValidTo   *time.Time `json:"valid_to"`
}

// ErrBasisExists: a basis of this credential already starts at that moment.
var ErrBasisExists = errors.New("a cost basis of this credential already starts at that moment")

// InvalidCost names a field a basis or a usage charge cannot carry.
type InvalidCost struct{ Reason string }

func (e InvalidCost) Error() string { return "invalid cost: " + e.Reason }

// ValidBasis reports whether b is one of the three bases.
func ValidBasis(b string) bool {
	return b == BasisSeat || b == BasisUsage || b == BasisSeatAndUsage
}

// CostBases returns the basis history of the credential at a place, newest
// first. It follows the secret value, like the plans.
func (s *Store) CostBases(ctx context.Context, orgID, runtimeID uuid.UUID, ord int) ([]CostBasis, error) {
	key, slot, err := credentialSecret(ctx, s.pool, orgID, runtimeID, ord)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT runtime_id, ord, secret_key, secret_slot, basis, valid_from, valid_to
		FROM credential_cost_bases WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3
		ORDER BY valid_from DESC`, orgID, key, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CostBasis{}
	for rows.Next() {
		var b CostBasis
		var o *int16
		if err := rows.Scan(&b.RuntimeID, &o, &b.SecretKey, &b.SecretSlot, &b.Basis, &b.ValidFrom, &b.ValidTo); err != nil {
			return nil, err
		}
		if o != nil {
			v := int(*o)
			b.Ord = &v
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetCostBasis appends a basis to the credential at a place, valid from
// `from` (now when zero), the way SetPlan appends a price: the basis valid at
// that moment is closed there, one that starts later bounds the new one, and
// a basis starting at the same moment is ErrBasisExists. The basis already in
// effect at `from` comes back as it is, and nothing is stored.
func (s *Store) SetCostBasis(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, basis string, from time.Time) (CostBasis, error) {
	if !ValidBasis(basis) {
		return CostBasis{}, InvalidCost{`basis must be "seat", "usage" or "seat_and_usage"`}
	}
	if from.IsZero() {
		from = time.Now()
	}
	b := CostBasis{Basis: basis, ValidFrom: from.UTC().Truncate(time.Microsecond)}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CostBasis{}, err
	}
	defer tx.Rollback(ctx)
	key, slot, err := credentialSecret(ctx, tx, orgID, runtimeID, ord)
	if err != nil {
		return CostBasis{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('basis/' || $1::uuid::text || '/' || $2::text || '#' || $3::int::text))`,
		orgID, key, slot); err != nil {
		return CostBasis{}, err
	}
	// The same basis as the one in effect then: nothing changes, and no row
	// is added — a second row would read as a change of basis that is none.
	var cur CostBasis
	var curOrd *int16
	err = tx.QueryRow(ctx, `SELECT runtime_id, ord, secret_key, secret_slot, basis, valid_from, valid_to
		FROM credential_cost_bases WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3
		  AND valid_from <= $4 AND (valid_to IS NULL OR valid_to > $4)`, orgID, key, slot, b.ValidFrom).
		Scan(&cur.RuntimeID, &curOrd, &cur.SecretKey, &cur.SecretSlot, &cur.Basis, &cur.ValidFrom, &cur.ValidTo)
	switch {
	case err == nil && cur.Basis == basis:
		if curOrd != nil {
			v := int(*curOrd)
			cur.Ord = &v
		}
		return cur, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return CostBasis{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM credential_cost_bases
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND valid_from = $4)`,
		orgID, key, slot, b.ValidFrom).Scan(&exists); err != nil {
		return CostBasis{}, err
	}
	if exists {
		return CostBasis{}, ErrBasisExists
	}
	if err := tx.QueryRow(ctx, `SELECT min(valid_from) FROM credential_cost_bases
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND valid_from > $4`,
		orgID, key, slot, b.ValidFrom).Scan(&b.ValidTo); err != nil {
		return CostBasis{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE credential_cost_bases SET valid_to = $4
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3
		  AND valid_from < $4 AND (valid_to IS NULL OR valid_to > $4)`,
		orgID, key, slot, b.ValidFrom); err != nil {
		return CostBasis{}, err
	}
	b.RuntimeID, b.Ord, b.SecretKey, b.SecretSlot = &runtimeID, &ord, key, slot
	if _, err := tx.Exec(ctx, `INSERT INTO credential_cost_bases (org_id, runtime_id, ord, secret_key, secret_slot,
		basis, valid_from, valid_to) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		orgID, runtimeID, ord, key, slot, b.Basis, b.ValidFrom, b.ValidTo); err != nil {
		return CostBasis{}, err
	}
	return b, tx.Commit(ctx)
}

// UsageCharge is money charged for a credential's usage over [From, To),
// in its own currency.
type UsageCharge struct {
	ID         uuid.UUID  `json:"id"`
	RuntimeID  *uuid.UUID `json:"runtime_id"`
	Ord        *int       `json:"ord"`
	SecretKey  string     `json:"secret_key"`
	SecretSlot int        `json:"secret_slot"`
	Amount     float64    `json:"amount"`
	Currency   string     `json:"currency"`
	From       time.Time  `json:"from"`
	To         time.Time  `json:"to"`
	// FXToUSD/FXDate: the rate a person gave and the day it is from, both or
	// neither; never set on a USD charge. Without them a charge in another
	// currency has no dollar figure.
	FXToUSD   *float64  `json:"fx_to_usd"`
	FXDate    *string   `json:"fx_date"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// The bounds of what the columns of 0151 hold: amount NUMERIC(14,4),
// fx_to_usd NUMERIC(12,6) and > 0.
const (
	MaxChargeAmount = 9999999999.9999
	MinFXToUSD      = 0.000001
	MaxFXToUSD      = 999999.999999
)

var minFXDate = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrChargeOverlaps: another usage charge of the credential already covers
// part of the span. Two charges over the same time would be the same bill
// counted twice, whatever their currencies.
var ErrChargeOverlaps = errors.New("another usage charge of this credential already covers part of that span: delete it or enter the span next to it")

// Validate normalises a charge and refuses what cannot be counted.
func (c *UsageCharge) Validate() error {
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	if len(c.Currency) != 3 || strings.Trim(c.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return InvalidCost{"currency must be a three-letter code"}
	}
	if c.Amount < 0 || math.IsNaN(c.Amount) || math.IsInf(c.Amount, 0) {
		return InvalidCost{"amount must be a non-negative number"}
	}
	// The column is NUMERIC(14,4): a larger amount would reach the database
	// and come back as its error, not as a sentence.
	if c.Amount > MaxChargeAmount {
		return InvalidCost{"amount must be at most 9999999999.9999"}
	}
	if c.From.IsZero() || c.To.IsZero() {
		return InvalidCost{"from and to are required"}
	}
	c.From, c.To = c.From.UTC().Truncate(time.Microsecond), c.To.UTC().Truncate(time.Microsecond)
	if !c.From.Before(c.To) {
		return InvalidCost{"from must lie before to"}
	}
	if c.FXDate != nil && strings.TrimSpace(*c.FXDate) == "" {
		c.FXDate = nil
	}
	if c.FXDate != nil {
		d, err := time.Parse("2006-01-02", *c.FXDate)
		if err != nil {
			return InvalidCost{"fx_date must be YYYY-MM-DD"}
		}
		// A rate is from a day that was: not before 2000, not after tomorrow
		// (a day ahead for the time zones east of UTC).
		if d.Before(minFXDate) || d.After(time.Now().UTC().Truncate(24*time.Hour).Add(24*time.Hour)) {
			return InvalidCost{"fx_date must lie between 2000-01-01 and tomorrow"}
		}
	}
	if c.FXToUSD != nil {
		// NUMERIC(12,6): a rate below 0.000001 would be stored as 0, one of a
		// million or more would not fit.
		if r := *c.FXToUSD; math.IsNaN(r) || r < MinFXToUSD || r > MaxFXToUSD {
			return InvalidCost{"fx_to_usd must be a rate between 0.000001 and 999999.999999"}
		}
	}
	if c.Currency == "USD" {
		// A dollar amount needs no rate: one given is either 1 or wrong.
		if c.FXToUSD != nil && *c.FXToUSD != 1 {
			return InvalidCost{"a USD charge converts at 1"}
		}
		c.FXToUSD, c.FXDate = nil, nil
	} else if (c.FXToUSD == nil) != (c.FXDate == nil) {
		return InvalidCost{"a rate needs its date: give fx_to_usd and fx_date together, or neither"}
	}
	c.Note = strings.TrimSpace(c.Note)
	if len(c.Note) > 2000 {
		return InvalidCost{"note is too long"}
	}
	return nil
}

const chargeColumns = `id, runtime_id, ord, secret_key, secret_slot, amount::float8, currency, period_from, period_to,
	fx_to_usd::float8, to_char(fx_date, 'YYYY-MM-DD'), note, created_at`

func scanCharge(row pgx.Row) (UsageCharge, error) {
	var c UsageCharge
	var o *int16
	if err := row.Scan(&c.ID, &c.RuntimeID, &o, &c.SecretKey, &c.SecretSlot, &c.Amount, &c.Currency, &c.From, &c.To,
		&c.FXToUSD, &c.FXDate, &c.Note, &c.CreatedAt); err != nil {
		return UsageCharge{}, err
	}
	if o != nil {
		v := int(*o)
		c.Ord = &v
	}
	return c, nil
}

// UsageCharges returns the charges of the credential at a place, latest span
// first. It follows the secret value.
func (s *Store) UsageCharges(ctx context.Context, orgID, runtimeID uuid.UUID, ord int) ([]UsageCharge, error) {
	key, slot, err := credentialSecret(ctx, s.pool, orgID, runtimeID, ord)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+chargeColumns+` FROM credential_usage_charges
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3
		ORDER BY period_from DESC, created_at DESC`, orgID, key, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageCharge{}
	for rows.Next() {
		c, err := scanCharge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddUsageCharge stores a charge on the credential at a place.
func (s *Store) AddUsageCharge(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, c UsageCharge) (UsageCharge, error) {
	if err := c.Validate(); err != nil {
		return UsageCharge{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UsageCharge{}, err
	}
	defer tx.Rollback(ctx)
	key, slot, err := credentialSecret(ctx, tx, orgID, runtimeID, ord)
	if err != nil {
		return UsageCharge{}, err
	}
	// One credential's charges never overlap: two over the same time are one
	// bill entered twice (in any currency), and both would count. The lock
	// keeps two concurrent adds from each finding the other absent.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('charge/' || $1::uuid::text || '/' || $2::text || '#' || $3::int::text))`,
		orgID, key, slot); err != nil {
		return UsageCharge{}, err
	}
	var overlaps bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM credential_usage_charges
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND period_from < $5 AND period_to > $4)`,
		orgID, key, slot, c.From, c.To).Scan(&overlaps); err != nil {
		return UsageCharge{}, err
	}
	if overlaps {
		return UsageCharge{}, ErrChargeOverlaps
	}
	saved, err := scanCharge(tx.QueryRow(ctx, `INSERT INTO credential_usage_charges (id, org_id, runtime_id, ord,
		secret_key, secret_slot, amount, currency, period_from, period_to, fx_to_usd, fx_date, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::date,$13)
		RETURNING `+chargeColumns,
		uuid.New(), orgID, runtimeID, ord, key, slot, c.Amount, c.Currency, c.From, c.To, c.FXToUSD, c.FXDate, c.Note))
	if err != nil {
		return UsageCharge{}, err
	}
	return saved, tx.Commit(ctx)
}

// DeleteUsageCharge removes a charge of the credential at a place. A charge
// of another credential (or organisation) is not found.
func (s *Store) DeleteUsageCharge(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, id uuid.UUID) error {
	key, slot, err := credentialSecret(ctx, s.pool, orgID, runtimeID, ord)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM credential_usage_charges
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND id = $4`, orgID, key, slot, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
