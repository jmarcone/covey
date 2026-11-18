package observability

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"covey/internal/billing"
	"covey/internal/daemon"
)

// Billed money (migration 0146): what Anthropic actually charged a metered
// credential, as its Usage and Cost Analytics API reports it, next to what
// Claude Code reported and what covey's list recomputes from the same tokens.
//
// Three figures, three sources, never mixed:
//
//   - reported: Σ costUSD of the per-line deltas in model_usage (Claude Code's
//     own price of the work);
//   - list: Σ tokens × covey's list (listprice.go) — the check on the first;
//   - billed: Σ amount_cents / 100 of the billed rows of the API key the
//     credential's value IS (credential_billing_keys), and billed_list the
//     list price Anthropic computed for them.
//
// residual = billed_list − list: what Anthropic priced that covey's tokens do
// not explain (other use of the same key, a price the list does not know,
// tokens the lines missed). discount = billed_list − billed.
//
// Money stays NUMERIC from the API's decimal string to the JSON string the
// endpoint writes. A float never holds a billed amount.

// ErrInvalidBillingKey: the mapping cannot be made as asked.
type ErrInvalidBillingKey struct{ Reason string }

func (e ErrInvalidBillingKey) Error() string { return "invalid billing key: " + e.Reason }

// apiKeyID is what an Anthropic API key id looks like (apikey_01…).
var apiKeyID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// ReplaceBilled replaces what one read covered — the window of width `width`
// — with what it returned, in one transaction: the window's rows go, the new
// set comes. A read that has not landed (empty, data_refreshed_at null)
// replaces nothing: "nothing yet" is not "zero". A nil result leaves its table
// alone.
//
// A row outside the window it was read for is an error, not something to
// drop: the replace is only right if the window is exactly what was read.
func (s *Store) ReplaceBilled(ctx context.Context, orgID uuid.UUID, width string, cost *billing.CostResult,
	usage *billing.UsageResult, fetchedAt time.Time) error {
	if width != billing.Hour && width != billing.Day {
		return fmt.Errorf("billed rows: unknown bucket width %q", width)
	}
	// Every billed figure is a sum of amount_cents read as US cents. A row in
	// another currency would be added to them as if it were dollars, so a read
	// with one is refused whole, before anything is replaced.
	if cost != nil && cost.Landed {
		for i, r := range cost.Rows {
			cur := strings.ToUpper(strings.TrimSpace(r.Currency))
			if cur != "USD" {
				return fmt.Errorf("billed cost row at %s is in %q: covey sums US dollars only, nothing of this read is stored",
					r.Start.UTC().Format(time.RFC3339), r.Currency)
			}
			cost.Rows[i].Currency = cur
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	inside := func(start time.Time, w billing.Window) bool {
		return !start.Before(w.Start) && start.Before(w.End)
	}
	if cost != nil && cost.Landed {
		if _, err := tx.Exec(ctx, `DELETE FROM billed_cost WHERE org_id = $1 AND bucket_width = $2
			AND bucket_start >= $3 AND bucket_start < $4`, orgID, width, cost.Window.Start, cost.Window.End); err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, r := range cost.Rows {
			if !inside(r.Start, cost.Window) {
				return fmt.Errorf("billed cost row at %s outside the read window %s..%s", r.Start, cost.Window.Start, cost.Window.End)
			}
			// Two rows on the same stored key are parts of one combination
			// (dimensions the API reports as null are stored as ''): summed.
			b.Queue(`INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, workspace_id,
				model, service_tier, cost_type, token_type, amount_cents, list_amount_cents, currency, data_refreshed_at, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12::numeric,$13,$14,$15)
				ON CONFLICT (org_id, bucket_width, bucket_start, api_key_id, workspace_id, model, service_tier, cost_type, token_type)
				DO UPDATE SET amount_cents = billed_cost.amount_cents + EXCLUDED.amount_cents,
					list_amount_cents = billed_cost.list_amount_cents + EXCLUDED.list_amount_cents`,
				orgID, width, r.Start, r.End, r.Group.APIKeyID, r.Group.WorkspaceID, r.Group.Model, r.Group.ServiceTier,
				r.Group.CostType, r.Group.TokenType, r.AmountCents, r.ListAmountCents, r.Currency, cost.DataRefreshedAt, fetchedAt)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	if usage != nil && usage.Landed {
		if _, err := tx.Exec(ctx, `DELETE FROM billed_usage WHERE org_id = $1 AND bucket_width = $2
			AND bucket_start >= $3 AND bucket_start < $4`, orgID, width, usage.Window.Start, usage.Window.End); err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, r := range usage.Rows {
			if !inside(r.Start, usage.Window) {
				return fmt.Errorf("billed usage row at %s outside the read window %s..%s", r.Start, usage.Window.Start, usage.Window.End)
			}
			b.Queue(`INSERT INTO billed_usage (org_id, bucket_width, bucket_start, bucket_end, api_key_id, workspace_id,
				model, service_tier, uncached_input, cache_write_5m, cache_write_1h, cache_read, output,
				web_search_requests, requests, data_refreshed_at, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
				ON CONFLICT (org_id, bucket_width, bucket_start, api_key_id, workspace_id, model, service_tier)
				DO UPDATE SET uncached_input = billed_usage.uncached_input + EXCLUDED.uncached_input,
					cache_write_5m = billed_usage.cache_write_5m + EXCLUDED.cache_write_5m,
					cache_write_1h = billed_usage.cache_write_1h + EXCLUDED.cache_write_1h,
					cache_read = billed_usage.cache_read + EXCLUDED.cache_read,
					output = billed_usage.output + EXCLUDED.output,
					web_search_requests = billed_usage.web_search_requests + EXCLUDED.web_search_requests,
					requests = billed_usage.requests + EXCLUDED.requests`,
				orgID, width, r.Start, r.End, r.Group.APIKeyID, r.Group.WorkspaceID, r.Group.Model, r.Group.ServiceTier,
				r.UncachedInput, r.CacheWrite5m, r.CacheWrite1h, r.CacheRead, r.Output, r.WebSearchRequests, r.Requests,
				usage.DataRefreshedAt, fetchedAt)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// BillingSyncStatus is the poller's state for one organisation.
type BillingSyncStatus struct {
	// Configured: an anthropic_admin_key is stored (set by the caller, which
	// knows the secret store; this package does not).
	Configured      bool       `json:"configured"`
	LastAttemptAt   *time.Time `json:"last_attempt_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	LastDailyAt     *time.Time `json:"last_daily_at"`
	DataRefreshedAt *time.Time `json:"data_refreshed_at"`
	// LastStatus: ok | not_landed | failed, empty before the first attempt.
	LastStatus string `json:"last_status"`
	// LastError has no secret in it: the client's errors carry a status and a
	// scrubbed piece of the body.
	LastError string `json:"last_error"`
}

// BillingSyncAttempt is what one sync of an organisation ended with.
type BillingSyncAttempt struct {
	At              time.Time
	Status          string // ok | not_landed | failed
	Error           string
	DataRefreshedAt *time.Time
	// Daily: the 35-day re-read was part of it and succeeded.
	Daily bool
}

// NoteBillingSync records an attempt. A failure keeps the last success and
// its data_refreshed_at — the rows they describe are still there.
func (s *Store) NoteBillingSync(ctx context.Context, orgID uuid.UUID, a BillingSyncAttempt) error {
	ok := a.Status != "failed"
	if len(a.Error) > 1000 {
		a.Error = a.Error[:1000]
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO billing_sync (org_id, last_attempt_at, last_success_at, last_daily_at,
			data_refreshed_at, last_status, last_error)
		VALUES ($1, $2, CASE WHEN $3 THEN $2::timestamptz END, CASE WHEN $4 THEN $2::timestamptz END, $5, $6, $7)
		ON CONFLICT (org_id) DO UPDATE SET
			last_attempt_at = $2,
			last_success_at = CASE WHEN $3 THEN $2::timestamptz ELSE billing_sync.last_success_at END,
			last_daily_at = CASE WHEN $4 THEN $2::timestamptz ELSE billing_sync.last_daily_at END,
			data_refreshed_at = COALESCE($5, billing_sync.data_refreshed_at),
			last_status = $6, last_error = $7`,
		orgID, a.At, ok, ok && a.Daily, a.DataRefreshedAt, a.Status, a.Error)
	return err
}

// BillingSync reads the poller's state; the zero status before the first run.
func (s *Store) BillingSync(ctx context.Context, orgID uuid.UUID) (BillingSyncStatus, error) {
	var st BillingSyncStatus
	err := s.pool.QueryRow(ctx, `SELECT last_attempt_at, last_success_at, last_daily_at, data_refreshed_at,
		last_status, last_error FROM billing_sync WHERE org_id = $1`, orgID).Scan(
		&st.LastAttemptAt, &st.LastSuccessAt, &st.LastDailyAt, &st.DataRefreshedAt, &st.LastStatus, &st.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return BillingSyncStatus{}, nil
	}
	return st, err
}

// BillingKeyMapping says which secret value is which Anthropic API key.
type BillingKeyMapping struct {
	SecretKey   string     `json:"secret_key"`
	SecretSlot  int        `json:"secret_slot"`
	APIKeyID    string     `json:"api_key_id"`
	WorkspaceID string     `json:"workspace_id"`
	ValidFrom   time.Time  `json:"valid_from"`
	ValidTo     *time.Time `json:"valid_to"`
	Source      string     `json:"source"`
}

// SetBillingKey says that the value behind the credential at a place is the
// API key apiKeyID (optionally only within workspaceID). Valid from when the
// value was written: the value IS that key for its whole life, so a mapping
// entered a week later still covers the week. A correction of the open
// mapping of the same value replaces it; a new value (the trigger of 0146
// closed the old mapping) starts a new one. Only an API-key credential is
// billed per API key; a subscription seat has no api_key_id. An API key that
// another credential is mapped to over any part of the same interval is
// refused (ErrInvalidBillingKey): its bill would be counted twice.
func (s *Store) SetBillingKey(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, apiKey, workspaceID string) (BillingKeyMapping, error) {
	apiKey, workspaceID = strings.TrimSpace(apiKey), strings.TrimSpace(workspaceID)
	if !apiKeyID.MatchString(apiKey) {
		return BillingKeyMapping{}, ErrInvalidBillingKey{Reason: "api_key_id must be an Anthropic API key id such as apikey_01…"}
	}
	if workspaceID != "" && !apiKeyID.MatchString(workspaceID) {
		return BillingKeyMapping{}, ErrInvalidBillingKey{Reason: "workspace_id must be an id such as wrkspc_01…"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BillingKeyMapping{}, err
	}
	defer tx.Rollback(ctx)
	var (
		m       = BillingKeyMapping{APIKeyID: apiKey, WorkspaceID: workspaceID, Source: "manual"}
		kind    string
		written *time.Time
		slot16  int16
	)
	err = tx.QueryRow(ctx, `SELECT rc.secret_key, rc.secret_slot, rc.kind, sec.updated_at
		FROM runtime_credentials rc
		JOIN runtimes r ON r.id = rc.runtime_id
		LEFT JOIN secrets sec ON sec.org_id = r.org_id AND sec.key = rc.secret_key
			AND sec.slot = rc.secret_slot AND sec.agent_id IS NULL
		WHERE r.org_id = $1 AND rc.runtime_id = $2 AND rc.ord = $3`, orgID, runtimeID, ord).Scan(&m.SecretKey, &slot16, &kind, &written)
	if errors.Is(err, pgx.ErrNoRows) {
		return BillingKeyMapping{}, ErrNotFound
	}
	if err != nil {
		return BillingKeyMapping{}, err
	}
	m.SecretSlot = int(slot16)
	if kind != daemon.CredAPIKey {
		return BillingKeyMapping{}, ErrInvalidBillingKey{Reason: "only a metered API-key credential is billed per API key; this one is " + kind}
	}
	if written == nil {
		return BillingKeyMapping{}, ErrInvalidBillingKey{Reason: "the credential's secret value is not stored"}
	}
	m.ValidFrom = written.UTC()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billing-key/' || $1::uuid::text || '/' || $2 || '#' || $3::int::text))`,
		orgID, m.SecretKey, m.SecretSlot); err != nil {
		return BillingKeyMapping{}, err
	}
	// An open mapping of an earlier write (should the trigger not have seen
	// it) ends where this value began.
	if _, err := tx.Exec(ctx, `UPDATE credential_billing_keys SET valid_to = GREATEST($4, valid_from)
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND valid_to IS NULL AND valid_from <> $4`,
		orgID, m.SecretKey, m.SecretSlot, m.ValidFrom); err != nil {
		return BillingKeyMapping{}, err
	}
	// The mapping's interval: from the value's write until the end a mapping
	// of the same write already has (a correction keeps it), else open.
	err = tx.QueryRow(ctx, `SELECT valid_to FROM credential_billing_keys
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND valid_from = $4`,
		orgID, m.SecretKey, m.SecretSlot, m.ValidFrom).Scan(&m.ValidTo)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return BillingKeyMapping{}, err
	}
	// One API key is billed to one credential at a time: a second credential
	// mapped to the same key over an overlapping interval would count every
	// billed cent twice. Serialised per key, so two concurrent mappings of one
	// key to two credentials cannot both pass; the unique index of 0146 holds
	// the open case in the database too.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billing-api-key/' || $1::uuid::text || '/' || $2))`,
		orgID, apiKey); err != nil {
		return BillingKeyMapping{}, err
	}
	var (
		otherKey  string
		otherSlot int16
	)
	err = tx.QueryRow(ctx, `SELECT secret_key, secret_slot FROM credential_billing_keys
		WHERE org_id = $1 AND api_key_id = $2 AND NOT (secret_key = $3 AND secret_slot = $4)
		  AND COALESCE(valid_to, 'infinity'::timestamptz) > $5
		  AND valid_from < COALESCE($6::timestamptz, 'infinity'::timestamptz)
		ORDER BY valid_from DESC LIMIT 1`,
		orgID, apiKey, m.SecretKey, m.SecretSlot, m.ValidFrom, m.ValidTo).Scan(&otherKey, &otherSlot)
	switch {
	case err == nil:
		return BillingKeyMapping{}, ErrInvalidBillingKey{Reason: fmt.Sprintf(
			"%s is already mapped to the credential %s#%d over the same time; an API key is billed to one credential only",
			apiKey, otherKey, otherSlot)}
	case !errors.Is(err, pgx.ErrNoRows):
		return BillingKeyMapping{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id,
			workspace_id, valid_from, source)
		VALUES ($1,$2,$3,$4,$5,$6,'manual')
		ON CONFLICT (org_id, secret_key, secret_slot, valid_from) DO UPDATE SET
			api_key_id = EXCLUDED.api_key_id, workspace_id = EXCLUDED.workspace_id, source = EXCLUDED.source`,
		orgID, m.SecretKey, m.SecretSlot, apiKey, workspaceID, m.ValidFrom); err != nil {
		return BillingKeyMapping{}, err
	}
	return m, tx.Commit(ctx)
}

// BillingKeyMappings lists the organisation's mappings, open ones first.
func (s *Store) BillingKeyMappings(ctx context.Context, orgID uuid.UUID) ([]BillingKeyMapping, error) {
	rows, err := s.pool.Query(ctx, `SELECT secret_key, secret_slot, api_key_id, workspace_id, valid_from, valid_to, source
		FROM credential_billing_keys WHERE org_id = $1
		ORDER BY valid_to IS NOT NULL, secret_key, secret_slot, valid_from DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BillingKeyMapping{}
	for rows.Next() {
		var m BillingKeyMapping
		var slot int16
		if err := rows.Scan(&m.SecretKey, &slot, &m.APIKeyID, &m.WorkspaceID, &m.ValidFrom, &m.ValidTo, &m.Source); err != nil {
			return nil, err
		}
		m.SecretSlot = int(slot)
		out = append(out, m)
	}
	return out, rows.Err()
}

// chosenRows picks, per UTC day, ONE bucket width of a billed table: the one
// read most recently (the 1h read of the last 48 h is fresher than the daily
// re-read, the daily re-read is fresher than an old 1h read), 1d on a tie.
// Both widths hold the same money; summing both would count it twice. $1 is
// the organisation, $2 the start (a UTC midnight).
func chosenRows(table, pick, alias string) string {
	return pick + ` AS (
	SELECT DISTINCT ON (day) day, bucket_width FROM (
		SELECT date_trunc('day', bucket_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS day, bucket_width,
			max(fetched_at) AS fetched
		FROM ` + table + ` WHERE org_id = $1 AND bucket_start >= $2
		GROUP BY 1, 2) x
	ORDER BY day, fetched DESC, bucket_width = '1d' DESC),
` + alias + ` AS (
	SELECT t.*, p.day FROM ` + table + ` t
	JOIN ` + pick + ` p ON p.bucket_width = t.bucket_width
		AND t.bucket_start >= p.day AND t.bucket_start < p.day + interval '24 hours'
	WHERE t.org_id = $1 AND t.bucket_start >= $2)`
}

// priceValues renders the list as a VALUES table, so the list price is
// computed in NUMERIC next to the billed amounts. The literals are the
// table's own decimal figures, not a float's expansion.
func priceValues() string {
	ids := make([]string, 0, len(listPrices))
	for id := range listPrices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "::numeric" }
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		p := listPrices[id]
		parts = append(parts, fmt.Sprintf("('%s', %s, %s, %s, %s, %s)", id, f(p.Input), f(p.Output), f(p.CacheRead),
			f(p.CacheWrite5m), f(p.CacheWrite1h)))
	}
	return `prices (id, input, output, cache_read, w5m, w1h) AS (VALUES ` + strings.Join(parts, ", ") + `)`
}

// normModel is the model id a row is compared under: the list's id where the
// list knows the model (dated or suffixed ids such as "[1m]" resolve to it),
// the lower-cased id without suffix otherwise.
func normModel(col string) string {
	base := `lower(regexp_replace(` + col + `, '\[.*$', ''))`
	return `COALESCE((SELECT p.id FROM prices p WHERE ` + base + ` = p.id OR ` + base + ` LIKE p.id || '-%'
		ORDER BY length(p.id) DESC LIMIT 1), ` + base + `)`
}

// TokenDiff compares one token type: what the lines say against what
// Anthropic counted.
type TokenDiff struct {
	Type     string `json:"type"`
	Reported int64  `json:"reported"`
	Billed   int64  `json:"billed"`
	Diff     int64  `json:"diff"`
}

// ReconcileModel is one UTC day × credential × model. Money is a decimal
// string in US dollars.
type ReconcileModel struct {
	Model string `json:"model"`
	// ReportedUSD: Σ Claude Code's costUSD of the line deltas.
	ReportedUSD string `json:"reported_usd"`
	// ListUSD: Σ tokens × covey's list; Unpriced when the list does not know
	// the model (then ListUSD covers only what it knows).
	ListUSD  string `json:"list_usd"`
	Unpriced bool   `json:"unpriced,omitempty"`
	// BilledListUSD / BilledUSD: Anthropic's list price and its amount after
	// discounts (before credits) for the mapped API key.
	BilledListUSD string `json:"billed_list_usd"`
	BilledUSD     string `json:"billed_usd"`
	// ResidualUSD = billed_list − list; DiscountUSD = billed_list − billed.
	ResidualUSD string `json:"residual_usd"`
	DiscountUSD string `json:"discount_usd"`
	// ResidualLabel reads the residual: match (under a cent), billed_above_list,
	// billed_below_list — or why it cannot be read: incomplete,
	// not_comparable, no_billed_data.
	ResidualLabel string      `json:"residual_label"`
	Tokens        []TokenDiff `json:"tokens"`
	// HasReported / HasBilled: whether either side had any row at all.
	HasReported bool `json:"has_reported"`
	HasBilled   bool `json:"has_billed"`
	// NotComparable: a line or a billed row ran outside standard tier,
	// standard speed and global geography, which list prices do not cover.
	NotComparable       bool   `json:"not_comparable"`
	NotComparableReason string `json:"not_comparable_reason,omitempty"`
}

// ReconcileDay is one UTC day × mapped credential.
type ReconcileDay struct {
	Day         string `json:"day"` // YYYY-MM-DD, UTC
	SecretKey   string `json:"secret_key"`
	SecretSlot  int    `json:"secret_slot"`
	APIKeyID    string `json:"api_key_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Complete: the day ended at or before data_refreshed_at, so Anthropic's
	// figures for it will not grow any more (they can still be revised).
	Complete        bool       `json:"complete"`
	DataRefreshedAt *time.Time `json:"data_refreshed_at"`
	// RunsWithoutResult counts runs booked on the credential that day without
	// any result line: their spend is in the bill and nowhere on our side.
	RunsWithoutResult int64            `json:"runs_without_result"`
	Models            []ReconcileModel `json:"models"`
}

// Reconciliation is the per-day comparison for every mapped credential.
type Reconciliation struct {
	Since           time.Time      `json:"since"`
	DataRefreshedAt *time.Time     `json:"data_refreshed_at"`
	Days            []ReconcileDay `json:"days"`
}

// matchBelowUSD: a residual under a cent is a match.
const matchBelowUSD = 0.01

// Reconcile compares, per UTC day × mapped credential × model, what Claude
// Code reported, what covey's list makes of the same tokens, and what
// Anthropic billed the API key the credential's value is — inside the
// mapping's interval on both sides. since is moved back to its UTC midnight.
func (s *Store) Reconcile(ctx context.Context, orgID uuid.UUID, since time.Time) (Reconciliation, error) {
	since = since.UTC().Truncate(24 * time.Hour)
	rec := Reconciliation{Since: since, Days: []ReconcileDay{}}
	sync, err := s.BillingSync(ctx, orgID)
	if err != nil {
		return rec, err
	}
	rec.DataRefreshedAt = sync.DataRefreshedAt

	day := func(col string) string {
		return `date_trunc('day', ` + col + ` AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`
	}
	q := `WITH ` + priceValues() + `,
maps AS (
	SELECT secret_key, secret_slot, api_key_id, workspace_id, valid_from,
		COALESCE(valid_to, 'infinity'::timestamptz) AS valid_to
	FROM credential_billing_keys
	WHERE org_id = $1 AND COALESCE(valid_to, 'infinity'::timestamptz) > $2),
` + chosenRows("billed_cost", "cpick", "bcost") + `,
` + chosenRows("billed_usage", "upick", "buse") + `,
ours AS (
	SELECT m.secret_key, m.secret_slot, m.valid_from, ` + day("mu.created_at") + ` AS day, nm.model,
		SUM(mu.engine_usd) AS reported,
		SUM((mu.input_tokens * p.input + mu.output_tokens * p.output + mu.cache_read_tokens * p.cache_read
			+ mu.cache_write_5m_tokens * p.w5m + mu.cache_write_1h_tokens * p.w1h) / 1000000
			+ mu.web_search_requests * ` + strconv.FormatFloat(WebSearchUSD, 'f', -1, 64) + `::numeric) AS list,
		bool_or(p.id IS NULL) AS unpriced,
		SUM(mu.input_tokens) AS t_in, SUM(mu.cache_write_5m_tokens) AS t_5m, SUM(mu.cache_write_1h_tokens) AS t_1h,
		SUM(mu.cache_read_tokens) AS t_read, SUM(mu.output_tokens) AS t_out, SUM(mu.web_search_requests) AS t_web,
		string_agg(DISTINCT CASE WHEN mu.service_tier NOT IN ('', 'standard') THEN 'service_tier ' || mu.service_tier
			WHEN mu.speed NOT IN ('', 'standard') THEN 'speed ' || mu.speed
			WHEN mu.inference_geo NOT IN ('', 'global') THEN 'inference_geo ' || mu.inference_geo END, ', ') AS odd
	FROM maps m
	JOIN model_usage mu ON mu.org_id = $1 AND mu.secret_key = m.secret_key AND mu.secret_slot = m.secret_slot
		AND mu.created_at >= m.valid_from AND mu.created_at < m.valid_to AND mu.created_at >= $2
	CROSS JOIN LATERAL (SELECT ` + normModel("mu.model") + ` AS model) nm
	LEFT JOIN prices p ON p.id = nm.model
	GROUP BY 1, 2, 3, 4, 5),
billed AS (
	SELECT m.secret_key, m.secret_slot, m.valid_from, c.day, nm.model,
		SUM(c.amount_cents) / 100 AS billed, SUM(c.list_amount_cents) / 100 AS billed_list,
		string_agg(DISTINCT CASE WHEN c.service_tier NOT IN ('', 'standard') THEN 'billed service_tier ' || c.service_tier END, ', ') AS odd,
		max(c.data_refreshed_at) AS refreshed
	FROM maps m
	JOIN bcost c ON c.api_key_id = m.api_key_id AND (m.workspace_id = '' OR c.workspace_id = m.workspace_id)
		AND c.bucket_end > m.valid_from AND c.bucket_start < m.valid_to
	CROSS JOIN LATERAL (SELECT ` + normModel("c.model") + ` AS model) nm
	GROUP BY 1, 2, 3, 4, 5),
btok AS (
	SELECT m.secret_key, m.secret_slot, m.valid_from, u.day, nm.model,
		SUM(u.uncached_input) AS t_in, SUM(u.cache_write_5m) AS t_5m, SUM(u.cache_write_1h) AS t_1h,
		SUM(u.cache_read) AS t_read, SUM(u.output) AS t_out, SUM(u.web_search_requests) AS t_web,
		max(u.data_refreshed_at) AS refreshed
	FROM maps m
	JOIN buse u ON u.api_key_id = m.api_key_id AND (m.workspace_id = '' OR u.workspace_id = m.workspace_id)
		AND u.bucket_end > m.valid_from AND u.bucket_start < m.valid_to
	CROSS JOIN LATERAL (SELECT ` + normModel("u.model") + ` AS model) nm
	GROUP BY 1, 2, 3, 4, 5),
keys AS (
	SELECT secret_key, secret_slot, valid_from, day, model FROM ours
	UNION SELECT secret_key, secret_slot, valid_from, day, model FROM billed
	UNION SELECT secret_key, secret_slot, valid_from, day, model FROM btok),
noresult AS (
	SELECT m.secret_key, m.secret_slot, m.valid_from, ` + day("ce.created_at") + ` AS day, COUNT(*) AS n
	FROM maps m
	JOIN cost_entries ce ON ce.secret_key = m.secret_key AND ce.secret_slot = m.secret_slot
		AND ce.created_at >= m.valid_from AND ce.created_at < m.valid_to AND ce.created_at >= $2
	JOIN agents a ON a.id = ce.agent_id AND a.org_id = $1
	WHERE NOT EXISTS (SELECT 1 FROM model_usage l WHERE l.cost_entry_id = ce.id)
	GROUP BY 1, 2, 3, 4)
SELECT k.secret_key, k.secret_slot, m.api_key_id, m.workspace_id, k.day, k.model,
	o.secret_key IS NOT NULL, b.secret_key IS NOT NULL OR t.secret_key IS NOT NULL,
	round(COALESCE(o.reported, 0), 8)::text, round(COALESCE(o.list, 0), 8)::text, COALESCE(o.unpriced, false),
	round(COALESCE(b.billed_list, 0), 8)::text, round(COALESCE(b.billed, 0), 8)::text,
	round(COALESCE(b.billed_list, 0) - COALESCE(o.list, 0), 8)::text,
	round(COALESCE(b.billed_list, 0) - COALESCE(b.billed, 0), 8)::text,
	(COALESCE(b.billed_list, 0) - COALESCE(o.list, 0))::float8,
	COALESCE(o.t_in, 0), COALESCE(o.t_5m, 0), COALESCE(o.t_1h, 0), COALESCE(o.t_read, 0), COALESCE(o.t_out, 0), COALESCE(o.t_web, 0),
	COALESCE(t.t_in, 0), COALESCE(t.t_5m, 0), COALESCE(t.t_1h, 0), COALESCE(t.t_read, 0), COALESCE(t.t_out, 0), COALESCE(t.t_web, 0),
	concat_ws(', ', NULLIF(o.odd, ''), NULLIF(b.odd, '')),
	GREATEST(b.refreshed, t.refreshed),
	COALESCE(n.n, 0)
FROM keys k
JOIN maps m ON m.secret_key = k.secret_key AND m.secret_slot = k.secret_slot AND m.valid_from = k.valid_from
LEFT JOIN ours o ON o.secret_key = k.secret_key AND o.secret_slot = k.secret_slot AND o.valid_from = k.valid_from
	AND o.day = k.day AND o.model = k.model
LEFT JOIN billed b ON b.secret_key = k.secret_key AND b.secret_slot = k.secret_slot AND b.valid_from = k.valid_from
	AND b.day = k.day AND b.model = k.model
LEFT JOIN btok t ON t.secret_key = k.secret_key AND t.secret_slot = k.secret_slot AND t.valid_from = k.valid_from
	AND t.day = k.day AND t.model = k.model
LEFT JOIN noresult n ON n.secret_key = k.secret_key AND n.secret_slot = k.secret_slot AND n.valid_from = k.valid_from
	AND n.day = k.day
ORDER BY k.day, k.secret_key, k.secret_slot, k.valid_from, k.model`
	rows, err := s.pool.Query(ctx, q, orgID, since)
	if err != nil {
		return rec, err
	}
	defer rows.Close()
	// One credential can have two mappings on one day (its value was
	// replaced): they are two entries, each with its own api key.
	type dayKey struct {
		day         time.Time
		key, apiKey string
		slot        int
	}
	index := map[dayKey]int{}
	for rows.Next() {
		var (
			m                 ReconcileModel
			secretKey, apiKey string
			workspace         string
			slot              int16
			d                 time.Time
			residual          float64
			o, t              [6]int64
			odd               string
			refreshed         *time.Time
			noResult          int64
		)
		if err := rows.Scan(&secretKey, &slot, &apiKey, &workspace, &d, &m.Model, &m.HasReported, &m.HasBilled,
			&m.ReportedUSD, &m.ListUSD, &m.Unpriced, &m.BilledListUSD, &m.BilledUSD, &m.ResidualUSD, &m.DiscountUSD, &residual,
			&o[0], &o[1], &o[2], &o[3], &o[4], &o[5], &t[0], &t[1], &t[2], &t[3], &t[4], &t[5],
			&odd, &refreshed, &noResult); err != nil {
			return rec, err
		}
		for i, name := range []string{"uncached_input", "cache_write_5m", "cache_write_1h", "cache_read", "output", "web_search_requests"} {
			m.Tokens = append(m.Tokens, TokenDiff{Type: name, Reported: o[i], Billed: t[i], Diff: t[i] - o[i]})
		}
		if odd != "" {
			m.NotComparable, m.NotComparableReason = true, odd
		}
		// The day's completeness: its own rows' data_refreshed_at, or for a day
		// without billed rows the last successful read's.
		if refreshed == nil {
			refreshed = sync.DataRefreshedAt
		}
		d = d.UTC()
		complete := refreshed != nil && !d.Add(24*time.Hour).After(*refreshed)
		switch {
		case m.NotComparable:
			m.ResidualLabel = "not_comparable"
		case !complete:
			m.ResidualLabel = "incomplete"
		case !m.HasBilled:
			m.ResidualLabel = "no_billed_data"
		case residual > -matchBelowUSD && residual < matchBelowUSD:
			m.ResidualLabel = "match"
		case residual > 0:
			m.ResidualLabel = "billed_above_list"
		default:
			m.ResidualLabel = "billed_below_list"
		}
		k := dayKey{day: d, key: secretKey, apiKey: apiKey, slot: int(slot)}
		i, ok := index[k]
		if !ok {
			rec.Days = append(rec.Days, ReconcileDay{Day: d.Format("2006-01-02"), SecretKey: secretKey,
				SecretSlot: int(slot), APIKeyID: apiKey, WorkspaceID: workspace, Complete: complete,
				DataRefreshedAt: refreshed, RunsWithoutResult: noResult, Models: []ReconcileModel{}})
			i = len(rec.Days) - 1
			index[k] = i
		}
		entry := &rec.Days[i]
		entry.Complete = entry.Complete && complete
		if refreshed != nil && (entry.DataRefreshedAt == nil || refreshed.Before(*entry.DataRefreshedAt)) {
			entry.DataRefreshedAt = refreshed
		}
		entry.Models = append(entry.Models, m)
	}
	return rec, rows.Err()
}

// BilledPoint is one bucket × API key × model of the bill.
type BilledPoint struct {
	Bucket      time.Time `json:"bucket"`
	APIKeyID    string    `json:"api_key_id"`
	WorkspaceID string    `json:"workspace_id"`
	Model       string    `json:"model"`
	// SecretKey/SecretSlot: the credential the key was mapped to during the
	// bucket (the latest such mapping when one ended inside it); nil is use
	// of the organisation's account by no covey credential at that time.
	SecretKey     *string `json:"secret_key"`
	SecretSlot    *int    `json:"secret_slot"`
	BilledUSD     string  `json:"billed_usd"`
	BilledListUSD string  `json:"billed_list_usd"`
	// Complete: the bucket ended at or before the rows' data_refreshed_at.
	Complete bool `json:"complete"`
}

// BilledSeries is the bill over time.
type BilledSeries struct {
	Bucket string        `json:"bucket"`
	Points []BilledPoint `json:"points"`
}

// billedMapping joins a bill row c to the ONE mapping it belongs to (mk, NULL
// columns when none): the API key's mapping valid during the row's bucket, the
// latest when one ended inside it. A bucket overlaps two mappings when a value
// is re-saved or a key changes hands inside it; joining every overlapping
// mapping would count it once per mapping. The chart (BilledSeries) and the
// tile (BilledWindowFor) both take it from here, so they cannot disagree on
// whose a bucket is.
const billedMapping = `LEFT JOIN LATERAL (SELECT k.secret_key, k.secret_slot::int AS secret_slot FROM credential_billing_keys k
			WHERE k.org_id = $1 AND k.api_key_id = c.api_key_id AND (k.workspace_id = '' OR k.workspace_id = c.workspace_id)
			  AND k.valid_from < c.bucket_end AND COALESCE(k.valid_to, 'infinity'::timestamptz) > c.bucket_start
			ORDER BY k.valid_from DESC LIMIT 1) mk ON true`

// BilledSeries sums the billed rows per bucket (hour | day | week, UTC; day
// by default), API key and model. Hours come from the 1h reads only; days and
// weeks from one width per day (chosenRows).
func (s *Store) BilledSeries(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time) (BilledSeries, error) {
	out := BilledSeries{Bucket: bucket, Points: []BilledPoint{}}
	since = since.UTC().Truncate(24 * time.Hour)
	var src string
	switch bucket {
	case "hour":
		src = `bcost AS (SELECT t.*, NULL::timestamptz AS day FROM billed_cost t
			WHERE t.org_id = $1 AND t.bucket_width = '1h' AND t.bucket_start >= $2)`
	case "week":
		src = chosenRows("billed_cost", "cpick", "bcost")
	default:
		out.Bucket, bucket = "day", "day"
		src = chosenRows("billed_cost", "cpick", "bcost")
	}
	rows, err := s.pool.Query(ctx, `WITH `+src+`
		SELECT date_trunc('`+bucket+`', c.bucket_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS b,
			c.api_key_id, c.workspace_id, c.model, mk.secret_key, mk.secret_slot,
			round(SUM(c.amount_cents) / 100, 8)::text, round(SUM(c.list_amount_cents) / 100, 8)::text,
			bool_and(c.data_refreshed_at IS NOT NULL AND c.bucket_end <= c.data_refreshed_at)
		FROM bcost c
		`+billedMapping+`
		GROUP BY 1, 2, 3, 4, 5, 6
		ORDER BY 1, 2, 4`, orgID, since)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p BilledPoint
		if err := rows.Scan(&p.Bucket, &p.APIKeyID, &p.WorkspaceID, &p.Model, &p.SecretKey, &p.SecretSlot,
			&p.BilledUSD, &p.BilledListUSD, &p.Complete); err != nil {
			return out, err
		}
		out.Points = append(out.Points, p)
	}
	return out, rows.Err()
}

// billedHourlyReach is how far back the poller re-reads the bill per hour on
// every sync (billingHourlyBack in the orchestrator): inside it the bill is
// known per hour, before it only per UTC day.
const billedHourlyReach = 48 * time.Hour

// BilledWindow is the bill of every mapped credential over ONE window made of
// whole billed buckets, with what Claude Code reported and what covey's list
// recomputes for exactly the same window. The bill is only known per bucket
// (an hour, or a UTC day further back), so a window that starts mid-bucket
// cannot be billed as it stands: the window starts at the first whole bucket
// at or after `since`, and the other two figures are cut to it too, by the
// lines' own timestamps. Comparing a bill over 34 hours with lines over 24
// would invent a gap that is not there.
type BilledWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// Width is the bucket the window is made of: 1h within the hourly reach,
	// 1d before it.
	Width string `json:"width"`
	// ComparedUntil is where the reported and recomputed figures of the
	// totals end: the bill's data_refreshed_at (its data is complete only up
	// to there) when that is before End, else End; Start when no read of the
	// bill has landed yet. Lines after it are not in ReportedUSD/ListUSD — a
	// bill complete to 06:00 beside lines up to now would invent a gap.
	ComparedUntil time.Time     `json:"compared_until"`
	Totals        []BilledTotal `json:"totals"`
}

// BilledTotal is one credential with a mapping to an API key valid in the
// window — also when nothing was billed, so "mapped, nothing billed" can be
// told from "not mapped". A bill bucket counts for the mapping of its time
// (billedMapping), as in the chart; the lines stay inside the mapping's
// interval.
type BilledTotal struct {
	SecretKey  string `json:"secret_key"`
	SecretSlot int    `json:"secret_slot"`
	// HasBilled: a billed row in the window went to this credential.
	HasBilled     bool   `json:"has_billed"`
	BilledUSD     string `json:"billed_usd"`
	BilledListUSD string `json:"billed_list_usd"`
	// ReportedUSD: Σ Claude Code's costUSD of the credential's lines in the
	// window up to ComparedUntil; ListUSD: their tokens at covey's list (a
	// model the list does not know at the engine's figure, as in the cost
	// cells).
	ReportedUSD float64 `json:"reported_usd"`
	ListUSD     float64 `json:"list_usd"`
}

// billedWindowStart is the first whole billed bucket at or after since, and
// its width.
func billedWindowStart(since, now time.Time) (time.Time, string) {
	since = since.UTC()
	width, step := billing.Day, 24*time.Hour
	if !since.Before(now.Add(-billedHourlyReach)) {
		width, step = billing.Hour, time.Hour
	}
	start := since.Truncate(step)
	if start.Before(since) {
		start = start.Add(step)
	}
	return start, width
}

// BilledWindowFor sums the bill and the credential's own lines per mapped
// credential over the whole billed buckets in [since, now] — the lines only
// up to where the bill is complete (ComparedUntil).
func (s *Store) BilledWindowFor(ctx context.Context, orgID uuid.UUID, since, now time.Time) (BilledWindow, error) {
	start, width := billedWindowStart(since, now)
	out := BilledWindow{Start: start, End: now.UTC(), Width: width, Totals: []BilledTotal{}}
	src := chosenRows("billed_cost", "cpick", "bcost")
	if width == billing.Hour {
		src = `bcost AS (SELECT t.* FROM billed_cost t
			WHERE t.org_id = $1 AND t.bucket_width = '1h' AND t.bucket_start >= $2)`
	}
	maps := `maps AS (
	SELECT secret_key, secret_slot::int AS secret_slot, api_key_id, workspace_id, valid_from,
		COALESCE(valid_to, 'infinity'::timestamptz) AS valid_to
	FROM credential_billing_keys
	WHERE org_id = $1 AND COALESCE(valid_to, 'infinity'::timestamptz) > $2 AND valid_from < $3)`
	// Each bucket goes to one mapping, as in the chart (billedMapping). The
	// credentials are the mapped ones plus, should a bucket's mapping only
	// begin after now, its holder — a bucket in the chart is never missing
	// from the tile.
	rows, err := s.pool.Query(ctx, `WITH `+maps+`, `+src+`,
	billed AS (
		SELECT mk.secret_key, mk.secret_slot, c.amount_cents, c.list_amount_cents
		FROM bcost c
		`+billedMapping+`
		WHERE c.bucket_start < $3 AND mk.secret_key IS NOT NULL)
		SELECT m.secret_key, m.secret_slot, count(b.secret_key) > 0,
			round(COALESCE(SUM(b.amount_cents), 0) / 100, 8)::text, round(COALESCE(SUM(b.list_amount_cents), 0) / 100, 8)::text
		FROM (SELECT secret_key, secret_slot FROM maps UNION SELECT secret_key, secret_slot FROM billed) m
		LEFT JOIN billed b ON b.secret_key = m.secret_key AND b.secret_slot = m.secret_slot
		GROUP BY 1, 2
		ORDER BY 1, 2`, orgID, start, now)
	if err != nil {
		return out, err
	}
	index := map[string]int{}
	for rows.Next() {
		var b BilledTotal
		if err := rows.Scan(&b.SecretKey, &b.SecretSlot, &b.HasBilled, &b.BilledUSD, &b.BilledListUSD); err != nil {
			rows.Close()
			return out, err
		}
		index[fmt.Sprintf("%s#%d", b.SecretKey, b.SecretSlot)] = len(out.Totals)
		out.Totals = append(out.Totals, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Our side ends where the bill is known to be complete: the latest
	// data_refreshed_at of the stored bill (the last landed read's), else of
	// the poller's state; nothing at all before a read has landed.
	var refreshed *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(data_refreshed_at) FROM billed_cost WHERE org_id = $1`,
		orgID).Scan(&refreshed); err != nil {
		return out, err
	}
	if refreshed == nil {
		sync, err := s.BillingSync(ctx, orgID)
		if err != nil {
			return out, err
		}
		refreshed = sync.DataRefreshedAt
	}
	out.ComparedUntil = out.End
	switch {
	case refreshed == nil || !refreshed.After(start):
		out.ComparedUntil = start
	case refreshed.Before(out.End):
		out.ComparedUntil = refreshed.UTC()
	}

	// Our side: the credential's lines in the same window, inside each
	// mapping's interval (a value replaced mid-window was another key).
	rows, err = s.pool.Query(ctx, `WITH `+maps+`
		SELECT m.secret_key, m.secret_slot, mu.model,
			SUM(mu.input_tokens), SUM(mu.output_tokens), SUM(mu.cache_read_tokens),
			SUM(mu.cache_write_5m_tokens), SUM(mu.cache_write_1h_tokens), SUM(mu.web_search_requests),
			SUM(mu.engine_usd)::float8
		FROM (SELECT DISTINCT secret_key, secret_slot, valid_from, valid_to FROM maps) m
		JOIN model_usage mu ON mu.org_id = $1 AND mu.secret_key = m.secret_key AND mu.secret_slot = m.secret_slot
			AND mu.created_at >= GREATEST(m.valid_from, $2) AND mu.created_at < LEAST(m.valid_to, $3)
		GROUP BY 1, 2, 3`, orgID, start, out.ComparedUntil)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key, model string
			slot       int
			t          ModelTokens
			engine     float64
		)
		if err := rows.Scan(&key, &slot, &model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m,
			&t.CacheWrite1h, &t.WebSearches, &engine); err != nil {
			return out, err
		}
		i, ok := index[fmt.Sprintf("%s#%d", key, slot)]
		if !ok {
			continue
		}
		b := &out.Totals[i]
		b.ReportedUSD += engine
		if pr, ok := ListPrice(model); ok {
			b.ListUSD += pr.Cost(t)
		} else {
			b.ListUSD += engine
		}
	}
	return out, rows.Err()
}

// BillingKey is an API key the bill names, with what it cost over the window,
// and the credential it is mapped to, if any.
type BillingKey struct {
	APIKeyID      string     `json:"api_key_id"`
	WorkspaceID   string     `json:"workspace_id"`
	BilledUSD     string     `json:"billed_usd"`
	BilledListUSD string     `json:"billed_list_usd"`
	LastSeen      time.Time  `json:"last_seen"`
	SecretKey     *string    `json:"secret_key"`
	SecretSlot    *int       `json:"secret_slot"`
	MappedSince   *time.Time `json:"mapped_since"`
}

// BillingKeys lists the API keys seen in the bill since `since` (the
// endpoint asks for 30 days), largest amount first — what a person needs to
// pick the right one for a credential. An empty api_key_id is use without a
// key (the Console, a federated token).
func (s *Store) BillingKeys(ctx context.Context, orgID uuid.UUID, since time.Time) ([]BillingKey, error) {
	since = since.UTC().Truncate(24 * time.Hour)
	rows, err := s.pool.Query(ctx, `WITH `+chosenRows("billed_cost", "cpick", "bcost")+`
		SELECT c.api_key_id, c.workspace_id, round(SUM(c.amount_cents) / 100, 8)::text,
			round(SUM(c.list_amount_cents) / 100, 8)::text, max(c.bucket_end),
			mk.secret_key, mk.secret_slot, mk.valid_from
		FROM bcost c
		LEFT JOIN LATERAL (SELECT k.secret_key, k.secret_slot::int AS secret_slot, k.valid_from FROM credential_billing_keys k
			WHERE k.org_id = $1 AND k.api_key_id = c.api_key_id AND k.valid_to IS NULL
			ORDER BY k.valid_from DESC LIMIT 1) mk ON true
		GROUP BY 1, 2, 6, 7, 8
		ORDER BY SUM(c.amount_cents) DESC, 1`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BillingKey{}
	for rows.Next() {
		var k BillingKey
		if err := rows.Scan(&k.APIKeyID, &k.WorkspaceID, &k.BilledUSD, &k.BilledListUSD, &k.LastSeen,
			&k.SecretKey, &k.SecretSlot, &k.MappedSince); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
