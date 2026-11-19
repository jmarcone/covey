package runtimes

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// --- The monthly money limit of a pay-per-token seat (migration 0152) ---
//
// An Enterprise seat billed per token against a monthly limit reports its
// meter in band as a window of its own ("overage", reset on the first of the
// month), utilisation 0..1 of the limit. With the limit stored here, a point
// of that window is Amount/100 in Currency: the seat's real spend is
// Δutilisation × limit, counted where the credential's basis is usage
// (0151). Dated and keyed on the secret value like the plans and the bases;
// a change adds a row and closes the one before, and no row is overwritten.

// DefaultLimitWindow is the window Claude Code names a monthly usage limit by.
const DefaultLimitWindow = "overage"

// UsageLimit is one dated limit of a credential.
type UsageLimit struct {
	RuntimeID  *uuid.UUID `json:"runtime_id"`
	Ord        *int       `json:"ord"`
	SecretKey  string     `json:"secret_key"`
	SecretSlot int        `json:"secret_slot"`
	// WindowKind is the meter window the limit reads (overage). A credential
	// has one limit at a time: a new one closes the one before, whatever
	// window it named.
	WindowKind string `json:"window_kind"`
	// Amount per month in Currency; FXToUSD/FXDate a rate a person gave and
	// its day, both or neither, never on USD.
	Amount    float64    `json:"amount"`
	Currency  string     `json:"currency"`
	FXToUSD   *float64   `json:"fx_to_usd"`
	FXDate    *string    `json:"fx_date"`
	ValidFrom time.Time  `json:"valid_from"`
	ValidTo   *time.Time `json:"valid_to"`
}

// ErrLimitExists: a limit of this credential already starts at that moment.
var ErrLimitExists = errors.New("a usage limit of this credential already starts at that moment")

var limitWindow = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Validate normalises a limit and refuses what cannot price a point.
func (l *UsageLimit) Validate() error {
	l.WindowKind = strings.TrimSpace(l.WindowKind)
	if l.WindowKind == "" {
		l.WindowKind = DefaultLimitWindow
	}
	if !limitWindow.MatchString(l.WindowKind) {
		return InvalidCost{"window_kind must be a meter window name such as overage"}
	}
	if strings.HasPrefix(l.WindowKind, "seven_day") || strings.HasPrefix(l.WindowKind, "five_hour") {
		return InvalidCost{"the weekly and five-hour windows are a seat meter's: a plan prices them, not a usage limit"}
	}
	// The same rules as a charge for amount, currency and rate.
	c := UsageCharge{Amount: l.Amount, Currency: l.Currency, From: time.Unix(0, 0), To: time.Unix(1, 0),
		FXToUSD: l.FXToUSD, FXDate: l.FXDate}
	if err := c.Validate(); err != nil {
		return err
	}
	if !(l.Amount > 0) || math.IsInf(l.Amount, 0) {
		return InvalidCost{"amount must be above zero"}
	}
	l.Currency, l.FXToUSD, l.FXDate = c.Currency, c.FXToUSD, c.FXDate
	return nil
}

const limitColumns = `runtime_id, ord, secret_key, secret_slot, window_kind, amount::float8, currency,
	fx_to_usd::float8, to_char(fx_date, 'YYYY-MM-DD'), valid_from, valid_to`

func scanLimit(row pgx.Row) (UsageLimit, error) {
	var l UsageLimit
	var o *int16
	if err := row.Scan(&l.RuntimeID, &o, &l.SecretKey, &l.SecretSlot, &l.WindowKind, &l.Amount, &l.Currency,
		&l.FXToUSD, &l.FXDate, &l.ValidFrom, &l.ValidTo); err != nil {
		return UsageLimit{}, err
	}
	if o != nil {
		v := int(*o)
		l.Ord = &v
	}
	return l, nil
}

// UsageLimits returns the limit history of the credential at a place, newest
// first. It follows the secret value.
func (s *Store) UsageLimits(ctx context.Context, orgID, runtimeID uuid.UUID, ord int) ([]UsageLimit, error) {
	key, slot, err := credentialSecret(ctx, s.pool, orgID, runtimeID, ord)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+limitColumns+` FROM credential_usage_limits
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 ORDER BY valid_from DESC`, orgID, key, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageLimit{}
	for rows.Next() {
		l, err := scanLimit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetUsageLimit appends a limit to the credential at a place, valid from
// l.ValidFrom (now when zero), the way SetCostBasis appends a basis: the
// limit valid then is closed there, a later one bounds the new one, and one
// starting at the same moment is ErrLimitExists.
func (s *Store) SetUsageLimit(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, l UsageLimit) (UsageLimit, error) {
	if err := l.Validate(); err != nil {
		return UsageLimit{}, err
	}
	if l.ValidFrom.IsZero() {
		l.ValidFrom = time.Now()
	}
	l.ValidFrom, l.ValidTo = l.ValidFrom.UTC().Truncate(time.Microsecond), nil
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UsageLimit{}, err
	}
	defer tx.Rollback(ctx)
	key, slot, err := credentialSecret(ctx, tx, orgID, runtimeID, ord)
	if err != nil {
		return UsageLimit{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('limit/' || $1::uuid::text || '/' || $2::text || '#' || $3::int::text))`,
		orgID, key, slot); err != nil {
		return UsageLimit{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM credential_usage_limits
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND valid_from = $4)`,
		orgID, key, slot, l.ValidFrom).Scan(&exists); err != nil {
		return UsageLimit{}, err
	}
	if exists {
		return UsageLimit{}, ErrLimitExists
	}
	if err := tx.QueryRow(ctx, `SELECT min(valid_from) FROM credential_usage_limits
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND valid_from > $4`,
		orgID, key, slot, l.ValidFrom).Scan(&l.ValidTo); err != nil {
		return UsageLimit{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE credential_usage_limits SET valid_to = $4
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3
		  AND valid_from < $4 AND (valid_to IS NULL OR valid_to > $4)`,
		orgID, key, slot, l.ValidFrom); err != nil {
		return UsageLimit{}, err
	}
	saved, err := scanLimit(tx.QueryRow(ctx, `INSERT INTO credential_usage_limits (org_id, runtime_id, ord, secret_key,
		secret_slot, window_kind, amount, currency, fx_to_usd, fx_date, valid_from, valid_to)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11,$12)
		RETURNING `+limitColumns,
		orgID, runtimeID, ord, key, slot, l.WindowKind, l.Amount, l.Currency, l.FXToUSD, l.FXDate, l.ValidFrom, l.ValidTo))
	if err != nil {
		return UsageLimit{}, err
	}
	return saved, tx.Commit(ctx)
}
