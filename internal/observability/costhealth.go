package observability

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Cost health (#178): whether the figures on the cost page can be trusted
// right now. A cost page that is silently wrong is worse than none: an
// uploader that stopped makes the outside part look smaller, a meter without
// readings leaves the multiple without hours, a model without a list price
// counts as nothing. Each check here says one such thing, as a status and a
// reason code with the facts behind it; the page words it in the reader's
// language.
//
// Every check is a read: nothing here writes, and nothing reads the wall
// clock — the caller passes the instant the checks are made at, so a test
// can put a host 76 minutes behind it.

// Health statuses, worst last. info says something worth knowing and never
// colours the badge.
const (
	HealthInfo = "info"
	HealthPass = "pass"
	HealthWarn = "warn"
	HealthFail = "fail"
)

// healthRank orders the statuses for the badge: info never wins.
var healthRank = map[string]int{HealthInfo: 0, HealthPass: 1, HealthWarn: 2, HealthFail: 3}

// The thresholds of the checks, as #178 names them.
const (
	// uploaderStaleAfter: an uploader pushes on a schedule well inside it, so
	// a host that has not pushed for longer has stopped (C1).
	uploaderStaleAfter = 75 * time.Minute
	// uploaderActiveFor: a host that pushed within it is expected to keep
	// pushing; one that has not for longer is a machine no longer in use.
	uploaderActiveFor = 7 * 24 * time.Hour
	// meterStaleAfter: covey reads the meter in band during runs; two hours
	// without a reading is a gap the multiple cannot see into (C2).
	meterStaleAfter = 120 * time.Minute
	// meterDropWindow: how far back a drop inside one reset period is looked
	// for (C3).
	meterDropWindow = 14 * 24 * time.Hour
	// pricedWindow and basisWindow: the models used (C5) and the credentials
	// that spent (C6) within them.
	pricedWindow = 30 * 24 * time.Hour
	basisWindow  = 7 * 24 * time.Hour
)

// syntheticModel is what Claude Code names a line it made up itself (an
// error, a refusal before any call); it costs nothing and has no price.
// The uploader drops it under the same name (usagepush.SyntheticModel).
const syntheticModel = "<synthetic>"

// HealthCheck is one check's answer. Reason is a code the page words (per
// check: see CostHealth); Facts carries what the wording needs.
type HealthCheck struct {
	ID        string      `json:"id"`
	Status    string      `json:"status"`
	Reason    string      `json:"reason"`
	Facts     HealthFacts `json:"facts"`
	CheckedAt time.Time   `json:"checked_at"`
}

// HealthFacts: the figures behind a reason. Only the fields a check sets are
// sent.
type HealthFacts struct {
	// C1: the latest push of any host, and the hosts that stopped.
	LastPushAt  *time.Time  `json:"last_push_at,omitempty"`
	LastPushAgo *int        `json:"last_push_minutes,omitempty"`
	StaleHosts  []StaleHost `json:"stale_hosts,omitempty"`
	// C2: the latest weekly reading, how old it is, and whether the fleet is
	// paused (no runs, so no in-band readings).
	ReadingAt   *time.Time `json:"reading_at,omitempty"`
	ReadingAgo  *int       `json:"reading_minutes,omitempty"`
	WeekPercent *float64   `json:"week_percent,omitempty"`
	FleetKilled *bool      `json:"fleet_killed,omitempty"`
	// C3: drops inside one reset period, and the latest one.
	Drops      *int       `json:"drops,omitempty"`
	LastDropAt *time.Time `json:"last_drop_at,omitempty"`
	// C4: keys that hold more than one row.
	ExternalDuplicates *int `json:"external_duplicates,omitempty"`
	AgentDuplicates    *int `json:"agent_duplicates,omitempty"`
	// C5: the models used, and those without a list price.
	Models   *int     `json:"models,omitempty"`
	Unpriced []string `json:"unpriced,omitempty"`
	// C6: the spending subscription credentials (secret #slot) without a
	// basis or a plan in force.
	Credentials []string `json:"credentials,omitempty"`
	// C7: the bill sync's state.
	SyncStatus    string     `json:"sync_status,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	// C8: outside calls before the first meter reading.
	Calls *int `json:"calls,omitempty"`
}

// StaleHost is a machine whose uploader stopped (C1).
type StaleHost struct {
	Source   string    `json:"source"`
	Host     string    `json:"host"`
	PushedAt time.Time `json:"pushed_at"`
	Minutes  int       `json:"minutes"`
}

// HealthReport is every check at one instant, with the worst status among
// them: the badge's colour, decided here and nowhere else.
type HealthReport struct {
	Status    string        `json:"status"`
	CheckedAt time.Time     `json:"checked_at"`
	Checks    []HealthCheck `json:"checks"`
}

// Worst is the first check with the report's status (the badge's reason);
// nil when every check passed or only informs.
func (r HealthReport) Worst() *HealthCheck {
	if r.Status != HealthWarn && r.Status != HealthFail {
		return nil
	}
	for i := range r.Checks {
		if r.Checks[i].Status == r.Status {
			return &r.Checks[i]
		}
	}
	return nil
}

// CostHealth runs the checks C1–C8 of #178 for one organisation as of now.
//
// Reasons per check:
//
//	c1 uploader fresh:   no_uploader | fresh (pass), stale (fail)
//	c2 meter fresh:      no_meter | fresh (pass), stale (warn), none (fail)
//	c3 meter monotonic:  monotonic (pass), drops (fail)
//	c4 no double booking: unique (pass), duplicates (fail)
//	c5 models priced:    no_models | priced (pass), unpriced (fail)
//	c6 cost basis set:   set (pass), missing (warn)
//	c7 bill sync:        not_linked | ok (pass), failed (warn)
//	c8 pre-meter usage:  pre_meter (info)
func (s *Store) CostHealth(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthReport, error) {
	now = now.UTC()
	checks := []func(context.Context, uuid.UUID, time.Time) (HealthCheck, error){
		s.checkUploaderFresh, s.checkMeterFresh, s.checkMeterMonotonic, s.checkNoDoubleBooking,
		s.checkModelsPriced, s.checkCostBasisSet, s.checkBillSync, s.checkPreMeterUsage,
	}
	rep := HealthReport{Status: HealthPass, CheckedAt: now, Checks: make([]HealthCheck, 0, len(checks))}
	for _, check := range checks {
		c, err := check(ctx, orgID, now)
		if err != nil {
			return HealthReport{}, err
		}
		c.CheckedAt = now
		if healthRank[c.Status] > healthRank[rep.Status] {
			rep.Status = c.Status
		}
		rep.Checks = append(rep.Checks, c)
	}
	return rep, nil
}

func minutesSince(now, t time.Time) *int {
	m := int(now.Sub(t) / time.Minute)
	return &m
}

func intp(n int) *int { return &n }

// C1 uploader fresh: every host that pushed in the last 7 days pushed within
// 75 minutes (external_usage_hosts.pushed_at). A stopped uploader makes the
// outside part look smaller, not missing.
func (s *Store) checkUploaderFresh(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c1"}
	var last *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(pushed_at) FROM external_usage_hosts WHERE org_id = $1`,
		orgID).Scan(&last); err != nil {
		return c, fmt.Errorf("cost health c1: %w", err)
	}
	if last == nil {
		c.Status, c.Reason = HealthPass, "no_uploader"
		return c, nil
	}
	c.Facts.LastPushAt, c.Facts.LastPushAgo = last, minutesSince(now, *last)
	rows, err := s.pool.Query(ctx, `SELECT source, host, pushed_at FROM external_usage_hosts
		WHERE org_id = $1 AND pushed_at > $2::timestamptz - $3::interval AND pushed_at < $2::timestamptz - $4::interval
		ORDER BY pushed_at, source, host`, orgID, now, uploaderActiveFor, uploaderStaleAfter)
	if err != nil {
		return c, fmt.Errorf("cost health c1: %w", err)
	}
	stale, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StaleHost, error) {
		var h StaleHost
		err := r.Scan(&h.Source, &h.Host, &h.PushedAt)
		h.Minutes = *minutesSince(now, h.PushedAt)
		return h, err
	})
	if err != nil {
		return c, fmt.Errorf("cost health c1: %w", err)
	}
	if len(stale) > 0 {
		c.Status, c.Reason, c.Facts.StaleHosts = HealthFail, "stale", stale
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "fresh"
	return c, nil
}

// C2 meter fresh: the age of the latest weekly reading. Stale is a warning,
// not a failure: while the fleet is paused no run reads the meter in band,
// and the hours have to come from somewhere else (a probe). Only readings
// with a figure count: a row that says the meter could not be read is not a
// reading. An organisation with no subscription credential has no meter.
func (s *Store) checkMeterFresh(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c2"}
	var at time.Time
	var pct float64
	err := s.pool.QueryRow(ctx, `SELECT received_at, week_percent FROM usage_readings
		WHERE org_id = $1 AND window_kind = 'seven_day' AND supported AND NOT stale AND week_percent IS NOT NULL
		  AND received_at <= $2
		ORDER BY received_at DESC, id DESC LIMIT 1`, orgID, now).Scan(&at, &pct)
	if errors.Is(err, pgx.ErrNoRows) {
		var seats bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runtime_credentials rc
			JOIN runtimes r ON r.id = rc.runtime_id WHERE r.org_id = $1 AND rc.kind <> 'api_key')`,
			orgID).Scan(&seats); err != nil {
			return c, fmt.Errorf("cost health c2: %w", err)
		}
		if !seats {
			c.Status, c.Reason = HealthPass, "no_meter"
			return c, nil
		}
		c.Status, c.Reason = HealthFail, "none"
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("cost health c2: %w", err)
	}
	var killed bool
	if err := s.pool.QueryRow(ctx, `SELECT fleet_killed FROM organizations WHERE id = $1`, orgID).Scan(&killed); err != nil {
		return c, fmt.Errorf("cost health c2: %w", err)
	}
	c.Facts.ReadingAt, c.Facts.ReadingAgo, c.Facts.WeekPercent, c.Facts.FleetKilled = &at, minutesSince(now, at), &pct, &killed
	if now.Sub(at) > meterStaleAfter {
		c.Status, c.Reason = HealthWarn, "stale"
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "fresh"
	return c, nil
}

// C3 meter monotonic: inside one reset period (the same credential and the
// same resets_at) the weekly percent never goes down by more than the one
// point a lagging reading may show. A drop is a reset nobody recorded, and
// the meter arithmetic, which counts only rises, would miss the week after
// it. Readings in the order they were read (fetched_at; a reading entered by
// hand arrives later than it was read), the ones the meter reads (supported,
// not stale, with a figure). A reading without a reset time belongs to no
// known period and is left out: lumped together, every ordinary weekly reset
// would read as a drop.
func (s *Store) checkMeterMonotonic(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c3"}
	var drops int
	var last *time.Time
	err := s.pool.QueryRow(ctx, `SELECT count(*), max(fetched_at) FROM (
		SELECT fetched_at, week_percent,
			lag(week_percent) OVER (PARTITION BY secret_key, secret_slot, resets_at ORDER BY fetched_at, id) AS prev
		FROM usage_readings
		WHERE org_id = $1 AND window_kind = 'seven_day' AND supported AND NOT stale
		  AND week_percent IS NOT NULL AND resets_at IS NOT NULL AND secret_key IS NOT NULL
		  AND fetched_at > $2::timestamptz - $3::interval AND fetched_at <= $2) x
		WHERE week_percent < prev - 1`, orgID, now, meterDropWindow).Scan(&drops, &last)
	if err != nil {
		return c, fmt.Errorf("cost health c3: %w", err)
	}
	if drops > 0 {
		c.Status, c.Reason, c.Facts.Drops, c.Facts.LastDropAt = HealthFail, "drops", &drops, last
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "monotonic"
	return c, nil
}

// C4 no double booking: one row per call — outside calls by (source,
// session_id, message_id), agent lines by (line_uuid, model): one result
// line is split per model, so line_uuid alone repeats by design. Today the
// schema already holds both (external_usage's primary key, 0150;
// idx_model_usage_line, 0144); the check is what notices when an install
// lost one of them, a restore without its indexes for instance.
func (s *Store) checkNoDoubleBooking(ctx context.Context, orgID uuid.UUID, _ time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c4"}
	var ext, agent int
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM (SELECT 1 FROM external_usage WHERE org_id = $1
			GROUP BY source, session_id, message_id HAVING count(*) > 1) x),
		(SELECT count(*) FROM (SELECT 1 FROM model_usage WHERE org_id = $1 AND line_uuid IS NOT NULL
			GROUP BY line_uuid, model HAVING count(*) > 1) y)`, orgID).Scan(&ext, &agent)
	if err != nil {
		return c, fmt.Errorf("cost health c4: %w", err)
	}
	if ext > 0 || agent > 0 {
		c.Status, c.Reason = HealthFail, "duplicates"
		c.Facts.ExternalDuplicates, c.Facts.AgentDuplicates = &ext, &agent
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "unique"
	return c, nil
}

// C5 models priced: every model used in the last 30 days, by covey's agents
// or outside, has a list price in covey's table (ListPrice) — a model
// without one counts as nothing in every token-price figure (#154).
func (s *Store) checkModelsPriced(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c5"}
	rows, err := s.pool.Query(ctx, `SELECT model FROM external_usage
			WHERE org_id = $1 AND ts > $2::timestamptz - $3::interval AND ts <= $2
		UNION SELECT model FROM model_usage
			WHERE org_id = $1 AND created_at > $2::timestamptz - $3::interval AND created_at <= $2`,
		orgID, now, pricedWindow)
	if err != nil {
		return c, fmt.Errorf("cost health c5: %w", err)
	}
	models, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return c, fmt.Errorf("cost health c5: %w", err)
	}
	n := 0
	var missing []string
	for _, m := range models {
		if m == "" || m == syntheticModel {
			continue
		}
		n++
		if _, ok := ListPrice(m); !ok {
			missing = append(missing, m)
		}
	}
	slices.Sort(missing)
	c.Facts.Models = &n
	switch {
	case len(missing) > 0:
		c.Status, c.Reason, c.Facts.Unpriced = HealthFail, "unpriced", missing
	case n == 0:
		c.Status, c.Reason = HealthPass, "no_models"
	default:
		c.Status, c.Reason = HealthPass, "priced"
	}
	return c, nil
}

// C6 cost basis set: every subscription credential that spent in the last 7
// days has a cost basis or a plan in force — without either its real cost is
// unknown. API keys are left out: they are pay-per-token at list price unless
// told otherwise. Bases and plans are keyed on the secret value (0145, 0151),
// so the credential is too: runtime_id/ord only say where a basis was
// entered, and a Reorder moves values between places. The kind is the one
// the line was filed with, else the credential's as it stands.
func (s *Store) checkCostBasisSet(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c6"}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT m.secret_key || ' #' || m.secret_slot FROM model_usage m
		WHERE m.org_id = $1 AND m.created_at > $2::timestamptz - $3::interval AND m.created_at <= $2
		  AND m.secret_key IS NOT NULL AND m.secret_slot IS NOT NULL
		  AND COALESCE(NULLIF(m.credential_kind, ''), (SELECT rc.kind FROM runtime_credentials rc
			WHERE rc.runtime_id = m.runtime_id AND rc.ord = m.credential_ord), 'api_key') <> 'api_key'
		  AND NOT EXISTS (SELECT 1 FROM credential_cost_bases b
			WHERE b.org_id = m.org_id AND b.secret_key = m.secret_key AND b.secret_slot = m.secret_slot
			  AND b.valid_from <= $2 AND (b.valid_to IS NULL OR b.valid_to > $2))
		  AND NOT EXISTS (SELECT 1 FROM credential_plans p
			WHERE p.org_id = m.org_id AND p.secret_key = m.secret_key AND p.secret_slot = m.secret_slot
			  AND p.valid_from <= $2 AND (p.valid_to IS NULL OR p.valid_to > $2))
		ORDER BY 1`, orgID, now, basisWindow)
	if err != nil {
		return c, fmt.Errorf("cost health c6: %w", err)
	}
	creds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return c, fmt.Errorf("cost health c6: %w", err)
	}
	if len(creds) > 0 {
		c.Status, c.Reason, c.Facts.Credentials = HealthWarn, "missing", creds
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "set"
	return c, nil
}

// C7 bill sync: the pay-per-token bill import is healthy, where there is one
// (a billing_sync row exists once an admin key made the poller run).
func (s *Store) checkBillSync(ctx context.Context, orgID uuid.UUID, _ time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c7"}
	var status, lastErr string
	var success *time.Time
	err := s.pool.QueryRow(ctx, `SELECT last_status, last_success_at, last_error FROM billing_sync WHERE org_id = $1`,
		orgID).Scan(&status, &success, &lastErr)
	if errors.Is(err, pgx.ErrNoRows) {
		c.Status, c.Reason = HealthPass, "not_linked"
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("cost health c7: %w", err)
	}
	c.Facts.SyncStatus, c.Facts.LastSuccessAt = status, success
	if status == "failed" {
		c.Status, c.Reason, c.Facts.LastError = HealthWarn, "failed", lastErr
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "ok"
	return c, nil
}

// C8 pre-meter usage: outside calls from before the organisation's first
// weekly meter reading have a token price and no real cost — say how many,
// so nobody divides them by the meter. The first reading is the meter's own
// (the rows its arithmetic reads, by when they were read); without any
// reading every outside call lies before it.
func (s *Store) checkPreMeterUsage(ctx context.Context, orgID uuid.UUID, now time.Time) (HealthCheck, error) {
	c := HealthCheck{ID: "c8", Status: HealthInfo, Reason: "pre_meter"}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM external_usage e
		WHERE e.org_id = $1 AND e.ts <= $2 AND e.ts < COALESCE((SELECT min(fetched_at) FROM usage_readings
			WHERE org_id = $1 AND window_kind = 'seven_day' AND supported AND NOT stale AND week_percent IS NOT NULL),
			'infinity'::timestamptz)`, orgID, now).Scan(&n)
	if err != nil {
		return c, fmt.Errorf("cost health c8: %w", err)
	}
	c.Facts.Calls = &n
	return c, nil
}
