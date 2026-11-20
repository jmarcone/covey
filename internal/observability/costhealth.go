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
	// billSyncStaleAfter: the poller reads every organisation's bill once an
	// hour (orchestrator billingEvery); two rounds without an attempt is a
	// sync that stopped running (C7).
	billSyncStaleAfter = 2 * time.Hour
	// billLandsWithin and billHourlyReach: the bill lands 4–24 hours late,
	// and the hourly read reaches 48 hours back (orchestrator
	// billingHourlyBack). Pay-per-token use between them should have landed
	// by the last attempt (C7).
	billLandsWithin = 24 * time.Hour
	billHourlyReach = 48 * time.Hour
)

// lineKindSQL is the kind of credential a model_usage line m was paid by:
// the one it was filed with, else the credential's as it stands, else ”.
// Only api_key is pay-per-token at list price; every other kind, and an
// unknown one, is treated as a seat on a meter — a credential covey cannot
// name is not assumed to be one that needs no basis.
const lineKindSQL = `COALESCE(NULLIF(m.credential_kind, ''), (SELECT rc.kind FROM runtime_credentials rc
	WHERE rc.runtime_id = m.runtime_id AND rc.ord = m.credential_ord), '')`

// HealthInputs are what the checks need from outside the cost tables.
type HealthInputs struct {
	// AdminKey: an org-wide anthropic_admin_key is stored, so the poller
	// reads the organisation's bill (C7). The secret store knows; this
	// package does not.
	AdminKey bool
}

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
	// C1: the latest push of any host, the hosts that stopped (they send a
	// watermark, so they push on every run, idle or not), and the quiet ones
	// (no watermark: idle and stopped look the same).
	LastPushAt  *time.Time  `json:"last_push_at,omitempty"`
	LastPushAgo *int        `json:"last_push_minutes,omitempty"`
	StaleHosts  []StaleHost `json:"stale_hosts,omitempty"`
	QuietHosts  []StaleHost `json:"quiet_hosts,omitempty"`
	// C2: the seat that decides (secret #slot), when it last spent, its
	// latest weekly reading and how old it is, and whether the fleet is
	// paused (no runs, so no in-band readings).
	Credential  string     `json:"credential,omitempty"`
	SpentAt     *time.Time `json:"spent_at,omitempty"`
	ReadingAt   *time.Time `json:"reading_at,omitempty"`
	ReadingAgo  *int       `json:"reading_minutes,omitempty"`
	WeekPercent *float64   `json:"week_percent,omitempty"`
	FleetKilled *bool      `json:"fleet_killed,omitempty"`
	// C3: drops inside one week, weeks cut short before their reset time,
	// and the latest of either.
	Drops       *int       `json:"drops,omitempty"`
	EarlyResets *int       `json:"early_resets,omitempty"`
	LastDropAt  *time.Time `json:"last_drop_at,omitempty"`
	// C4: keys that hold more than one row.
	ExternalDuplicates *int `json:"external_duplicates,omitempty"`
	AgentDuplicates    *int `json:"agent_duplicates,omitempty"`
	// C5: the models used, and those without a list price.
	Models   *int     `json:"models,omitempty"`
	Unpriced []string `json:"unpriced,omitempty"`
	// C6: the spending subscription credentials (secret #slot) without a
	// plan in force or a basis of usage, and the lines that name no
	// credential at all.
	Credentials  []string `json:"credentials,omitempty"`
	Unattributed *int     `json:"unattributed,omitempty"`
	// C7: the bill sync's state.
	SyncStatus     string     `json:"sync_status,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastAttemptAgo *int       `json:"last_attempt_minutes,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	// C8: outside calls of the sources linked to a seat from before that
	// seat's first meter reading.
	Calls *int `json:"calls,omitempty"`
}

// StaleHost is a machine that has not pushed for a while (C1).
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
//	c1 uploader fresh:   no_uploader | fresh (pass), quiet (info), all_silent (warn), stale (fail)
//	c2 meter fresh:      no_meter | fresh (pass), stale (warn), none (fail)
//	c3 meter monotonic:  monotonic (pass), drops | early_reset (fail)
//	c4 no double booking: unique (pass), duplicates (fail)
//	c5 models priced:    no_models | priced (pass), unpriced (fail)
//	c6 cost basis set:   set (pass), missing | unattributed (warn)
//	c7 bill sync:        not_linked | ok (pass), not_run | stopped | failed | not_landed (warn)
//	c8 pre-meter usage:  pre_meter (info)
func (s *Store) CostHealth(ctx context.Context, orgID uuid.UUID, now time.Time, in HealthInputs) (HealthReport, error) {
	now = now.UTC()
	checks := []func(context.Context, uuid.UUID, time.Time, HealthInputs) (HealthCheck, error){
		s.checkUploaderFresh, s.checkMeterFresh, s.checkMeterMonotonic, s.checkNoDoubleBooking,
		s.checkModelsPriced, s.checkCostBasisSet, s.checkBillSync, s.checkPreMeterUsage,
	}
	rep := HealthReport{Status: HealthPass, CheckedAt: now, Checks: make([]HealthCheck, 0, len(checks))}
	for _, check := range checks {
		c, err := check(ctx, orgID, now, in)
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

func credName(key string, slot int) string { return fmt.Sprintf("%s #%d", key, slot) }

// C1 uploader fresh: every host that pushed in the last 7 days pushed within
// 75 minutes (external_usage_hosts.pushed_at). A stopped uploader makes the
// outside part look smaller, not missing.
//
// Only a host that sends a watermark (first_reported_at) can be told to have
// stopped: its uploader pushes on every run, with nothing new or not (`covey
// usage push --complete`). One without a watermark sends nothing while idle,
// so its silence says only "idle or switched off" — worth knowing, never a
// colour. When every host has been silent for longer than 7 days, nothing
// outside is counted any more at all: a warning, until one pushes again.
func (s *Store) checkUploaderFresh(ctx context.Context, orgID uuid.UUID, now time.Time, _ HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c1"}
	var last *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(pushed_at) FROM external_usage_hosts WHERE org_id = $1 AND pushed_at <= $2`,
		orgID, now).Scan(&last); err != nil {
		return c, fmt.Errorf("cost health c1: %w", err)
	}
	if last == nil {
		c.Status, c.Reason = HealthPass, "no_uploader"
		return c, nil
	}
	c.Facts.LastPushAt, c.Facts.LastPushAgo = last, minutesSince(now, *last)
	if now.Sub(*last) > uploaderActiveFor {
		c.Status, c.Reason = HealthWarn, "all_silent"
		return c, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT source, host, pushed_at, first_reported_at IS NOT NULL FROM external_usage_hosts
		WHERE org_id = $1 AND pushed_at > $2::timestamptz - $3::interval AND pushed_at < $2::timestamptz - $4::interval
		ORDER BY pushed_at, source, host`, orgID, now, uploaderActiveFor, uploaderStaleAfter)
	if err != nil {
		return c, fmt.Errorf("cost health c1: %w", err)
	}
	var stale, quiet []StaleHost
	var h StaleHost
	var watermark bool
	if _, err := pgx.ForEachRow(rows, []any{&h.Source, &h.Host, &h.PushedAt, &watermark}, func() error {
		h.Minutes = *minutesSince(now, h.PushedAt)
		if watermark {
			stale = append(stale, h)
		} else {
			quiet = append(quiet, h)
		}
		return nil
	}); err != nil {
		return c, fmt.Errorf("cost health c1: %w", err)
	}
	c.Facts.StaleHosts, c.Facts.QuietHosts = stale, quiet
	switch {
	case len(stale) > 0:
		c.Status, c.Reason = HealthFail, "stale"
	case len(quiet) > 0:
		c.Status, c.Reason = HealthInfo, "quiet"
	default:
		c.Status, c.Reason = HealthPass, "fresh"
	}
	return c, nil
}

// C2 meter fresh, per seat: every credential on a meter that spent in the
// last 7 days — by covey's agents (model_usage) or by an outside source
// linked to it (external_usage_meters) — has a weekly reading, and none of
// its spending lies more than two hours after that reading: those hours have
// no meter figure. A seat whose basis in force is usage is left out — its
// plan prices nothing (0151), so the multiple does not read its meter.
//
// The worst seat decides (no reading before a stale one, the older reading
// first), and the facts name it. A reading is aged by when it was read
// (fetched_at), as the meter orders them: one typed in by hand arrives later
// than it was read. Stale is a warning, not a failure: while the fleet is
// paused no run reads the meter in band, and the hours have to come from a
// reading entered by hand.
func (s *Store) checkMeterFresh(ctx context.Context, orgID uuid.UUID, now time.Time, _ HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c2"}
	rows, err := s.pool.Query(ctx, `WITH spent AS (
			SELECT m.secret_key AS key, m.secret_slot::int AS slot, max(m.created_at) AS last FROM model_usage m
			WHERE m.org_id = $1 AND m.created_at > $2::timestamptz - $3::interval AND m.created_at <= $2
			  AND m.secret_key IS NOT NULL AND m.secret_slot IS NOT NULL AND `+lineKindSQL+` <> 'api_key'
			GROUP BY 1, 2
			UNION ALL
			SELECT em.secret_key, em.secret_slot::int, max(e.ts) FROM external_usage e
			JOIN external_usage_meters em ON em.org_id = e.org_id AND em.source = e.source
			WHERE e.org_id = $1 AND e.ts > $2::timestamptz - $3::interval AND e.ts <= $2
			GROUP BY 1, 2),
		seats AS (SELECT key, slot, max(last) AS last FROM spent GROUP BY 1, 2)
		SELECT s.key, s.slot, s.last, r.fetched_at, r.week_percent FROM seats s
		LEFT JOIN LATERAL (SELECT u.fetched_at, u.week_percent FROM usage_readings u
			WHERE u.org_id = $1 AND u.secret_key = s.key AND u.secret_slot = s.slot AND u.window_kind = 'seven_day'
			  AND u.supported AND NOT u.stale AND u.week_percent IS NOT NULL AND u.fetched_at <= $2
			ORDER BY u.fetched_at DESC, u.id DESC LIMIT 1) r ON true
		WHERE NOT EXISTS (SELECT 1 FROM credential_cost_bases b
			WHERE b.org_id = $1 AND b.secret_key = s.key AND b.secret_slot = s.slot AND b.basis = 'usage'
			  AND b.valid_from <= $2 AND (b.valid_to IS NULL OR b.valid_to > $2))
		ORDER BY s.key, s.slot`, orgID, now, basisWindow)
	if err != nil {
		return c, fmt.Errorf("cost health c2: %w", err)
	}
	type seat struct {
		key   string
		slot  int
		spent time.Time
		at    *time.Time
		pct   *float64
	}
	// rank: 2 no reading, 1 spending more than two hours past it, 0 fresh.
	rank := func(x seat) int {
		switch {
		case x.at == nil:
			return 2
		case x.spent.Sub(*x.at) > meterStaleAfter:
			return 1
		}
		return 0
	}
	var worst *seat
	var x seat
	if _, err := pgx.ForEachRow(rows, []any{&x.key, &x.slot, &x.spent, &x.at, &x.pct}, func() error {
		if worst == nil || rank(x) > rank(*worst) ||
			(rank(x) == rank(*worst) && x.at != nil && worst.at != nil && x.at.Before(*worst.at)) {
			w := x
			if x.at != nil { // a copy: the next row scans into x
				at := *x.at
				w.at = &at
			}
			if x.pct != nil {
				pct := *x.pct
				w.pct = &pct
			}
			worst = &w
		}
		return nil
	}); err != nil {
		return c, fmt.Errorf("cost health c2: %w", err)
	}
	if worst == nil {
		c.Status, c.Reason = HealthPass, "no_meter"
		return c, nil
	}
	spent := worst.spent.UTC()
	c.Facts.Credential, c.Facts.SpentAt = credName(worst.key, worst.slot), &spent
	if worst.at == nil {
		c.Status, c.Reason = HealthFail, "none"
		return c, nil
	}
	var killed bool
	if err := s.pool.QueryRow(ctx, `SELECT fleet_killed FROM organizations WHERE id = $1`, orgID).Scan(&killed); err != nil {
		return c, fmt.Errorf("cost health c2: %w", err)
	}
	at := worst.at.UTC()
	c.Facts.ReadingAt, c.Facts.ReadingAgo, c.Facts.WeekPercent, c.Facts.FleetKilled = &at, minutesSince(now, at), worst.pct, &killed
	if rank(*worst) == 1 {
		c.Status, c.Reason = HealthWarn, "stale"
		return c, nil
	}
	c.Status, c.Reason = HealthPass, "fresh"
	return c, nil
}

// C3 meter monotonic: the meter's own rule (meterQuery), so the check and
// the arithmetic it protects cannot disagree — per credential, source and
// window, a week ends where the reset time moves on by more than an hour,
// and readings of a week that is already over are dropped. Two things are a
// reset nobody recorded:
//
//   - drops: inside one week a reading lies more than a point below the
//     week's top. The meter counts only rises, so it misses everything until
//     the figure is back above the old top;
//   - early_reset: a new week starts, with a fall of more than a point, while
//     the previous reading's reset time was still ahead — an account reset in
//     mid-week. The meter counts the new week from 0, but the week before was
//     cut short, so its points bought less than a week. An ordinary weekly
//     rollover has its reset time behind it and is not one.
//
// Readings of the last 14 days, by when they were read; the weekly
// all-models window only, the one the multiple divides by.
func (s *Store) checkMeterMonotonic(ctx context.Context, orgID uuid.UUID, now time.Time, _ HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c3"}
	from := now.Add(-meterDropWindow)
	rows, err := s.meterRowsAt(ctx, orgID, "day", from)
	if err != nil {
		return c, fmt.Errorf("cost health c3: %w", err)
	}
	var drops, early int
	var last *time.Time
	for i, r := range rows {
		if r.window != WindowSevenDay || r.at.After(now) || !r.at.After(from) {
			continue
		}
		var prev *meterRow
		if p := i - 1; p >= 0 && rows[p].key == r.key && rows[p].slot == r.slot && rows[p].source == r.source && rows[p].window == r.window {
			prev = &rows[p]
		}
		switch {
		case !r.reset && r.pct < r.top-1:
			drops++
		case r.reset && prev != nil && prev.resets != nil && prev.resets.After(r.at) && r.pct < prev.pct-1:
			early++
		default:
			continue
		}
		if at := r.at.UTC(); last == nil || at.After(*last) {
			last = &at
		}
	}
	switch {
	case drops > 0:
		c.Status, c.Reason = HealthFail, "drops"
	case early > 0:
		c.Status, c.Reason = HealthFail, "early_reset"
	default:
		c.Status, c.Reason = HealthPass, "monotonic"
		return c, nil
	}
	c.Facts.Drops, c.Facts.EarlyResets, c.Facts.LastDropAt = &drops, &early, last
	return c, nil
}

// C4 no double booking: one row per call — outside calls by (source,
// session_id, message_id), agent lines by (line_uuid, model): one result
// line is split per model, so line_uuid alone repeats by design. Today the
// schema already holds both (external_usage's primary key, 0150;
// idx_model_usage_line, 0144); the check is what notices when an install
// lost one of them, a restore without its indexes for instance.
func (s *Store) checkNoDoubleBooking(ctx context.Context, orgID uuid.UUID, _ time.Time, _ HealthInputs) (HealthCheck, error) {
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
// without one counts as nothing in every token-price figure (#154). A line
// with tokens and no model name has no price either and is listed as ""
// (the page names it); one without tokens costs nothing whatever its name.
func (s *Store) checkModelsPriced(ctx context.Context, orgID uuid.UUID, now time.Time, _ HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c5"}
	rows, err := s.pool.Query(ctx, `SELECT model FROM external_usage
			WHERE org_id = $1 AND ts > $2::timestamptz - $3::interval AND ts <= $2
			  AND (model <> '' OR input + output + cache_read + cache_write_5m + cache_write_1h > 0)
		UNION SELECT model FROM model_usage
			WHERE org_id = $1 AND created_at > $2::timestamptz - $3::interval AND created_at <= $2
			  AND (model <> '' OR input_tokens + output_tokens + cache_read_tokens + cache_write_5m_tokens + cache_write_1h_tokens > 0)`,
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
		if m == syntheticModel {
			continue
		}
		n++
		if _, ok := ListPrice(m); m == "" || !ok {
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

// C6 cost basis set: every credential on a meter that spent in the last 7
// days has a real cost — a plan in force (a basis of seat or seat_and_usage
// prices the meter by it, and no basis means seat), or a basis of usage in
// force (the usage charges are the money, the plan prices nothing). A seat
// without a plan is unpriced on the headline whatever its basis says. API
// keys are left out: they are pay-per-token at list price unless told
// otherwise; a line of unknown kind is not (lineKindSQL).
//
// Bases and plans are keyed on the secret value (0145, 0151), so the
// credential is too: runtime_id/ord only say where a basis was entered, and
// a Reorder moves values between places. Lines that name no secret at all
// cannot be matched to any basis; they are counted as unattributed.
func (s *Store) checkCostBasisSet(ctx context.Context, orgID uuid.UUID, now time.Time, _ HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c6"}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT m.secret_key, m.secret_slot::int FROM model_usage m
		WHERE m.org_id = $1 AND m.created_at > $2::timestamptz - $3::interval AND m.created_at <= $2
		  AND m.secret_key IS NOT NULL AND m.secret_slot IS NOT NULL AND `+lineKindSQL+` <> 'api_key'
		  AND NOT EXISTS (SELECT 1 FROM credential_plans p
			WHERE p.org_id = m.org_id AND p.secret_key = m.secret_key AND p.secret_slot = m.secret_slot
			  AND p.valid_from <= $2 AND (p.valid_to IS NULL OR p.valid_to > $2))
		  AND NOT EXISTS (SELECT 1 FROM credential_cost_bases b
			WHERE b.org_id = m.org_id AND b.secret_key = m.secret_key AND b.secret_slot = m.secret_slot
			  AND b.basis = 'usage' AND b.valid_from <= $2 AND (b.valid_to IS NULL OR b.valid_to > $2))
		ORDER BY 1, 2`, orgID, now, basisWindow)
	if err != nil {
		return c, fmt.Errorf("cost health c6: %w", err)
	}
	var creds []string
	var key string
	var slot int
	if _, err := pgx.ForEachRow(rows, []any{&key, &slot}, func() error {
		creds = append(creds, credName(key, slot))
		return nil
	}); err != nil {
		return c, fmt.Errorf("cost health c6: %w", err)
	}
	var unattributed int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage m
		WHERE m.org_id = $1 AND m.created_at > $2::timestamptz - $3::interval AND m.created_at <= $2
		  AND (m.secret_key IS NULL OR m.secret_slot IS NULL) AND `+lineKindSQL+` <> 'api_key'`,
		orgID, now, basisWindow).Scan(&unattributed); err != nil {
		return c, fmt.Errorf("cost health c6: %w", err)
	}
	if unattributed > 0 {
		c.Facts.Unattributed = &unattributed
	}
	switch {
	case len(creds) > 0:
		c.Status, c.Reason, c.Facts.Credentials = HealthWarn, "missing", creds
	case unattributed > 0:
		c.Status, c.Reason = HealthWarn, "unattributed"
	default:
		c.Status, c.Reason = HealthPass, "set"
	}
	return c, nil
}

// C7 bill sync: the pay-per-token bill import is healthy, where there is one
// — where an org-wide admin key is stored (the poller reads only then,
// orchestrator SyncBilling; the billing_sync row outlives a removed key and
// says nothing about one). It warns when the sync never ran, stopped running
// (no attempt in two of its hourly rounds), failed, or found no bill where
// covey's own API keys spent between 24 and 48 hours before it — that bill
// should have landed. A not_landed without such use is an organisation whose
// keys were idle: nothing is missing.
func (s *Store) checkBillSync(ctx context.Context, orgID uuid.UUID, now time.Time, in HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c7"}
	if !in.AdminKey {
		c.Status, c.Reason = HealthPass, "not_linked"
		return c, nil
	}
	var status, lastErr string
	var attempt, success *time.Time
	err := s.pool.QueryRow(ctx, `SELECT last_status, last_attempt_at, last_success_at, last_error FROM billing_sync WHERE org_id = $1`,
		orgID).Scan(&status, &attempt, &success, &lastErr)
	if errors.Is(err, pgx.ErrNoRows) {
		c.Status, c.Reason = HealthWarn, "not_run"
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("cost health c7: %w", err)
	}
	c.Facts.SyncStatus, c.Facts.LastSuccessAt, c.Facts.LastAttemptAt = status, success, attempt
	if attempt != nil {
		c.Facts.LastAttemptAgo = minutesSince(now, *attempt)
	}
	if status == "failed" {
		c.Facts.LastError = lastErr
	}
	switch {
	case attempt == nil || now.Sub(*attempt) > billSyncStaleAfter:
		c.Status, c.Reason = HealthWarn, "stopped"
		return c, nil
	case status == "failed":
		c.Status, c.Reason = HealthWarn, "failed"
		return c, nil
	case status == "not_landed":
		var spent bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM model_usage m
			WHERE m.org_id = $1 AND m.created_at > $2::timestamptz - $3::interval AND m.created_at <= $2::timestamptz - $4::interval
			  AND `+lineKindSQL+` = 'api_key')`, orgID, *attempt, billHourlyReach, billLandsWithin).Scan(&spent); err != nil {
			return c, fmt.Errorf("cost health c7: %w", err)
		}
		if spent {
			c.Status, c.Reason = HealthWarn, "not_landed"
			return c, nil
		}
	}
	c.Status, c.Reason = HealthPass, "ok"
	return c, nil
}

// C8 pre-meter usage: outside calls from before their seat's first weekly
// meter reading have a token price and no real cost — say how many, so
// nobody divides them by the meter. Only the calls of a source linked to a
// seat (external_usage_meters) share a meter at all; a source billed per use
// has none to be before. The first reading is the seat's own and the meter's
// (the rows its arithmetic reads, by when they were read); a seat without
// any reading has all its linked calls before it.
func (s *Store) checkPreMeterUsage(ctx context.Context, orgID uuid.UUID, now time.Time, _ HealthInputs) (HealthCheck, error) {
	c := HealthCheck{ID: "c8", Status: HealthInfo, Reason: "pre_meter"}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM external_usage e
		JOIN external_usage_meters em ON em.org_id = e.org_id AND em.source = e.source
		WHERE e.org_id = $1 AND e.ts <= $2 AND e.ts < COALESCE((SELECT min(u.fetched_at) FROM usage_readings u
			WHERE u.org_id = $1 AND u.secret_key = em.secret_key AND u.secret_slot = em.secret_slot
			  AND u.window_kind = 'seven_day' AND u.supported AND NOT u.stale AND u.week_percent IS NOT NULL),
			'infinity'::timestamptz)`, orgID, now).Scan(&n)
	if err != nil {
		return c, fmt.Errorf("cost health c8: %w", err)
	}
	c.Facts.Calls = &n
	return c, nil
}
