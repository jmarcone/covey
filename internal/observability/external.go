package observability

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"covey/internal/daemon"
)

// Usage outside covey (#120, migration 0150): the model calls of Claude Code
// sessions that run on an operator's machine rather than in a sandbox, sent
// as token counts by an uploader. They share the subscription meter with
// covey's agents, and on a per-use bill they can be most of it.
//
// They are kept apart from the agents on purpose: their own table, never a
// cost entry, a model_usage row, a budget or a guard rail. The cost cells
// carry them as a field of their own (CostCells.External), and every figure
// the cost endpoints returned before stays what it was.
//
// The formulas are the ones #120 names:
//
//   - F1, a call's list price: its tokens at covey's list (ListPrice(model).
//     Cost), the same table and formula as the agents' recomputed figure. A
//     model the list does not know gets no figure, not $0: its tokens are
//     counted as unpriced.
//   - F2, a bucket: the sum of F1 over the calls with b ≤ ts < b + width.
//   - F3, the meter share, per reading of a seat's meter: step i is the
//     reading at t_i, and its span S_i = (t_{i−1}, t_i] reaches back to the
//     previous reading of the same meter (the baseline before the window,
//     for the first one). R_i is the step's priced movement, L_out,i F1 over
//     the calls of the sources linked to the seat with ts in S_i (a model
//     the list does not know at the call's reported_usd), L_cov,i covey's
//     recomputed list of the seat's lines with created_at in S_i (a model
//     the list does not know at the engine's figure, as measuredBetween).
//     When the step is reported (stepReported) and L_out,i + L_cov,i > 0:
//     R_out,i = R_i · L_out,i / (L_out,i + L_cov,i), R_cov,i = R_i − R_out,i.
//     Otherwise the step's movement is nobody's: unsplit, never covey's
//     whole. Each part is placed at t_i.
//
//     L_cov comes from model_usage only: a booking of an older daemon that
//     files no lines is not in it (the booking check flags such bookings),
//     so a step with only such a run moved "without a reported call".
//   - F3c, the split per chunk and the unattributed share (#120). The meter
//     is read in whole percents, only while covey runs, and lags use by
//     minutes: most readings move 0 points while list price accrues, so a
//     reading's own list share says little about its own movement. And the
//     meter is account-wide: a machine that does not report (another one on
//     the same subscription) moves it too. Per seat, the steps are walked in
//     time order and gathered into chunks: a split, idle or
//     moved_without_reported_call step joins the current chunk, which
//     closes once its points P_c reach 5 (at that reading). A not_reported,
//     outside_unpriced or unpriced step closes it before itself and stays
//     out of every chunk, and so does every step read while the seat's
//     cost basis was usage (0151: its points are the usage charges', not
//     in the dent), as do a reset and a change of plan price (the
//     points are percent of the plan's weekly limit). The last chunk may
//     hold fewer than 5 points. A run of steps with no list (moved with no
//     call, or idle) right after a chunk that closed at its size, with
//     nothing closing it in between, is the meter's lag after that chunk's
//     use: it joins that chunk rather than standing as a chunk with no
//     list; k is taken on the chunks as they closed, before this. A step
//     with covey bookings without a line and no list shares no chunk with
//     list, nor with movement with no call.
//     Per chunk c: P_c, L_c = Σ (L_out + L_cov),
//     Lo_c and Lc_c, and S_c = 2 points of slack (the two ends' whole
//     percents and one point of lag at the end, as meterQuery reads a drop
//     of one point as a lagging reading). Per plan price:
//     k = min over the chunks at that price with P_c ≥ 10, L_c > 0 and
//     every call priced of (P_c + S_c) / L_c, points per list $ (a reset
//     step's points and list are left out of it: its span holds calls
//     whose points went with the old week); null without such a chunk.
//     U_c = max(0, P_c − S_c − k·L_c) when k is known; U_c = P_c for a
//     chunk with L_c = 0 (the meter moved and nobody reported a call);
//     U_c = 0 without k and for a chunk with unpriced outside calls or a
//     covey booking without a line (their use is missing from L_c, so it
//     would land in U_c). Each step of a chunk: R_un = R · U_c / P_c, and
//     the rest by the CHUNK's list share, R_out = (R − R_un) · Lo_c / L_c,
//     R_cov the rest; covey's part goes to the agents by their share of
//     Lc_c. So each chunk's parts are the chunk-level split, placed over
//     its steps, and every step keeps covey + outside + unattributed +
//     unsplit = R.
//     Assuming the meter charges in proportion to list price at one true
//     rate k* per plan: P*_c ≥ k*·L_c and P_c + S_c ≥ P*_c for every chunk,
//     so k ≥ k* and U_c ≤ the true unattributed points: the unattributed
//     part beyond the rate is a lower bound, and the multiples lower
//     bounds. Unattributed movement is booked to nobody, never to covey or
//     a source.

// Limits of one upload.
const (
	ExternalMaxRows      = 5000
	ExternalMaxBodyBytes = 2 << 20
	// ExternalMaxAhead is how far in the future a call's ts may lie: clock
	// skew between the uploader's machine and the control plane, no more.
	ExternalMaxAhead  = 5 * time.Minute
	externalMaxSource = 64
	externalMaxField  = 256
	// externalMaxUSD keeps reported_usd inside its NUMERIC(14,6) column.
	externalMaxUSD = 1e7
	// externalMaxTokens bounds every count of one call. No real call comes
	// near it (a context is about a million tokens), and it keeps the sums
	// the cost page reads inside int64: a window would need nine billion
	// calls at the bound to overflow, where two rows of 9e18 did at once.
	externalMaxTokens = 1_000_000_000
	// externalMaxHosts bounds the hosts one push names, in its rows and in
	// reported_through together: each is a machine the meter share waits
	// for (stepReported), so a push must not invent thousands of them. A
	// source has at most as many active at once (UpsertExternalUsage).
	externalMaxHosts = 100
)

// External origins: where in a session a call was made.
var externalOrigins = map[string]bool{"main": true, "subagent": true, "workflow": true}

// ExternalRow is one model call as the uploader sends it. Pointers mark the
// required fields that have no usable zero value, so that a missing one is
// refused rather than read as 0.
type ExternalRow struct {
	Host         string     `json:"host"`
	SessionID    string     `json:"session_id"`
	MessageID    string     `json:"message_id"`
	TS           *time.Time `json:"ts"`
	Model        string     `json:"model"`
	Input        *int64     `json:"input"`
	Output       *int64     `json:"output"`
	CacheRead    *int64     `json:"cache_read"`
	CacheWrite5m *int64     `json:"cache_write_5m"`
	CacheWrite1h *int64     `json:"cache_write_1h"`
	Origin       *string    `json:"origin,omitempty"`
	ReportedUSD  *float64   `json:"reported_usd,omitempty"`
}

// MeterCredential names the subscription credential a source shares its
// meter with, by its secret (as the meter readings do).
type MeterCredential struct {
	SecretKey  string `json:"secret_key"`
	SecretSlot int    `json:"secret_slot"`
}

// MeterLinkChange is what an upload says about its source's meter link: Set
// false leaves the stored link alone, Set with a nil Credential removes it.
type MeterLinkChange struct {
	Set        bool
	Credential *MeterCredential
}

// ErrInvalidExternal: an upload that cannot be taken as it stands. The
// reason names the offending row (0-based) where there is one.
type ErrInvalidExternal struct{ Reason string }

func (e ErrInvalidExternal) Error() string { return "invalid external usage: " + e.Reason }

// ExternalUploadResult is what an upload changed. Inserted + Updated +
// Unchanged = Received: a re-sent row that is identical counts as unchanged.
// Hosts: the source's machines as stored after the upload, with how far each
// has reported.
type ExternalUploadResult struct {
	Source          string           `json:"source"`
	Received        int              `json:"received"`
	Inserted        int              `json:"inserted"`
	Updated         int              `json:"updated"`
	Unchanged       int              `json:"unchanged"`
	MeterCredential *MeterCredential `json:"meter_credential"`
	Hosts           []ExternalHost   `json:"hosts"`
}

// ExternalHost is one machine of a source: ReportedThrough, the watermark
// its pushes gave (every call of the host with ts at or before it has been
// sent; nil when none did), FirstReportedAt, when its first watermark
// arrived (nil when none did), and PushedAt, its latest push.
type ExternalHost struct {
	Host            string     `json:"host"`
	ReportedThrough *time.Time `json:"reported_through"`
	FirstReportedAt *time.Time `json:"first_reported_at"`
	PushedAt        time.Time  `json:"pushed_at"`
}

// ValidateExternalSource checks a source label.
func ValidateExternalSource(source string) error {
	if source == "" {
		return ErrInvalidExternal{Reason: "source is required"}
	}
	if !utf8.ValidString(source) || utf8.RuneCountInString(source) > externalMaxSource {
		return ErrInvalidExternal{Reason: fmt.Sprintf("source must be at most %d characters", externalMaxSource)}
	}
	// Postgres refuses 0x00 in text: refused here, it is a 400, not a 500.
	if strings.ContainsRune(source, 0) {
		return ErrInvalidExternal{Reason: "source must not contain a NUL character"}
	}
	return nil
}

// externalText checks one required text field of an upload.
func externalText(name, v string) string {
	switch {
	case v == "":
		return name + " is required"
	case !utf8.ValidString(v) || len(v) > externalMaxField:
		return fmt.Sprintf("%s must be valid UTF-8 of at most %d bytes", name, externalMaxField)
	case strings.ContainsRune(v, 0):
		return name + " must not contain a NUL character"
	}
	return ""
}

// ValidateExternalReported checks a push's watermarks: per host, the time
// through which its calls have been sent. A time after now is not refused:
// the store clamps it to the push's arrival.
func ValidateExternalReported(reported map[string]time.Time) error {
	if len(reported) > externalMaxHosts {
		return ErrInvalidExternal{Reason: fmt.Sprintf("reported_through names %d hosts, at most %d", len(reported), externalMaxHosts)}
	}
	for host, at := range reported {
		if msg := externalText("reported_through host", host); msg != "" {
			return ErrInvalidExternal{Reason: msg}
		}
		if at.IsZero() {
			return ErrInvalidExternal{Reason: fmt.Sprintf("reported_through of %q needs a time (RFC 3339)", host)}
		}
	}
	return nil
}

// ValidateExternalHosts checks that a push names at most externalMaxHosts
// distinct hosts, in its rows and its watermarks together.
func ValidateExternalHosts(rows []ExternalRow, reported map[string]time.Time) error {
	hosts := make(map[string]bool, len(reported))
	for h := range reported {
		hosts[h] = true
	}
	for _, r := range rows {
		hosts[r.Host] = true
	}
	if len(hosts) > externalMaxHosts {
		return ErrInvalidExternal{Reason: fmt.Sprintf("the push names %d hosts (rows and reported_through together), at most %d; "+
			"send each machine's rows under its own host, at most %d per request", len(hosts), externalMaxHosts, externalMaxHosts)}
	}
	return nil
}

// ValidateExternalRows checks an upload's rows against #120's rules: every
// required field present, counts between 0 and externalMaxTokens, ts not
// after now + 5 min, at most ExternalMaxRows, each message_id once.
func ValidateExternalRows(rows []ExternalRow, now time.Time) error {
	if len(rows) > ExternalMaxRows {
		return ErrInvalidExternal{Reason: fmt.Sprintf("%d rows, at most %d per request", len(rows), ExternalMaxRows)}
	}
	seen := make(map[string]int, len(rows))
	latest := now.Add(ExternalMaxAhead)
	for i, r := range rows {
		bad := func(format string, a ...any) error {
			return ErrInvalidExternal{Reason: fmt.Sprintf("row %d: ", i) + fmt.Sprintf(format, a...)}
		}
		for _, f := range []struct{ name, v string }{
			{"host", r.Host}, {"session_id", r.SessionID}, {"message_id", r.MessageID}, {"model", r.Model},
		} {
			if msg := externalText(f.name, f.v); msg != "" {
				return bad("%s", msg)
			}
		}
		if r.TS == nil || r.TS.IsZero() {
			return bad("ts is required (RFC 3339)")
		}
		if r.TS.After(latest) {
			return bad("ts %s is later than now + %s", r.TS.UTC().Format(time.RFC3339), ExternalMaxAhead)
		}
		for _, c := range []struct {
			name string
			v    *int64
		}{
			{"input", r.Input}, {"output", r.Output}, {"cache_read", r.CacheRead},
			{"cache_write_5m", r.CacheWrite5m}, {"cache_write_1h", r.CacheWrite1h},
		} {
			if c.v == nil {
				return bad("%s is required", c.name)
			}
			if *c.v < 0 {
				return bad("%s must not be negative", c.name)
			}
			if *c.v > externalMaxTokens {
				return bad("%s must be at most %d", c.name, externalMaxTokens)
			}
		}
		if r.Origin != nil && !externalOrigins[*r.Origin] {
			return bad("origin must be main, subagent or workflow")
		}
		if r.ReportedUSD != nil && (*r.ReportedUSD < 0 || *r.ReportedUSD >= externalMaxUSD || *r.ReportedUSD != *r.ReportedUSD) {
			return bad("reported_usd must be a non-negative amount")
		}
		if j, ok := seen[r.MessageID]; ok {
			return bad("message_id %q already is row %d", r.MessageID, j)
		}
		seen[r.MessageID] = i
	}
	return nil
}

// UpsertExternalUsage stores an upload for the organisation: every row
// replaces the one with its (source, message_id) or is added, the source's
// meter link is changed as the upload says, and every host the upload names
// (in a row or in reported) is marked as pushed now, with its watermark
// moved up to reported's time (clamped to now, never back) — in one
// transaction, so a refused link stores no row either.
func (s *Store) UpsertExternalUsage(ctx context.Context, orgID uuid.UUID, source string, link MeterLinkChange,
	rows []ExternalRow, reported map[string]time.Time, now time.Time) (ExternalUploadResult, error) {
	res := ExternalUploadResult{Source: source, Received: len(rows), Hosts: []ExternalHost{}}
	if err := ValidateExternalSource(source); err != nil {
		return res, err
	}
	if err := ValidateExternalRows(rows, now); err != nil {
		return res, err
	}
	if err := ValidateExternalReported(reported); err != nil {
		return res, err
	}
	if err := ValidateExternalHosts(rows, reported); err != nil {
		return res, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if link.Set {
		if err := setMeterLink(ctx, tx, orgID, source, link.Credential); err != nil {
			return res, err
		}
	}
	// The answer names the link the source has now, whether this upload set
	// it or an earlier one did.
	var (
		m    MeterCredential
		slot int16
	)
	err = tx.QueryRow(ctx, `SELECT secret_key, secret_slot FROM external_usage_meters
		WHERE org_id = $1 AND source = $2`, orgID, source).Scan(&m.SecretKey, &slot)
	switch {
	case err == nil:
		m.SecretSlot = int(slot)
		res.MeterCredential = &m
	case !errors.Is(err, pgx.ErrNoRows):
		return res, err
	}

	if len(rows) > 0 {
		// The rows are upserted, and so locked, in message_id order: two
		// concurrent pushes of overlapping batches then take their row locks
		// in one order and wait for each other instead of deadlocking.
		rows = append([]ExternalRow(nil), rows...)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].MessageID < rows[j].MessageID })
		n := len(rows)
		var (
			host, session, msg, model = make([]string, n), make([]string, n), make([]string, n), make([]string, n)
			ts                        = make([]time.Time, n)
			in, out, cr, w5, w1       = make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
			origin                    = make([]*string, n)
			reported                  = make([]*float64, n)
		)
		for i, r := range rows {
			host[i], session[i], msg[i], model[i] = r.Host, r.SessionID, r.MessageID, r.Model
			ts[i] = r.TS.UTC()
			in[i], out[i], cr[i], w5[i], w1[i] = *r.Input, *r.Output, *r.CacheRead, *r.CacheWrite5m, *r.CacheWrite1h
			origin[i], reported[i] = r.Origin, r.ReportedUSD
		}
		// A row that is already stored as sent is not touched (the WHERE of
		// the update), so a re-upload is visible as unchanged; xmax = 0 tells
		// an insert from an update.
		q, err := tx.Query(ctx, `INSERT INTO external_usage AS e (org_id, source, host, session_id, message_id, ts,
			model, input, output, cache_read, cache_write_5m, cache_write_1h, origin, reported_usd)
		SELECT $1, $2, u.* FROM unnest($3::text[], $4::text[], $5::text[], $6::timestamptz[], $7::text[],
			$8::bigint[], $9::bigint[], $10::bigint[], $11::bigint[], $12::bigint[], $13::text[], $14::float8[]) AS u
		ON CONFLICT (org_id, source, message_id) DO UPDATE SET
			host = EXCLUDED.host, session_id = EXCLUDED.session_id, ts = EXCLUDED.ts, model = EXCLUDED.model,
			input = EXCLUDED.input, output = EXCLUDED.output, cache_read = EXCLUDED.cache_read,
			cache_write_5m = EXCLUDED.cache_write_5m, cache_write_1h = EXCLUDED.cache_write_1h,
			origin = EXCLUDED.origin, reported_usd = EXCLUDED.reported_usd
		WHERE (e.host, e.session_id, e.ts, e.model, e.input, e.output, e.cache_read, e.cache_write_5m,
				e.cache_write_1h, e.origin, e.reported_usd)
			IS DISTINCT FROM (EXCLUDED.host, EXCLUDED.session_id, EXCLUDED.ts, EXCLUDED.model, EXCLUDED.input,
				EXCLUDED.output, EXCLUDED.cache_read, EXCLUDED.cache_write_5m, EXCLUDED.cache_write_1h,
				EXCLUDED.origin, EXCLUDED.reported_usd)
		RETURNING xmax = 0`, orgID, source, host, session, msg, ts, model, in, out, cr, w5, w1, origin, reported)
		if err != nil {
			return res, err
		}
		for q.Next() {
			var inserted bool
			if err := q.Scan(&inserted); err != nil {
				q.Close()
				return res, err
			}
			if inserted {
				res.Inserted++
			} else {
				res.Updated++
			}
		}
		q.Close()
		if err := q.Err(); err != nil {
			return res, err
		}
		res.Unchanged = res.Received - res.Inserted - res.Updated
	}

	// The hosts: GREATEST skips a NULL, so a push without a watermark for a
	// host keeps the one it had.
	marks := map[string]*time.Time{}
	for _, r := range rows {
		if _, ok := marks[r.Host]; !ok {
			marks[r.Host] = nil
		}
	}
	for host, at := range reported {
		at := at.UTC()
		if at.After(now) {
			at = now.UTC()
		}
		marks[host] = &at
	}
	if len(marks) > 0 {
		// In host order, for the same reason as the rows: a fixed lock order.
		hosts, ats := make([]string, 0, len(marks)), make([]*time.Time, 0, len(marks))
		for host := range marks {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		for _, host := range hosts {
			ats = append(ats, marks[host])
		}
		activeFrom := now.UTC().Add(-externalActiveFor)
		// The hosts of the push that were active before it: a push naming
		// only these never adds an active host (F6 below).
		var wasActive int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM external_usage_hosts
			WHERE org_id = $1 AND source = $2 AND host = ANY($3) AND pushed_at > $4`,
			orgID, source, hosts, activeFrom).Scan(&wasActive); err != nil {
			return res, err
		}
		// first_reported_at: when the host's first watermark arrived, set
		// once (review of e82d640f, F5): hostMarks dates the source's claim to
		// watermarks by it, not by the watermarks, which move on every push.
		if _, err := tx.Exec(ctx, `INSERT INTO external_usage_hosts (org_id, source, host, reported_through,
				first_reported_at, pushed_at)
			SELECT $1, $2, h.host, h.at, CASE WHEN h.at IS NOT NULL THEN $5::timestamptz END, $5
			FROM unnest($3::text[], $4::timestamptz[]) AS h(host, at)
			ON CONFLICT (org_id, source, host) DO UPDATE SET
				reported_through = GREATEST(external_usage_hosts.reported_through, EXCLUDED.reported_through),
				first_reported_at = COALESCE(external_usage_hosts.first_reported_at, EXCLUDED.first_reported_at),
				pushed_at = GREATEST(external_usage_hosts.pushed_at, EXCLUDED.pushed_at)`,
			orgID, source, hosts, ats, now.UTC()); err != nil {
			return res, err
		}
		// At most externalMaxHosts machines of a source active at once (they
		// pushed in the last externalActiveFor, this push included): each is
		// waited for by stepReported, and a source can otherwise grow them
		// 100 a push (review of d3498bee, F6). A push that names a host not
		// active before it and takes the count past the bound is refused
		// whole, as a 400. Two pushes of the source at once each count
		// without the other's new hosts, so they may pass it by one push's
		// hosts at most; a push of hosts that were all active already is
		// never refused, so such an overshoot locks no machine out (review of
		// e82d640f, F6): it shrinks as hosts stop pushing.
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM external_usage_hosts
			WHERE org_id = $1 AND source = $2 AND pushed_at > $3`,
			orgID, source, activeFrom).Scan(&active); err != nil {
			return res, err
		}
		if active > externalMaxHosts && wasActive < len(hosts) {
			return res, ErrInvalidExternal{Reason: fmt.Sprintf("source %q would have %d hosts that pushed in the last 7 days, "+
				"at most %d; push under fewer host names, or wait until old ones have not pushed for 7 days", source, active, externalMaxHosts)}
		}
	}
	hosts, err := externalHosts(ctx, tx, orgID, []string{source})
	if err != nil {
		return res, err
	}
	res.Hosts = append(res.Hosts, hosts[source]...)
	return res, tx.Commit(ctx)
}

// externalHosts are the stored hosts of the given sources, by source and
// host name.
func externalHosts(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, orgID uuid.UUID, sources []string) (map[string][]ExternalHost, error) {
	rows, err := q.Query(ctx, `SELECT source, host, reported_through, first_reported_at, pushed_at FROM external_usage_hosts
		WHERE org_id = $1 AND source = ANY($2) ORDER BY source, host`, orgID, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ExternalHost{}
	for rows.Next() {
		var (
			src string
			h   ExternalHost
		)
		if err := rows.Scan(&src, &h.Host, &h.ReportedThrough, &h.FirstReportedAt, &h.PushedAt); err != nil {
			return nil, err
		}
		if h.ReportedThrough != nil {
			at := h.ReportedThrough.UTC()
			h.ReportedThrough = &at
		}
		if h.FirstReportedAt != nil {
			at := h.FirstReportedAt.UTC()
			h.FirstReportedAt = &at
		}
		h.PushedAt = h.PushedAt.UTC()
		out[src] = append(out[src], h)
	}
	return out, rows.Err()
}

// setMeterLink stores (or with nil removes) a source's meter credential. The
// credential must be a subscription credential of the organisation: a metered
// API key has no meter to share, and a secret no runtime of the organisation
// uses has no readings.
func setMeterLink(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, source string, c *MeterCredential) error {
	if c == nil {
		_, err := tx.Exec(ctx, `DELETE FROM external_usage_meters WHERE org_id = $1 AND source = $2`, orgID, source)
		return err
	}
	if c.SecretKey == "" || c.SecretSlot < 0 || c.SecretSlot > 32767 {
		return ErrInvalidExternal{Reason: "meter_credential needs a secret_key and a small non-negative secret_slot"}
	}
	var subscription, known bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(bool_or(rc.kind = $4), false), count(*) > 0
		FROM runtime_credentials rc JOIN runtimes r ON r.id = rc.runtime_id
		WHERE r.org_id = $1 AND rc.secret_key = $2 AND rc.secret_slot = $3`,
		orgID, c.SecretKey, c.SecretSlot, daemon.CredSubscription).Scan(&subscription, &known); err != nil {
		return err
	}
	if !known {
		return ErrInvalidExternal{Reason: "meter_credential names no credential of this organisation"}
	}
	if !subscription {
		return ErrInvalidExternal{Reason: "meter_credential is a metered API key, which has no subscription meter"}
	}
	_, err := tx.Exec(ctx, `INSERT INTO external_usage_meters (org_id, source, secret_key, secret_slot, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (org_id, source) DO UPDATE SET secret_key = EXCLUDED.secret_key,
			secret_slot = EXCLUDED.secret_slot, updated_at = EXCLUDED.updated_at`,
		orgID, source, c.SecretKey, c.SecretSlot)
	return err
}

// ExternalDeleteResult is what deleting a source removed.
type ExternalDeleteResult struct {
	Source       string `json:"source"`
	Deleted      int64  `json:"deleted"`
	MeterRemoved bool   `json:"meter_credential_removed"`
}

// DeleteExternalUsage removes every row of one source of the organisation,
// its meter link and its hosts' watermarks.
func (s *Store) DeleteExternalUsage(ctx context.Context, orgID uuid.UUID, source string) (ExternalDeleteResult, error) {
	res := ExternalDeleteResult{Source: source}
	if err := ValidateExternalSource(source); err != nil {
		return res, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM external_usage WHERE org_id = $1 AND source = $2`, orgID, source)
	if err != nil {
		return res, err
	}
	res.Deleted = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM external_usage_meters WHERE org_id = $1 AND source = $2`, orgID, source)
	if err != nil {
		return res, err
	}
	res.MeterRemoved = tag.RowsAffected() > 0
	if _, err := tx.Exec(ctx, `DELETE FROM external_usage_hosts WHERE org_id = $1 AND source = $2`, orgID, source); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// --- Reading ---------------------------------------------------------------------

// ExternalUsage is the outside-covey series of the cost cells.
type ExternalUsage struct {
	// Sources: every source with calls in the window or a meter link, by
	// label.
	Sources []ExternalSource `json:"sources"`
	// Cells: per bucket, source and origin, the calls' figures (F2).
	Cells []ExternalCell `json:"cells"`
	// Meter: the share of the meters' real cost the calls make (F3).
	Meter ExternalMeter `json:"meter"`
}

// ExternalSource is one source over the window.
type ExternalSource struct {
	Source string `json:"source"`
	// MeterCredential: the subscription credential the source shares its
	// meter with; nil without one (no meter share). The link is not dated:
	// it applies to every call of the source, also the earlier ones.
	MeterCredential *MeterCredential `json:"meter_credential"`
	Calls           int64            `json:"calls"`
	// ListUSD: F1 over the priced calls; nil when none was priced.
	ListUSD *float64 `json:"list_usd"`
	// ReportedUSD: Σ reported_usd of the calls that carried one; nil when
	// none did.
	ReportedUSD *float64 `json:"reported_usd"`
	// UnpricedTokens/UnpricedCalls: calls of a model the list does not know
	// (UnpricedModels), with no figure.
	UnpricedTokens int64    `json:"unpriced_tokens"`
	UnpricedCalls  int64    `json:"unpriced_calls"`
	UnpricedModels []string `json:"unpriced_models"`
	// LeadUSD: the lead price (leadUSD) — each call's reported_usd where it
	// carried one, else its list price (F1); nil when no call had either.
	// LeadUnpricedCalls / LeadUnpricedTokens: the calls with neither, which
	// are in no lead figure (never $0).
	LeadUSD            *float64   `json:"lead_usd"`
	LeadUnpricedCalls  int64      `json:"lead_unpriced_calls"`
	LeadUnpricedTokens int64      `json:"lead_unpriced_tokens"`
	FirstAt            *time.Time `json:"first_at"`
	LastAt             *time.Time `json:"last_at"`
	// Hosts: the source's machines and how far each has reported.
	Hosts []ExternalHost `json:"hosts"`
}

// ExternalCell is one bucket × source × origin.
type ExternalCell struct {
	Period time.Time `json:"period"`
	Source string    `json:"source"`
	// Origin: main | subagent | workflow; nil where the uploader did not say.
	Origin *string `json:"origin"`
	Calls  int64   `json:"calls"`
	// ListUSD: F1 summed over the cell's priced calls (F2); nil when none of
	// them was priced. Measured splits it by cost type (cache_read,
	// cache_write, input, output), as the agents' cells do.
	ListUSD  *float64           `json:"list_usd"`
	Measured map[string]float64 `json:"measured"`
	// ReportedUSD: Σ reported_usd where sent (ReportedCalls of Calls); nil
	// when no call carried one.
	ReportedUSD   *float64 `json:"reported_usd"`
	ReportedCalls int64    `json:"reported_calls"`
	// Tokens per type over every call of the cell, priced or not.
	Tokens         map[string]int64 `json:"tokens"`
	UnpricedTokens int64            `json:"unpriced_tokens"`
	UnpricedCalls  int64            `json:"unpriced_calls"`
	// LeadUSD, LeadUnpricedCalls, LeadUnpricedTokens: as ExternalSource's,
	// over the cell's calls. The calls whose lead is their list price are
	// Calls − ReportedCalls − LeadUnpricedCalls.
	LeadUSD            *float64 `json:"lead_usd"`
	LeadUnpricedCalls  int64    `json:"lead_unpriced_calls"`
	LeadUnpricedTokens int64    `json:"lead_unpriced_tokens"`
}

// The lead price (#120, the human's rule for the cost page): one list-price
// figure per call, the price Claude Code reported for it where it reported
// one, else covey's list price for its tokens. covey's lines always carry
// Claude Code's figure (model_usage.engine_usd), so their lead price is it;
// an outside call carries one only sometimes (reported_usd). Every list
// figure of the meter split has a lead counterpart over the same calls,
// spans and cuts (the *_lead_usd fields), so a multiple of the lead figure
// divides the same use as its list twin.

// Why a step is not split (ExternalMeterStep.Reason).
const (
	// SplitNotReported: a machine of the linked sources that was active
	// before the reading has not reported through it, or a linked source has
	// no active machine.
	SplitNotReported = "not_reported"
	// SplitNoReportedCall: the meter moved, but neither covey nor a linked
	// source has a call in the step's span — mostly the meter's lag after
	// use. It joins its chunk (F3c) and is split by the chunk's share like
	// the chunk's other steps (Split is true, the reason stays) — the chunk
	// before it when it directly follows one that closed at its size. In a
	// chunk with no list at all (no chunk next to it) it is unattributed
	// whole.
	SplitNoReportedCall = "moved_without_reported_call"
	// SplitOutsideUnpriced: the linked sources have calls in the step's span,
	// but none of them has a price (no list price, no reported_usd), so L_out
	// is unknown, not 0: the step is never booked to covey.
	SplitOutsideUnpriced = "outside_unpriced"
	// SplitUnpriced: points no plan prices; the step is out of the dent.
	SplitUnpriced = "unpriced"
	// SplitIdle: a reported reading that did not move, with no priced call
	// on either side: nothing to split. It is neither a split nor an unsplit
	// step; it joins its chunk (F3c), whose ranges see it at the chunk's
	// share and its own price; without a plan its $ coefficient is 0.
	SplitIdle = "idle"
	// SplitUsage: a reading taken while the credential's basis was usage
	// (0151): its points are usage_points, whose money is the usage
	// charges, so the step is out of the dent and of every chunk whole —
	// moved or not, as a step no plan prices — and it is not unpriced.
	SplitUsage = "usage"
)

// ExternalMeter is F3 over the window: per reading step of every seat, the
// hours and seats they sum to, and the whole. It is filled once a source
// names the meter it shares; then every subscription seat with readings
// takes part, linked or not, so that the headline holds the whole dent in
// the meters. A seat no source names has no outside calls to wait for: its
// steps are split with L_out = 0, so all of its movement is covey's (F3).
type ExternalMeter struct {
	Steps       []ExternalMeterStep  `json:"steps"`
	Hours       []ExternalMeterHour  `json:"hours"`
	Credentials []ExternalMeterTotal `json:"credentials"`
	ExternalMeterSums
}

// ExternalMeterSums are the figures a seat and the whole share.
type ExternalMeterSums struct {
	// RealUSD: Σ R over the priced steps — the dent. ExternalRealUSD /
	// CoveyRealUSD: Σ R_out / Σ R_cov over the split steps;
	// UnattributedRealUSD: Σ R_un (F3c), the chunks' points beyond the rate
	// and the chunks with no list at all whole; UnsplitRealUSD: Σ R over
	// the priced steps that were not split and are not unattributed whole.
	// The four add up to RealUSD.
	RealUSD             float64 `json:"real_usd"`
	ExternalRealUSD     float64 `json:"external_real_usd"`
	CoveyRealUSD        float64 `json:"covey_real_usd"`
	UnattributedRealUSD float64 `json:"unattributed_real_usd"`
	UnsplitRealUSD      float64 `json:"unsplit_real_usd"`
	// UnattributedPoints: the points of UnattributedRealUSD;
	// UnattributedSteps: the steps with some.
	UnattributedPoints float64 `json:"unattributed_points"`
	UnattributedSteps  int     `json:"unattributed_steps"`
	// UnattributedNoCallRealUSD / UnattributedNoCallPoints: the part of the
	// unattributed movement that is a chunk with no list at all (the meter
	// moved and nobody reported a call: lag or another machine), an
	// estimate; the rest, beyond the rate k, is a lower bound.
	UnattributedNoCallRealUSD float64 `json:"unattributed_no_call_real_usd"`
	UnattributedNoCallPoints  float64 `json:"unattributed_no_call_points"`
	// IncompleteChunks: split chunks whose use is not all in their list —
	// outside calls without a price, or covey bookings without a line. They
	// set no rate and have no unattributed part, so their parts may hold
	// use of another machine, and the use of those calls is shared at the
	// priced calls' shares. A chunk with no list but covey bookings without
	// a line is one: covey's whole, never unattributed.
	IncompleteChunks int `json:"incomplete_chunks"`
	// Share: ExternalRealUSD over the split steps' real cost; nil without
	// one.
	Share *float64 `json:"share"`
	// SplitExternalListUSD / SplitCoveyListUSD: L_out and L_cov over the
	// split steps, the list prices the three multiples divide.
	SplitExternalListUSD float64 `json:"split_external_list_usd"`
	SplitCoveyListUSD    float64 `json:"split_covey_list_usd"`
	// SplitExternalLeadUSD / SplitCoveyLeadUSD: the same at the lead price.
	SplitExternalLeadUSD float64 `json:"split_external_lead_usd"`
	SplitCoveyLeadUSD    float64 `json:"split_covey_lead_usd"`
	// FloorListUSD: the list a floor ("at least M×") may divide, over the
	// priced steps (the steps the page's metered list sums, covey's and the
	// outside sessions' together): their L_out + L_cov without the parts of
	// covey lines that began before since and without the calls of the
	// first step's span that lie before since (review of e82d640f, finding
	// 3). A line that began before the range may have moved the meter before
	// its first reading, so its part in the range is no use the range's real
	// cost is known to hold. SplitExternalFloorListUSD /
	// SplitCoveyFloorListUSD: the same over the split steps, per party (the
	// parts' floors). Without since cutting a line or a span they equal
	// the lists; the ≈ multiples keep dividing the lists themselves.
	FloorListUSD              float64 `json:"floor_list_usd"`
	SplitExternalFloorListUSD float64 `json:"split_external_floor_list_usd"`
	SplitCoveyFloorListUSD    float64 `json:"split_covey_floor_list_usd"`
	// FloorLeadUSD: FloorListUSD at the lead price, with the same parts left
	// out.
	FloorLeadUSD float64 `json:"floor_lead_usd"`
	// Points: the priced movement, the dent in points. SplitPoints: the
	// split steps' part of it, SplitExternalPoints / SplitCoveyPoints the
	// share-weighted parts of their attributed points (Σ s_c·(points_i −
	// u_i) and Σ (1 − s_c)·(points_i − u_i), s_c the step's chunk's share,
	// u_i the step's unattributed points).
	// Points of seats on different plans are not one unit: a sum over seats
	// is a figure only while PointRateMixed is false.
	Points              float64 `json:"points"`
	SplitPoints         float64 `json:"split_points"`
	SplitExternalPoints float64 `json:"split_external_points"`
	SplitCoveyPoints    float64 `json:"split_covey_points"`
	// SplitRuns: since F3c the number of split chunks (the field keeps its
	// name). The meter shows whole percents and trails use, so each chunk's
	// movement is known to S_c = 2 points (a point for the whole percents at
	// its two ends, one of lag at its end), the S_c of k and U_c:
	// SplitSlackPoints / SplitSlackUSD, the attributed whole's range. A
	// part's range is S_c times ½·TV over each chunk's steps of its
	// coefficient, TV = f_1 + Σ|f_{k+1} − f_k| + f_n with f_k = s_c·rate_k
	// (s_c the chunk's outside share Lo_c / L_c, 1 − s_c for covey, 1 for
	// the whole; rate_k each reading's own plan price per point, 0 without a
	// plan, so an idle reading at another price widens it — #126 finding
	// 26): 2·s_c, 2·(1 − s_c) and 2 points per chunk,
	// Split{External,Covey}Slack{Points,USD}. Within a chunk s_c and the
	// attributed fraction a_c = 1 − U_c / P_c are one number each, so a
	// part's true coefficient a_c·s_c·rate_k has a TV at most the one given.
	// Across chunks a_c changes, and a sum over contiguous chunks would not
	// bound it: each chunk is its own term, so the ranges stay wide enough.
	// A chunk with spread lines adds the range of its share itself
	// (chunkShareRange) to the outside sessions' and covey's.
	SplitRuns                int     `json:"split_runs"`
	SplitSlackPoints         float64 `json:"split_slack_points"`
	SplitSlackUSD            float64 `json:"split_slack_usd"`
	SplitExternalSlackPoints float64 `json:"split_external_slack_points"`
	SplitExternalSlackUSD    float64 `json:"split_external_slack_usd"`
	SplitCoveySlackPoints    float64 `json:"split_covey_slack_points"`
	SplitCoveySlackUSD       float64 `json:"split_covey_slack_usd"`
	// PointRate: the dearest price per point among the priced steps that
	// moved; PointRateMixed: some of them moved at another price.
	PointRate      float64 `json:"point_rate"`
	PointRateMixed bool    `json:"point_rate_mixed"`
	// StepCount: the readings in the window with a span (the first reading
	// of a meter, its baseline, has none); SplitSteps / UnsplitSteps of them,
	// UnsplitSteps counting the priced ones that moved. An idle step
	// (SplitIdle) is in neither: a seat whose only reported steps are idle
	// has no split. A step that moved with no call (SplitNoReportedCall) is
	// a split step when its chunk has list price (it takes the chunk's
	// share) and in neither when not (unattributed whole,
	// UnattributedSteps).
	StepCount    int `json:"step_count"`
	SplitSteps   int `json:"split_steps"`
	UnsplitSteps int `json:"unsplit_steps"`
	// OutsideUnpricedSteps / OutsideUnpricedRealUSD: the part of UnsplitSteps
	// / UnsplitRealUSD whose reason is SplitOutsideUnpriced (the outside
	// calls of the span all had no price), for the page to word apart.
	OutsideUnpricedSteps   int     `json:"outside_unpriced_steps"`
	OutsideUnpricedRealUSD float64 `json:"outside_unpriced_real_usd"`
	// UnpricedSteps / UnpricedPoints: steps that moved while no plan priced
	// the seat. They stay out of the dent whole, never as $0.
	UnpricedSteps  int     `json:"unpriced_steps"`
	UnpricedPoints float64 `json:"unpriced_points"`
	// UsageSteps / UsagePoints: steps read while the seat's basis was usage
	// (SplitUsage), idle ones included, and their points: out of the dent
	// and of the split, neither priced nor unpriced (their money is the
	// usage charges). Absent without such a basis.
	UsageSteps  int     `json:"usage_steps,omitempty"`
	UsagePoints float64 `json:"usage_points,omitempty"`
	// UnpricedCalls / UnpricedTokens: linked outside calls in the steps'
	// spans with neither a list price nor a reported_usd: not in L_out.
	UnpricedCalls  int64 `json:"unpriced_calls"`
	UnpricedTokens int64 `json:"unpriced_tokens"`
	// UnmeteredListUSD: L_out of linked calls in the window that lie in no
	// step's span (after the seat's last reading, or with no reading at
	// all): no real share yet.
	UnmeteredListUSD float64 `json:"unmetered_list_usd"`
	// UnmeteredCoveyListUSD: L_cov likewise, covey's lines on the seat in the
	// window that lie in no step's span: no real share yet either.
	UnmeteredCoveyListUSD float64 `json:"unmetered_covey_list_usd"`
	// Unmetered*BeforeListUSD: the part of the two above that lies before
	// the seat's first reading in the range (at or before the first step's
	// previous reading, From): no meter data for that time, so it never gets
	// a real share. The rest lies after the last reading: its real share is
	// pending until the meter is read again. A seat with no step has no
	// first reading to be before: its unmetered list is all pending.
	UnmeteredBeforeListUSD      float64 `json:"unmetered_before_list_usd"`
	UnmeteredCoveyBeforeListUSD float64 `json:"unmetered_covey_before_list_usd"`
	// The four unmetered figures at the lead price.
	UnmeteredLeadUSD            float64 `json:"unmetered_lead_usd"`
	UnmeteredCoveyLeadUSD       float64 `json:"unmetered_covey_lead_usd"`
	UnmeteredBeforeLeadUSD      float64 `json:"unmetered_before_lead_usd"`
	UnmeteredCoveyBeforeLeadUSD float64 `json:"unmetered_covey_before_lead_usd"`
	// SourceParts: the outside part per source (F3c per source): source s's
	// real part of a split step is the step's outside real × Lo_s,c / Lo_c,
	// its share of the chunk's outside list; its list is Σ Lo_s over the
	// split steps. They add up to ExternalRealUSD and SplitExternalListUSD.
	// A seat lists every source linked to it, by name; the whole meter every
	// source of its seats. (Not "sources": a seat's Sources names the linked
	// sources already.)
	SourceParts []ExternalMeterSourcePart `json:"source_parts"`
}

// ExternalMeterSourcePart is one outside source's part of the split movement.
type ExternalMeterSourcePart struct {
	Source  string  `json:"source"`
	ListUSD float64 `json:"list_usd"`
	RealUSD float64 `json:"real_usd"`
}

// addSourceParts merges parts into a list by source, sorted by source.
func addSourceParts(into []ExternalMeterSourcePart, parts ...ExternalMeterSourcePart) []ExternalMeterSourcePart {
	for _, p := range parts {
		i := sort.Search(len(into), func(i int) bool { return into[i].Source >= p.Source })
		if i == len(into) || into[i].Source != p.Source {
			into = append(into, ExternalMeterSourcePart{})
			copy(into[i+1:], into[i:])
			into[i] = ExternalMeterSourcePart{Source: p.Source}
		}
		into[i].ListUSD += p.ListUSD
		into[i].RealUSD += p.RealUSD
	}
	return into
}

// ExternalMeterStep is one reading of one seat's meter and its split.
type ExternalMeterStep struct {
	// At: the reading, where the step is placed; From: the previous one.
	At         time.Time `json:"at"`
	From       time.Time `json:"from"`
	SecretKey  string    `json:"secret_key"`
	SecretSlot int       `json:"secret_slot"`
	Points     float64   `json:"points"`
	// Reset: the reading began a new week.
	Reset bool `json:"reset"`
	// RealUSD: R, the priced movement; nil when no plan prices its points,
	// and for a reading under the usage basis (Reason SplitUsage).
	RealUSD *float64 `json:"real_usd"`
	// ExternalListUSD: L_out; CoveyListUSD: L_cov over (From, At].
	ExternalListUSD float64 `json:"external_list_usd"`
	CoveyListUSD    float64 `json:"covey_list_usd"`
	// ExternalLeadUSD / CoveyLeadUSD: the same calls and lines at their lead
	// price (reported where they carry one, else list).
	ExternalLeadUSD float64 `json:"external_lead_usd"`
	CoveyLeadUSD    float64 `json:"covey_lead_usd"`
	// Reported: every active machine of the linked sources has reported
	// through At (stepReported). A seat no source is linked to has nothing
	// that could report on it: its steps are reported, with L_out = 0.
	Reported bool `json:"reported"`
	// Split: R_out and R_cov are set and, with UnattributedRealUSD (its
	// chunk's part, F3c), add up to R; they follow the chunk's list share,
	// not the step's. A split step may still carry Reason
	// SplitNoReportedCall (it moved with no call and took its chunk's
	// share). Otherwise Reason says why not: a step that moved with no call
	// in a chunk with no list has R unattributed whole, any other priced
	// step R unsplit. covey + outside + unattributed + unsplit = R in every
	// step.
	Split               bool     `json:"split"`
	Reason              string   `json:"reason,omitempty"`
	ExternalRealUSD     *float64 `json:"external_real_usd"`
	CoveyRealUSD        *float64 `json:"covey_real_usd"`
	UnattributedRealUSD float64  `json:"unattributed_real_usd"`
	UnattributedPoints  float64  `json:"unattributed_points"`
	UnsplitRealUSD      float64  `json:"unsplit_real_usd"`
	// SourceParts: a split step's outside part per source, R_out × Lo_s,c /
	// Lo_c, with the source's own list in the step; absent on a step that
	// is not split or whose chunk has no outside list.
	SourceParts []ExternalMeterSourcePart `json:"source_parts,omitempty"`
}

// ExternalMeterHour is one UTC hour of one seat: the sums of the steps
// placed in it.
type ExternalMeterHour struct {
	Hour       time.Time `json:"hour"`
	SecretKey  string    `json:"secret_key"`
	SecretSlot int       `json:"secret_slot"`
	// RealUSD: Σ R of its priced steps; nil when none was priced. Points:
	// their movement.
	RealUSD             *float64 `json:"real_usd"`
	Points              float64  `json:"points"`
	ExternalListUSD     float64  `json:"external_list_usd"`
	CoveyListUSD        float64  `json:"covey_list_usd"`
	ExternalRealUSD     float64  `json:"external_real_usd"`
	CoveyRealUSD        float64  `json:"covey_real_usd"`
	UnattributedRealUSD float64  `json:"unattributed_real_usd"`
	UnattributedPoints  float64  `json:"unattributed_points"`
	UnsplitRealUSD      float64  `json:"unsplit_real_usd"`
	Steps               int      `json:"steps"`
	SplitSteps          int      `json:"split_steps"`
	UnattributedSteps   int      `json:"unattributed_steps"`
	UnpricedPoints      float64  `json:"unpriced_points"`
	// UsagePoints: the points of its steps under the usage basis (0151),
	// not in RealUSD and not unpriced. Absent without such a basis.
	UsagePoints float64 `json:"usage_points,omitempty"`
	// SourceParts: the steps' outside parts per source, summed.
	SourceParts []ExternalMeterSourcePart `json:"source_parts"`
}

// ExternalMeterTotal is one seat credential over the window.
type ExternalMeterTotal struct {
	SecretKey  string `json:"secret_key"`
	SecretSlot int    `json:"secret_slot"`
	// MeterSource: the reading source R is taken from (the one the page
	// picks: most readings, usage_cmd before manual before inband); empty
	// without readings in the window.
	MeterSource string `json:"meter_source"`
	// Sources: the outside sources linked to this meter; none for a seat
	// no source names, whose steps are reported with L_out = 0.
	Sources []string `json:"sources"`
	// HoursWithReading: the UTC hours a step is placed in.
	HoursWithReading int `json:"hours_with_reading"`
	// PointsPerListUSD: k of F3c at the seat's latest plan price that has
	// one: the lowest rate (points per list $) of a stretch of adjacent
	// chunks with every call priced that moved 10 points or more, at the
	// stretch's upper end; nil without such a stretch, when only chunks with
	// no call are unattributed. PlanRates: k per plan price, in the order the prices
	// first appear (points are percent of one plan's limit, so each plan
	// has its own).
	PointsPerListUSD *float64                `json:"points_per_list_usd"`
	PlanRates        []ExternalMeterPlanRate `json:"plan_rates"`
	// PointsPerListOlderPlanUSD: the plan price per point PointsPerListUSD
	// is at, when that is not the seat's latest plan price (no stretch at
	// the latest one set a k yet); nil otherwise.
	PointsPerListOlderPlanUSD *float64 `json:"points_per_list_older_plan_usd"`
	ExternalMeterSums
	// Agents: covey's real share per agent (finding 22 on #126).
	Agents []ExternalMeterAgent `json:"agents"`
}

// ExternalMeterPlanRate is k (F3c) at one plan price per point of a seat;
// PointsPerListUSD nil when no chunk at that price could set it.
type ExternalMeterPlanRate struct {
	USDPerPoint      float64  `json:"usd_per_point"`
	PointsPerListUSD *float64 `json:"points_per_list_usd"`
}

// ExternalMeterAgent is one agent's part of covey's share of a seat:
// RealUSD = Σ over the chunks with Lc_c > 0 of R_cov,c · L_a,c / Lc_c
// (R_cov,c after the chunk's unattributed part, F3c; L_a,c the agent's list
// in the chunk), so the agents add up to the seat's CoveyRealUSD.
// SplitListUSD: the agent's list over the split steps; UnsplitListUSD: over
// the steps that were not split, which give it no real share.
type ExternalMeterAgent struct {
	AgentID        uuid.UUID `json:"agent_id"`
	SplitListUSD   float64   `json:"split_list_usd"`
	RealUSD        float64   `json:"real_usd"`
	UnsplitListUSD float64   `json:"unsplit_list_usd"`
}

// externalTokens are one group's summed counts.
type externalTokens struct {
	calls                       int64
	input, output, cr, w5m, w1h int64
	reported                    *float64
	reportedCalls               int64
}

func (t externalTokens) model() ModelTokens {
	return ModelTokens{Input: t.input, Output: t.output, CacheRead: t.cr, CacheWrite5m: t.w5m, CacheWrite1h: t.w1h}
}

func (t externalTokens) total() int64 { return t.input + t.output + t.cr + t.w5m + t.w1h }

// ExternalListUSD is F1 for one call or a sum of calls of one model: nil when
// the list does not know the model.
func ExternalListUSD(model string, t ModelTokens) *float64 {
	p, ok := ListPrice(model)
	if !ok {
		return nil
	}
	usd := p.Cost(t)
	return &usd
}

// externalFor reads the outside-covey series of the window: cells at the
// chart's bucket, and the meter share per reading step, whatever the bucket.
// coverage and plans are the cost cells' own: the share reads the meter the
// page reads, priced as the page prices it.
func (s *Store) externalFor(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time,
	coverage []MeterCoverage, plans []CredentialPlan, bases []CredentialBasis) (ExternalUsage, error) {
	out := ExternalUsage{Sources: []ExternalSource{}, Cells: []ExternalCell{}, Meter: emptyMeter()}

	links := map[string]MeterCredential{}
	lrows, err := s.pool.Query(ctx, `SELECT source, secret_key, secret_slot FROM external_usage_meters WHERE org_id = $1`, orgID)
	if err != nil {
		return out, err
	}
	for lrows.Next() {
		var (
			src  string
			m    MeterCredential
			slot int16
		)
		if err := lrows.Scan(&src, &m.SecretKey, &slot); err != nil {
			lrows.Close()
			return out, err
		}
		m.SecretSlot = int(slot)
		links[src] = m
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return out, err
	}

	// Cells: one query grouped by bucket, source, origin and model; priced
	// per model here, so F1 comes from ListPrice like the agents' figure.
	type ckey struct {
		p      time.Time
		source string
		origin string
		hasOr  bool
	}
	cells := map[ckey]*ExternalCell{}
	sources := map[string]*ExternalSource{}
	unpriced := map[string]map[string]bool{}
	source := func(name string) *ExternalSource {
		src, ok := sources[name]
		if !ok {
			src = &ExternalSource{Source: name, UnpricedModels: []string{}, Hosts: []ExternalHost{}}
			if m, ok := links[name]; ok {
				m := m
				src.MeterCredential = &m
			}
			sources[name] = src
		}
		return src
	}
	rows, err := s.pool.Query(ctx, `SELECT `+periodSQL(bucket, "ts")+`, source, origin, model, count(*),
		SUM(input), SUM(output), SUM(cache_read), SUM(cache_write_5m), SUM(cache_write_1h),
		SUM(reported_usd)::float8, count(reported_usd), MIN(ts), MAX(ts)
		FROM external_usage
		WHERE org_id = $1 AND ts >= $2
		GROUP BY 1, 2, 3, 4, reported_usd IS NULL`, orgID, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var (
			p           time.Time
			name, model string
			origin      *string
			t           externalTokens
			first, last time.Time
		)
		if err := rows.Scan(&p, &name, &origin, &model, &t.calls, &t.input, &t.output, &t.cr, &t.w5m, &t.w1h,
			&t.reported, &t.reportedCalls, &first, &last); err != nil {
			rows.Close()
			return out, err
		}
		k := ckey{p: p.UTC(), source: name}
		if origin != nil {
			k.origin, k.hasOr = *origin, true
		}
		c, ok := cells[k]
		if !ok {
			c = &ExternalCell{Period: k.p, Source: name, Measured: map[string]float64{}, Tokens: map[string]int64{}}
			if k.hasOr {
				o := k.origin
				c.Origin = &o
			}
			cells[k] = c
		}
		src := source(name)
		c.Calls += t.calls
		src.Calls += t.calls
		c.Tokens["input"] += t.input
		c.Tokens["output"] += t.output
		c.Tokens["cache_read"] += t.cr
		c.Tokens["cache_write_5m"] += t.w5m
		c.Tokens["cache_write_1h"] += t.w1h
		if t.reported != nil {
			c.ReportedUSD = addUSD(c.ReportedUSD, *t.reported)
			src.ReportedUSD = addUSD(src.ReportedUSD, *t.reported)
			c.ReportedCalls += t.reportedCalls
		}
		first, last = first.UTC(), last.UTC()
		if src.FirstAt == nil || first.Before(*src.FirstAt) {
			src.FirstAt = &first
		}
		if src.LastAt == nil || last.After(*src.LastAt) {
			src.LastAt = &last
		}
		pr, ok := ListPrice(model)
		// The lead price: a group is all reported or all not (the GROUP BY).
		switch {
		case t.reportedCalls > 0 && t.reported != nil:
			c.LeadUSD = addUSD(c.LeadUSD, *t.reported)
			src.LeadUSD = addUSD(src.LeadUSD, *t.reported)
		case ok:
			usd := pr.Cost(t.model())
			c.LeadUSD = addUSD(c.LeadUSD, usd)
			src.LeadUSD = addUSD(src.LeadUSD, usd)
		default:
			c.LeadUnpricedCalls += t.calls
			c.LeadUnpricedTokens += t.total()
			src.LeadUnpricedCalls += t.calls
			src.LeadUnpricedTokens += t.total()
		}
		if !ok {
			c.UnpricedTokens += t.total()
			c.UnpricedCalls += t.calls
			src.UnpricedTokens += t.total()
			src.UnpricedCalls += t.calls
			if unpriced[name] == nil {
				unpriced[name] = map[string]bool{}
			}
			unpriced[name][model] = true
			continue
		}
		usd := pr.Cost(t.model())
		c.ListUSD = addUSD(c.ListUSD, usd)
		src.ListUSD = addUSD(src.ListUSD, usd)
		c.Measured[TypeCacheRead] += float64(t.cr) * pr.CacheRead / 1e6
		c.Measured[TypeCacheWrite] += (float64(t.w5m)*pr.CacheWrite5m + float64(t.w1h)*pr.CacheWrite1h) / 1e6
		c.Measured[TypeInput] += float64(t.input) * pr.Input / 1e6
		c.Measured[TypeOutput] += float64(t.output) * pr.Output / 1e6
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for name := range links {
		source(name)
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	hosts, err := externalHosts(ctx, s.pool, orgID, names)
	if err != nil {
		return out, err
	}
	for name, src := range sources {
		src.Hosts = append(src.Hosts, hosts[name]...)
		for m := range unpriced[name] {
			src.UnpricedModels = append(src.UnpricedModels, m)
		}
		sort.Strings(src.UnpricedModels)
		out.Sources = append(out.Sources, *src)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Source < out.Sources[j].Source })
	for _, c := range cells {
		out.Cells = append(out.Cells, *c)
	}
	sort.Slice(out.Cells, func(i, j int) bool {
		a, b := out.Cells[i], out.Cells[j]
		if !a.Period.Equal(b.Period) {
			return a.Period.Before(b.Period)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return originOrder(a.Origin) < originOrder(b.Origin)
	})

	// Until a source names the meter it shares, there is nothing to split
	// the meters by and no step to call unreported: the page stays as it was
	// (a machine billed per use shares no meter).
	if len(links) == 0 {
		return out, nil
	}
	readings, err := s.meterRows(ctx, orgID, since)
	if err != nil {
		return out, err
	}
	seats, linked := meterSeats(links, coverage)
	var totals []ExternalMeterTotal
	var steps []ExternalMeterStep
	for _, seat := range seats {
		in, cov, err := s.seatSteps(ctx, orgID, since, seat, linked[seat], readings, coverage, plans, bases, hosts)
		if err != nil {
			return out, err
		}
		tot, st := splitSeatSince(seat, linked[seat], in, since)
		tot.MeterSource = cov.Source
		n := sort.Search(len(in), func(i int) bool { return !in[i].At.Before(since) })
		um, err := s.unmeteredList(ctx, orgID, since, linked[seat], in[n:])
		if err != nil {
			return out, err
		}
		tot.UnmeteredListUSD, tot.UnmeteredBeforeListUSD, tot.UnmeteredLeadUSD, tot.UnmeteredBeforeLeadUSD = um.list, um.beforeList, um.lead, um.beforeLead
		if um, err = s.unmeteredCoveyList(ctx, orgID, since, seat, in[n:]); err != nil {
			return out, err
		}
		tot.UnmeteredCoveyListUSD, tot.UnmeteredCoveyBeforeListUSD, tot.UnmeteredCoveyLeadUSD, tot.UnmeteredCoveyBeforeLeadUSD = um.list, um.beforeList, um.lead, um.beforeLead
		totals, steps = append(totals, tot), append(steps, st...)
	}
	out.Meter = meterSums(totals, steps)
	return out, nil
}

func emptyMeter() ExternalMeter {
	return ExternalMeter{Steps: []ExternalMeterStep{}, Hours: []ExternalMeterHour{}, Credentials: []ExternalMeterTotal{},
		ExternalMeterSums: ExternalMeterSums{SourceParts: []ExternalMeterSourcePart{}}}
}

// originOrder sorts a cell's origin: main, subagent, workflow, unsaid.
func originOrder(o *string) string {
	if o == nil {
		return "~"
	}
	return *o
}

// meterSeat is one subscription credential of the meter share.
type meterSeat struct {
	key  string
	slot int
}

// meterSeats are the credentials F3 covers once a source names a meter, in
// order: every linked one and every subscription seat with a weekly reading
// in the window, linked or not — with the sources linked to each. The one
// list every part of the share is read for.
func meterSeats(links map[string]MeterCredential, coverage []MeterCoverage) ([]meterSeat, map[meterSeat][]string) {
	linked := map[meterSeat][]string{}
	for name, m := range links {
		c := meterSeat{m.SecretKey, m.SecretSlot}
		linked[c] = append(linked[c], name)
	}
	for _, cv := range coverage {
		if cv.CredentialKind == "api_key" || cv.WindowKind != WindowSevenDay {
			continue
		}
		c := meterSeat{cv.SecretKey, cv.SecretSlot}
		if _, ok := linked[c]; !ok {
			linked[c] = nil
		}
	}
	seats := make([]meterSeat, 0, len(linked))
	for c := range linked {
		sort.Strings(linked[c])
		seats = append(seats, c)
	}
	sort.Slice(seats, func(i, j int) bool {
		if seats[i].key != seats[j].key {
			return seats[i].key < seats[j].key
		}
		return seats[i].slot < seats[j].slot
	})
	return seats, linked
}

// pickCoverage is the meter coverage a credential's real cost is read from,
// the rule of the page (pickMeter in web/src/pages/costFigures.ts): the
// weekly all-models window, the source with the most readings, usage_cmd
// before manual before inband on a tie. Two sources read the same meter, so
// never both.
func pickCoverage(coverage []MeterCoverage, key string, slot int) (MeterCoverage, bool) {
	rank := func(s string) int {
		switch s {
		case "usage_cmd":
			return 0
		case "manual":
			return 1
		case "inband":
			return 2
		}
		return 3
	}
	var best MeterCoverage
	found := false
	for _, c := range coverage {
		if c.SecretKey != key || c.SecretSlot != slot || c.WindowKind != WindowSevenDay {
			continue
		}
		if !found || c.Readings > best.Readings || (c.Readings == best.Readings && rank(c.Source) < rank(best.Source)) {
			best, found = c, true
		}
	}
	return best, found
}

// shareStep is one reading step of a seat with everything F3 needs: the
// movement and its price, the two lists of its span, the agents' part of
// L_cov, and whether the outside calls were reported through it.
type shareStep struct {
	At, From time.Time
	Points   float64
	// Rate: the plan's price per point at the reading; nil without a plan.
	Rate  *float64
	Reset bool
	LOut  float64
	LCov  float64
	// LOutLead / LCovLead: the same calls and lines at their lead price
	// (reported where they carry one, else list); a spread line's alike.
	LOutLead, LCovLead float64
	// LCovSpread: the part of LCov that spreadLine booked here for lines
	// spread over several spans, in proportion to their time in this one.
	// The proportional part serves the shares only: for k and U_c a spread
	// line counts whole or not at all (Spread, chunkRates, chunkListHigh).
	LCovSpread float64
	// Spread: the lines spread over this step's span and earlier ones,
	// recorded at the step their result time falls in.
	Spread []spreadSpan
	// SrcOut: each linked source's part of LOut.
	SrcOut map[string]float64
	// Agents: each agent's part of LCov.
	Agents                        map[uuid.UUID]float64
	UnpricedCalls, UnpricedTokens int64
	// UnlinedBookings: covey bookings on the seat in the span that no line
	// was linked to (an older daemon, a run cut off): use that moved the
	// meter but is not in LCov. UnlinedAgents: their usd per agent.
	UnlinedBookings int64
	UnlinedAgents   map[uuid.UUID]float64
	Reported        bool
	// BeforeSinceOut: the part of LOut whose calls lie before since (the
	// span of the range's first step that precedes it); BeforeSinceCov: the
	// part of LCov of lines that began before since and were not spread
	// (spread ones are in Spread). The floor lists leave both out
	// (floorCovExcluded).
	BeforeSinceOut, BeforeSinceCov float64
	// The same two at the lead price.
	BeforeSinceOutLead, BeforeSinceCovLead float64
	// Usage: read while the seat's basis was usage (0151); its points are
	// the usage charges', not the plan's.
	Usage bool
}

// seatSteps builds a seat's steps from its meter's readings (the source the
// page reads), each with the lists of its span and whether it was reported.
// A meter read only before the window, or never, has none. The steps begin
// at an anchor before since, so that the chunks (F3c) do not depend on where
// the range starts: the last reset before since (the week's first reading)
// among the readings meterQuery returns (from 8 days before since), else the
// first of them. Only the steps at or after since are reported (splitSeat's
// since); those before it shape the chunks and k.
func (s *Store) seatSteps(ctx context.Context, orgID uuid.UUID, since time.Time, seat meterSeat, sources []string,
	readings []meterRow, coverage []MeterCoverage, plans []CredentialPlan, bases []CredentialBasis,
	hosts map[string][]ExternalHost) ([]shareStep, MeterCoverage, error) {
	cov, ok := pickCoverage(coverage, seat.key, seat.slot)
	if !ok {
		return nil, cov, nil
	}
	var rs []meterRow
	for _, r := range readings {
		if r.key == seat.key && r.slot == seat.slot && r.source == cov.Source && r.window == cov.WindowKind {
			rs = append(rs, r)
		}
	}
	if len(rs) < 2 || rs[len(rs)-1].at.Before(since) {
		return nil, cov, nil
	}
	var steps []shareStep
	for k := stepAnchor(rs, since) + 1; k < len(rs); k++ {
		r := rs[k]
		st := shareStep{At: r.at.UTC(), From: rs[k-1].at.UTC(), Points: r.points, Reset: r.reset}
		if p, ok := planAt(plans, r.key, r.slot, r.at); ok {
			rate := p.USDPerPoint
			st.Rate = &rate
		} else if len(bases) > 0 && basisAt(bases, r.key, r.slot, r.at) == BasisUsage {
			// As meterPartition reads it: the plans are cut where usage
			// holds (seatPlans), and these points are the charges'.
			st.Usage = true
		}
		steps = append(steps, st)
	}
	if len(steps) == 0 {
		return nil, cov, nil
	}
	if err := s.stepLists(ctx, orgID, seat, sources, steps, since); err != nil {
		return nil, cov, err
	}
	if len(sources) == 0 {
		// No source is linked to the seat: no outside call can be booked on
		// it and none is waited for. Its steps are reported with L_out = 0,
		// so F3 books R to covey (R_out = 0) — or, when covey has no line in
		// the span either, the step is moved_without_reported_call.
		for i := range steps {
			steps[i].Reported = true
		}
		return steps, cov, nil
	}
	marks, err := s.hostMarks(ctx, orgID, sources, hosts, steps)
	if err != nil {
		return nil, cov, err
	}
	for i := range steps {
		steps[i].Reported = stepReported(steps[i].At, marks(i))
	}
	return steps, cov, nil
}

// stepAnchor is the reading a seat's steps begin after (seatSteps): the
// last reset before since, else the first reading.
func stepAnchor(rs []meterRow, since time.Time) int {
	anchor := 0
	for k := 1; k < len(rs) && rs[k].at.Before(since); k++ {
		if rs[k].reset {
			anchor = k
		}
	}
	return anchor
}

// spanArrays are the steps' spans as two arrays for unnest.
func spanArrays(steps []shareStep) ([]time.Time, []time.Time) {
	from, to := make([]time.Time, len(steps)), make([]time.Time, len(steps))
	for i, st := range steps {
		from[i], to[i] = st.From, st.At
	}
	return from, to
}

// externalStepUSD prices one group of outside calls of one model for L_out:
// F1 where the list knows the model, else the calls' reported_usd (the
// counterpart of the engine's figure in L_cov). ok is false for calls with
// neither: they are counted as unpriced.
func externalStepUSD(model string, t ModelTokens, hasReported bool, reported float64) (float64, bool) {
	if usd := ExternalListUSD(model, t); usd != nil {
		return *usd, true
	}
	if hasReported {
		return reported, true
	}
	return 0, false
}

// stepLists fills each step's L_out (the linked sources' calls with ts in
// its span) and L_cov with the agents' parts (the seat's lines with
// created_at in its span). The spans are disjoint, so a call or line counts
// in one step at most. created_at is a line's result time: a line with a
// started_at in an earlier span (a long turn, started_at = created_at −
// duration_ms) is spread over the spans it ran in, in proportion to its time
// in each (spreadLine), so its list does not land after the meter moved for
// it. An outside call has one time only and stays in its span. Each step
// also keeps the part of its lists that lies (calls) or began (lines not
// spread) before since, which the floor lists leave out.
func (s *Store) stepLists(ctx context.Context, orgID uuid.UUID, seat meterSeat, sources []string, steps []shareStep,
	since time.Time) error {
	from, to := spanArrays(steps)
	if len(sources) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT s.i, e.source, e.model, e.reported_usd IS NOT NULL, e.ts < $5, count(*),
			SUM(e.input), SUM(e.output), SUM(e.cache_read), SUM(e.cache_write_5m), SUM(e.cache_write_1h),
			COALESCE(SUM(e.reported_usd), 0)::float8
			FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS s(f, t, i)
			JOIN external_usage e ON e.org_id = $1 AND e.source = ANY($4) AND e.ts > s.f AND e.ts <= s.t
			GROUP BY 1, 2, 3, 4, 5`, orgID, from, to, sources, since)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				i             int64
				source, model string
				hasReported   bool
				before        bool
				calls         int64
				t             ModelTokens
				reported      float64
			)
			if err := rows.Scan(&i, &source, &model, &hasReported, &before, &calls, &t.Input, &t.Output, &t.CacheRead,
				&t.CacheWrite5m, &t.CacheWrite1h, &reported); err != nil {
				rows.Close()
				return err
			}
			st := &steps[i-1]
			if usd, ok := externalStepUSD(model, t, hasReported, reported); ok {
				// The lead price: reported where the calls carry it, else
				// the list price externalStepUSD gave.
				lead := usd
				if hasReported {
					lead = reported
				}
				if before {
					st.BeforeSinceOut += usd
					st.BeforeSinceOutLead += lead
				}
				st.LOut += usd
				st.LOutLead += lead
				if st.SrcOut == nil {
					st.SrcOut = map[string]float64{}
				}
				st.SrcOut[source] += usd
			} else {
				st.UnpricedCalls += calls
				st.UnpricedTokens += t.Input + t.Output + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	// The lines that ran wholly in their step's span are summed per agent and
	// model; one that began before it (started_at < the span's start) is its
	// own group, spread over the spans it ran in.
	rows, err := s.pool.Query(ctx, `SELECT s.i, m.agent_id, m.model,
		CASE WHEN m.started_at < s.f THEN m.id END, COALESCE(m.started_at, m.created_at) < $6,
		MIN(m.started_at), MAX(m.created_at),
		SUM(m.input_tokens), SUM(m.output_tokens), SUM(m.cache_read_tokens), SUM(m.cache_write_5m_tokens),
		SUM(m.cache_write_1h_tokens), SUM(m.web_search_requests), SUM(m.engine_usd)::float8
		FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS s(f, t, i)
		JOIN model_usage m ON m.org_id = $1 AND m.secret_key = $4 AND m.secret_slot = $5
			AND m.created_at > s.f AND m.created_at <= s.t
		GROUP BY 1, 2, 3, 4, 5`, orgID, from, to, seat.key, seat.slot, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			i         int64
			agent     uuid.UUID
			model     string
			spread    *int64
			before    bool
			started   *time.Time
			createdAt time.Time
			t         ModelTokens
			engine    float64
		)
		if err := rows.Scan(&i, &agent, &model, &spread, &before, &started, &createdAt, &t.Input, &t.Output, &t.CacheRead,
			&t.CacheWrite5m, &t.CacheWrite1h, &t.WebSearches, &engine); err != nil {
			return err
		}
		usd := engine
		if pr, ok := ListPrice(model); ok {
			usd = pr.Cost(t)
		}
		if spread != nil && started != nil {
			// Its part before since is left out of the floor lists by
			// floorCovExcluded, from its Started.
			spreadLine(steps, int(i-1), *started, createdAt, agent, usd, engine)
			continue
		}
		if before {
			steps[i-1].BeforeSinceCov += usd
			steps[i-1].BeforeSinceCovLead += engine
		}
		addCovList(&steps[i-1], agent, usd)
		steps[i-1].LCovLead += engine
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Bookings on the seat no line was linked to (as the cost cells'
	// measured "unattributed" type reads them): their use is not in L_cov,
	// so the step's chunk sets no rate and has no unattributed part (F3c).
	// The whole stretch bounds created_at, so the scan stays on
	// idx_cost_secret_slot and the anti join on idx_model_usage_cost_entry.
	rows, err = s.pool.Query(ctx, `SELECT s.i, ce.agent_id, count(*), SUM(ce.usd)::float8
		FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS s(f, t, i)
		JOIN cost_entries ce ON ce.secret_key = $4 AND ce.secret_slot = $5
			AND ce.created_at > $6 AND ce.created_at <= $7
			AND ce.created_at > s.f AND ce.created_at <= s.t AND ce.usd > 0.0005
		JOIN agents a ON a.id = ce.agent_id AND a.org_id = $1
		WHERE NOT EXISTS (SELECT 1 FROM model_usage m WHERE m.cost_entry_id = ce.id)
		GROUP BY 1, 2`, orgID, from, to, seat.key, seat.slot, from[0], to[len(to)-1])
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			i, n  int64
			agent uuid.UUID
			usd   float64
		)
		if err := rows.Scan(&i, &agent, &n, &usd); err != nil {
			return err
		}
		st := &steps[i-1]
		st.UnlinedBookings += n
		if st.UnlinedAgents == nil {
			st.UnlinedAgents = map[uuid.UUID]float64{}
		}
		st.UnlinedAgents[agent] += usd
	}
	return rows.Err()
}

// addCovList books usd of an agent's lines to a step's L_cov.
func addCovList(st *shareStep, agent uuid.UUID, usd float64) {
	st.LCov += usd
	if st.Agents == nil {
		st.Agents = map[uuid.UUID]float64{}
	}
	st.Agents[agent] += usd
}

// spreadLine books a line that ran from started to ended (its created_at, in
// step i's span) over the spans it ran in, in proportion to its time in each
// (F3c): the meter moves while a turn runs, and its result time lies after
// that. The part before the first step's span is in none.
// The steps' spans are contiguous, each starting at the one before's end.
//
// The proportional parts are an estimate of when the line's use moved the
// meter: a turn whose context grows uses more of its list late, so an even
// spread puts too much of it early (#126 review of d3498bee: 24 readings of
// one back-loaded run gave k 0.060 for a true 0.1). They serve the shares
// only. The line is also recorded whole in step i's Spread, with the first
// step it ran in: k counts it only in a stretch that holds all of it (a
// lower bound on the stretch's list), U_c at its whole list in every chunk
// it ran in (an upper bound on the chunk's), so k ≥ k* and U_c ≤ U*_c hold
// however its use was spread in time.
func spreadLine(steps []shareStep, i int, started, ended time.Time, agent uuid.UUID, usd, lead float64) {
	dur := ended.Sub(started)
	if dur <= 0 || !started.Before(steps[i].From) {
		addCovList(&steps[i], agent, usd)
		steps[i].LCovLead += lead
		return
	}
	first := -1
	for j := i; j >= 0 && steps[j].At.After(started); j-- {
		lo, hi := steps[j].From, steps[j].At
		if started.After(lo) {
			lo = started
		}
		if ended.Before(hi) {
			hi = ended
		}
		if hi.After(lo) {
			part := usd * float64(hi.Sub(lo)) / float64(dur)
			addCovList(&steps[j], agent, part)
			steps[j].LCovSpread += part
			steps[j].LCovLead += lead * float64(hi.Sub(lo)) / float64(dur)
			first = j
		}
	}
	if started.Before(steps[0].From) {
		// Its part before the first span is in no step: no stretch holds it.
		first = -1
	}
	steps[i].Spread = append(steps[i].Spread, spreadSpan{First: first, USD: usd, Lead: lead, Started: started, Ended: ended})
}

// spreadSpan is one line spread by spreadLine: First, the first step it ran
// in (−1 when it began before the first step's span); USD, its whole list;
// Started and Ended, the time it ran.
type spreadSpan struct {
	First int
	USD   float64
	// Lead: its whole lead price (Claude Code's figure), spread alike.
	Lead           float64
	Started, Ended time.Time
}

// floorCovExcluded is, per step, the covey list the floor lists leave out
// (review of e82d640f, finding 3): the parts of the spread lines that began
// before since, and the step's lines that were not spread but began before
// since (BeforeSinceCov). Such a line's use may have moved the meter before
// the range's first reading, so its proportional part in the range is no
// list the range's real cost is known to have paid for: a front-loaded turn
// read 17.86× for a true 10×. Zero since leaves nothing out.
func floorCovExcluded(steps []shareStep, since time.Time) []float64 {
	return floorCovExcludedBy(steps, since, false)
}

// floorCovExcludedBy is floorCovExcluded at the list price, or with lead at
// the lead price (FloorLeadUSD).
func floorCovExcludedBy(steps []shareStep, since time.Time, lead bool) []float64 {
	out := make([]float64, len(steps))
	for e := range steps {
		if lead {
			out[e] += steps[e].BeforeSinceCovLead
		} else {
			out[e] += steps[e].BeforeSinceCov
		}
		for _, ln := range steps[e].Spread {
			usd := ln.USD
			if lead {
				usd = ln.Lead
			}
			dur := ln.Ended.Sub(ln.Started)
			if !ln.Started.Before(since) || dur <= 0 {
				continue
			}
			for j := e; j >= 0 && steps[j].At.After(ln.Started); j-- {
				lo, hi := steps[j].From, steps[j].At
				if ln.Started.After(lo) {
					lo = ln.Started
				}
				if ln.Ended.Before(hi) {
					hi = ln.Ended
				}
				if hi.After(lo) {
					out[j] += usd * float64(hi.Sub(lo)) / float64(dur)
				}
			}
		}
	}
	return out
}

// unmetered is a seat's list in the window that lies in no step's span,
// at the list and at the lead price, and of each the part before the first
// step's previous reading (no meter data for that time; the rest lies after
// the last reading, its real share pending).
type unmetered struct{ list, beforeList, lead, beforeLead float64 }

func (u *unmetered) add(list, lead float64, before bool) {
	u.list += list
	u.lead += lead
	if before {
		u.beforeList += list
		u.beforeLead += lead
	}
}

// unmeteredSpan is the stretch the steps cover, (from, to], and whether
// there is one: with no step, (since, since] and nothing lies before it.
func unmeteredSpan(since time.Time, steps []shareStep) (from, to time.Time, has bool) {
	if len(steps) == 0 {
		return since, since, false
	}
	return steps[0].From, steps[len(steps)-1].At, true
}

// unmeteredList is L_out of the linked calls in the window that lie in no
// step's span: the steps' spans are contiguous, from the first step's
// previous reading through the last reading, so it is every call outside
// that stretch — before it or after it.
func (s *Store) unmeteredList(ctx context.Context, orgID uuid.UUID, since time.Time, sources []string, steps []shareStep) (unmetered, error) {
	var u unmetered
	if len(sources) == 0 {
		return u, nil
	}
	from, to, has := unmeteredSpan(since, steps)
	rows, err := s.pool.Query(ctx, `SELECT model, reported_usd IS NOT NULL, $6 AND ts <= $4,
		SUM(input), SUM(output), SUM(cache_read), SUM(cache_write_5m), SUM(cache_write_1h),
		COALESCE(SUM(reported_usd), 0)::float8
		FROM external_usage
		WHERE org_id = $1 AND source = ANY($2) AND ts >= $3 AND NOT (ts > $4 AND ts <= $5)
		GROUP BY 1, 2, 3`, orgID, sources, since, from, to, has)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			model       string
			hasReported bool
			before      bool
			t           ModelTokens
			reported    float64
		)
		if err := rows.Scan(&model, &hasReported, &before, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m,
			&t.CacheWrite1h, &reported); err != nil {
			return u, err
		}
		if v, ok := externalStepUSD(model, t, hasReported, reported); ok {
			lead := v
			if hasReported {
				lead = reported
			}
			u.add(v, lead, before)
		}
	}
	return u, rows.Err()
}

// unmeteredCoveyList is L_cov of the seat's lines in the window that lie in
// no step's span, priced as stepLists prices them: covey's list price before
// the seat's first reading in the range or after its last one (or with no
// reading at all), with no real share. Its lead price is the engine's
// figure.
func (s *Store) unmeteredCoveyList(ctx context.Context, orgID uuid.UUID, since time.Time, seat meterSeat, steps []shareStep) (unmetered, error) {
	var u unmetered
	from, to, has := unmeteredSpan(since, steps)
	rows, err := s.pool.Query(ctx, `SELECT model, $7 AND created_at <= $5,
		SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_5m_tokens),
		SUM(cache_write_1h_tokens), SUM(web_search_requests), SUM(engine_usd)::float8
		FROM model_usage
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND created_at >= $4
			AND NOT (created_at > $5 AND created_at <= $6)
		GROUP BY 1, 2`, orgID, seat.key, seat.slot, since, from, to, has)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			model  string
			before bool
			t      ModelTokens
			engine float64
		)
		if err := rows.Scan(&model, &before, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h,
			&t.WebSearches, &engine); err != nil {
			return u, err
		}
		usd := engine
		if pr, ok := ListPrice(model); ok {
			usd = pr.Cost(t)
		}
		u.add(usd, engine, before)
	}
	return u, rows.Err()
}

// externalActiveFor is how long a machine counts as active after a push or
// a call: one that has done neither in the week before a reading is not
// waited for (an old laptop must not hold every later reading unsplit).
const externalActiveFor = 7 * 24 * time.Hour

// hostMark is what stepReported knows of one machine at one reading.
type hostMark struct {
	// ReportedThrough: the machine's watermark; nil when it never sent one.
	ReportedThrough *time.Time
	// PushedAt: its latest push. The table keeps only the latest, so a
	// machine that pushed after the reading without reaching it counts as
	// active: it is alive and has not reported the reading.
	PushedAt time.Time
	// LastCall: its latest call at or before the reading; nil without one.
	LastCall *time.Time
}

// stepReported says whether the outside calls of a step's span were all
// sent: every active machine of the linked sources has reported through the
// reading. Active: it pushed, or had a call, in the week before the reading
// (externalActiveFor). Without an active machine, or without watermarks,
// nothing is reported: a step is split only on what the uploader says it
// has sent, never on the absence of calls.
func stepReported(at time.Time, hosts []hostMark) bool {
	since := at.Add(-externalActiveFor)
	active := false
	for _, h := range hosts {
		if !h.PushedAt.After(since) && (h.LastCall == nil || !h.LastCall.After(since)) {
			continue
		}
		active = true
		if h.ReportedThrough == nil || h.ReportedThrough.Before(at) {
			return false
		}
	}
	return active
}

// hostMarks returns, per step, the marks of the linked sources' machines. A
// machine's calls are read only from a week before the first step on, where
// they can make it active; its latest one before a reading is found by
// binary search. A machine that never sent a watermark is left out when
// another machine of its source has sent one and it has neither pushed nor
// had a call since the first of them arrived (watermarksFrom,
// unmarkedBefore): the source's
// uploader claims watermarks, so such a host is a name from before it did
// (rows uploaded before the hosts table, an older uploader), not a machine
// that will ever report — counted, it would hold every reading of the seat
// unsplit for a week after its last call. One that pushed or had a call
// after it is a machine still sending without watermarks (an older uploader
// beside a newer one): it is counted, and holds the readings it may not
// have sent (review of d3498bee, F5). A source none of whose machines ever
// sent one still holds its readings: nothing says its calls were sent.
func (s *Store) hostMarks(ctx context.Context, orgID uuid.UUID, sources []string, hosts map[string][]ExternalHost,
	steps []shareStep) (func(i int) []hostMark, error) {
	type hostKey struct{ source, host string }
	var keys []hostKey
	known := map[hostKey]ExternalHost{}
	for _, src := range sources {
		for _, h := range hosts[src] {
			k := hostKey{src, h.Host}
			keys = append(keys, k)
			known[k] = h
		}
	}
	// earliest: per source with a machine that sent a watermark, when the
	// first of them arrived (watermarksFrom).
	earliest := map[string]time.Time{}
	for _, src := range sources {
		if e, ok := watermarksFrom(hosts[src]); ok {
			earliest[src] = e
		}
	}
	// The latest call of each machine without a watermark in such a source.
	var qSrc, qHost []string
	for _, k := range keys {
		if _, ok := earliest[k.source]; ok && known[k].ReportedThrough == nil {
			qSrc, qHost = append(qSrc, k.source), append(qHost, k.host)
		}
	}
	lastCall := map[hostKey]time.Time{}
	if len(qSrc) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT h.source, h.host, l.ts
			FROM unnest($2::text[], $3::text[]) AS h(source, host)
			CROSS JOIN LATERAL (SELECT max(e.ts) AS ts FROM external_usage e
				WHERE e.org_id = $1 AND e.source = h.source AND e.host = h.host) l
			WHERE l.ts IS NOT NULL`, orgID, qSrc, qHost)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				k  hostKey
				ts time.Time
			)
			if err := rows.Scan(&k.source, &k.host, &ts); err != nil {
				rows.Close()
				return nil, err
			}
			lastCall[k] = ts.UTC()
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	dropped := map[hostKey]bool{}
	kept := keys[:0]
	for _, k := range keys {
		h := known[k]
		e, claims := earliest[k.source]
		if h.ReportedThrough == nil && claims {
			var lc *time.Time
			if t, ok := lastCall[k]; ok {
				lc = &t
			}
			if unmarkedBefore(h.PushedAt, lc, e) {
				dropped[k] = true
				delete(known, k)
				continue
			}
		}
		kept = append(kept, k)
	}
	keys = kept
	calls := map[hostKey][]time.Time{}
	// unlisted: hosts with rows the hosts table does not know, in a source
	// with a watermark; decided on their calls once read.
	unlisted := map[hostKey]bool{}
	rows, err := s.pool.Query(ctx, `SELECT source, host, ts FROM external_usage
		WHERE org_id = $1 AND source = ANY($2) AND ts > $3 AND ts <= $4
		ORDER BY source, host, ts`, orgID, sources, steps[0].At.Add(-externalActiveFor), steps[len(steps)-1].At)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k  hostKey
			ts time.Time
		)
		if err := rows.Scan(&k.source, &k.host, &ts); err != nil {
			return nil, err
		}
		if dropped[k] {
			continue
		}
		if _, ok := known[k]; !ok {
			// Rows of a host the hosts table does not know (uploaded before
			// it existed): active by its calls, and with no watermark.
			known[k] = ExternalHost{Host: k.host}
			keys = append(keys, k)
			if _, ok := earliest[k.source]; ok {
				unlisted[k] = true
			}
		}
		calls[k] = append(calls[k], ts.UTC())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(unlisted) > 0 {
		// Such a host never pushed since the table exists: left out unless
		// a call of it (read up to the last reading) is later than its
		// source's earliest watermark.
		kept := keys[:0]
		for _, k := range keys {
			if cs := calls[k]; unlisted[k] && unmarkedBefore(time.Time{}, &cs[len(cs)-1], earliest[k.source]) {
				delete(known, k)
				continue
			}
			kept = append(kept, k)
		}
		keys = kept
	}
	return func(i int) []hostMark {
		at := steps[i].At
		marks := make([]hostMark, 0, len(keys))
		for _, k := range keys {
			h := known[k]
			m := hostMark{ReportedThrough: h.ReportedThrough, PushedAt: h.PushedAt}
			cs := calls[k]
			if n := sort.Search(len(cs), func(j int) bool { return cs[j].After(at) }); n > 0 {
				last := cs[n-1]
				m.LastCall = &last
			}
			marks = append(marks, m)
		}
		return marks
	}, nil
}

// watermarksFrom is when a source began to send watermarks: the earliest
// first_reported_at of its machines (a machine's watermark itself where the
// table has none). It stays put once set, where the earliest watermark moved
// on with every push: an older uploader pushing hourly beside a newer one
// pushing every few minutes was left out whenever the newer one's latest
// watermark had passed its latest push — three quarters of each hour — and
// the page flipped between split and unsplit (review of e82d640f, F5).
func watermarksFrom(hosts []ExternalHost) (time.Time, bool) {
	var e time.Time
	ok := false
	for _, h := range hosts {
		at := h.FirstReportedAt
		if at == nil {
			at = h.ReportedThrough
		}
		if at != nil && (!ok || at.Before(e)) {
			e, ok = *at, true
		}
	}
	return e, ok
}

// unmarkedBefore: a machine without a watermark, in a source that began to
// send watermarks at earliest (watermarksFrom), last pushed at pushedAt
// (zero when the hosts table does not know it) and last called at lastCall
// (nil without a call), dates from before its source claimed watermarks:
// neither is later than earliest (hostMarks leaves it out).
func unmarkedBefore(pushedAt time.Time, lastCall *time.Time, earliest time.Time) bool {
	return !pushedAt.After(earliest) && (lastCall == nil || !lastCall.After(earliest))
}

// stepReason is why a step is not split: "" for a split step, SplitIdle for
// one with nothing to split, SplitNoReportedCall for one that moved with no
// call (split by its chunk's share all the same when the chunk has list
// price, F3c), else the reason it is unsplit.
//
// A step read under the usage basis (0151) is out of the dent and of every
// chunk whole, moved or not (usage): its money is the usage charges. A step
// that moved at no plan price is out of the dent whole (unpriced). A
// priced step is split when it was reported and L_out + L_cov > 0. A
// reported step with no list on either side that moved the meter moved
// "without a reported call": mostly the meter's lag after use, so it joins
// its chunk. A reported step whose outside calls all lack a price (L_out = 0
// with unpriced calls) is not split (outside_unpriced): the outside share
// is unknown, and booking R to covey would bill it the outside sessions'
// work. Outside calls some of which are priced stay split on the priced
// ones, the rest counted as unpriced (and the chunk is incomplete). One that
// did not move either (R = 0, both lists 0, no priced outside call: an idle
// reading) has nothing to split and is no split step (SplitIdle, #126
// finding 25), even with outside calls of no price.
func stepReason(in shareStep) string {
	lists := in.LOut + in.LCov
	switch {
	case in.Usage:
		return SplitUsage
	case in.Points != 0 && in.Rate == nil:
		return SplitUnpriced
	case !in.Reported:
		return SplitNotReported
	// A step that did not move and has no list is idle even with outside
	// calls of no price: there is nothing to split, and it must not close
	// the chunk.
	case in.LOut == 0 && in.UnpricedCalls > 0 && (in.Points != 0 || lists > 0):
		return SplitOutsideUnpriced
	case lists == 0 && in.Points != 0:
		return SplitNoReportedCall
	case lists == 0:
		return SplitIdle
	}
	return ""
}

const (
	// chunkMinPoints: a chunk closes at the reading its points reach this
	// (F3c): enough movement that the whole percents at its two ends are a
	// small part of it. The last chunk, and one closed early (by a step that
	// is not split, a reset or a change of plan price), may hold fewer.
	chunkMinPoints = 5
	// rateMinPoints: a stretch of chunks sets the rate k only from this much
	// movement on (its points without a reset step's): below it the slack
	// S_c is too large a part of it.
	rateMinPoints = 10
	// chunkSlackPoints is S_c: one point for the whole percents at the
	// chunk's two ends and one of lag at its end (a reading may trail the
	// use before it by a point, as meterQuery reads a drop of one). The same
	// for a stretch of adjacent chunks, for k, and for the parts' ranges.
	chunkSlackPoints = 2.0
	// lagFoldPoints: at most this much movement with no call right after a
	// chunk closed at its size is that chunk's lag and joins it (foldLag);
	// the meter trails use by a point at most.
	lagFoldPoints = 1.0
)

// meterChunk is one chunk of a seat's steps (F3c).
type meterChunk struct {
	// steps: the indexes of its steps, in order.
	steps []int
	// points: P_c; lOut, lCov: Lo_c and Lc_c; agents: each agent's part
	// of Lc_c.
	points, lOut, lCov float64
	agents             map[uuid.UUID]float64
	// srcOut: each source's part of Lo_c (Lo_s,c).
	srcOut map[string]float64
	// kPoints, kList: its points and list without a reset step's, for k;
	// kList without the spread lines' parts (chunkRates adds the lines a
	// stretch holds whole).
	kPoints, kList float64
	// lHigh: L_c for U_c, an upper bound on the list of its use: its list
	// with every spread line that ran in it at its whole list
	// (chunkListHigh).
	lHigh float64
	// rate: the plan price per point of its steps that moved or have list
	// (one per chunk: a change of price closes it); nil when none has one.
	rate *float64
	// unpriced: a step of it had outside calls without a price. unlined:
	// covey bookings without a line in it, unlinedUSD their usd, by agent
	// in unlinedAgents. Either is use that is not in its list (incomplete).
	unpriced      bool
	unlined       int64
	unlinedUSD    float64
	unlinedAgents map[uuid.UUID]float64
	// unattributed: U_c; frac: U_c / P_c, the fraction of each step's
	// movement that is unattributed (1 for a chunk with no call).
	unattributed, frac float64
	// follows: the chunk this one was opened right after, when that one
	// closed at its size at the step before (nothing closed it before this
	// step); −1 otherwise. Chunks linked so are adjacent: a stretch of them
	// sets k (chunkRates), and a chunk with no list that follows one is the
	// meter's lag after it, up to lagFoldPoints (foldLag).
	follows int
	// folded: the points of lag folded into it.
	folded float64
}

func (c *meterChunk) list() float64 { return c.lOut + c.lCov }

// incomplete: some use of the chunk is not in its list.
func (c *meterChunk) incomplete() bool { return c.unpriced || c.unlined > 0 }

// split: its steps are split by its share — it has list, or none but covey
// bookings without a line and no outside call without a price: covey's
// (share 0), an incomplete chunk with no unattributed part.
func (c *meterChunk) split() bool { return c.list() > 0 || (c.unlinedUSD > 0 && !c.unpriced) }

// unknown: no list, covey bookings without a line and outside calls without
// a price — whose use moved it is unknown: not split.
func (c *meterChunk) unknown() bool { return c.list() <= 0 && c.unlined > 0 && c.unpriced }

// noCall: the meter moved and nobody reported a call in the whole chunk —
// unattributed whole.
func (c *meterChunk) noCall() bool { return !c.split() && !c.unknown() }

// share is the chunk's outside share Lo_c / L_c; 0 without list.
func (c *meterChunk) share() float64 {
	if l := c.list(); l > 0 {
		return c.lOut / l
	}
	return 0
}

// What a chunk or step with no list holds (seatChunks): covey bookings
// without a line, movement with no call, or neither yet.
const (
	noListNone = iota
	noListUnlined
	noListNoCall
)

func (c *meterChunk) noListKind() int {
	switch {
	case c.unlined > 0:
		return noListUnlined
	case c.points != 0:
		return noListNoCall
	}
	return noListNone
}

func stepNoListKind(in shareStep) int {
	switch {
	case in.UnlinedBookings > 0:
		return noListUnlined
	case in.Points != 0:
		return noListNoCall
	}
	return noListNone
}

// add puts step i in the chunk; counts: it moved or has list (not idle), so
// its plan price is the chunk's.
func (c *meterChunk) add(i int, in shareStep, counts bool) {
	c.steps = append(c.steps, i)
	c.points += in.Points
	c.lOut += in.LOut
	c.lCov += in.LCov
	if !in.Reset {
		c.kPoints += in.Points
		c.kList += in.LOut + in.LCov - in.LCovSpread
	}
	if counts && in.Rate != nil && c.rate == nil {
		c.rate = in.Rate
	}
	c.unpriced = c.unpriced || in.UnpricedCalls > 0
	c.unlined += in.UnlinedBookings
	for id, l := range in.Agents {
		if c.agents == nil {
			c.agents = map[uuid.UUID]float64{}
		}
		c.agents[id] += l
	}
	for id, u := range in.UnlinedAgents {
		if c.unlinedAgents == nil {
			c.unlinedAgents = map[uuid.UUID]float64{}
		}
		c.unlinedAgents[id] += u
		c.unlinedUSD += u
	}
	for src, l := range in.SrcOut {
		if c.srcOut == nil {
			c.srcOut = map[string]float64{}
		}
		c.srcOut[src] += l
	}
}

// seatChunks walks a seat's steps in time order into chunks (F3c): a split,
// idle or moved_without_reported_call step joins the current chunk, opening
// one when none is open, and the chunk closes at the reading its points
// reach chunkMinPoints. A not_reported, outside_unpriced, unpriced or usage step
// closes it before itself and stays in none; a reset closes it before
// itself, and so does a step that moved or has list at another plan price
// than the chunk's — or, while the chunk has none yet (idle steps only), the
// one it follows (an idle step, with neither, rides along at its own price).
// While the open chunk has no list, a step with covey bookings without a
// line closes it before itself when it holds movement with no call, and the
// other way round (each opens a chunk of its own, following none). Likewise
// a step with such bookings and no list never joins a chunk with list, and
// a step with list never joins a chunk of such bookings only.
// chunkOf[i] is step i's chunk, −1 for none. A chunk opened right after one
// that closed at its size, with nothing closing it in between (no unsplit
// or unpriced step, no reset, no change of plan price), records it in
// follows.
func seatChunks(steps []shareStep, reasons []string) ([]int, []meterChunk) {
	chunkOf := make([]int, len(steps))
	var chunks []meterChunk
	cur, sized := -1, -1
	for i, in := range steps {
		chunkOf[i] = -1
		if in.Reset {
			cur, sized = -1, -1
		}
		switch reasons[i] {
		case "", SplitIdle, SplitNoReportedCall:
		default:
			cur, sized = -1, -1
			continue
		}
		counts := reasons[i] != SplitIdle
		if counts && in.Rate != nil {
			if cur >= 0 {
				r := chunks[cur].rate
				if f := chunks[cur].follows; r == nil && f >= 0 {
					r = chunks[f].rate
				}
				if r != nil && *r != *in.Rate {
					cur = -1
				}
			} else if sized >= 0 {
				if r := chunks[sized].rate; r != nil && *r != *in.Rate {
					sized = -1
				}
			}
		}
		// While the open chunk has no list, covey's bookings without a line
		// and movement with no call do not share it: a chunk of the first is
		// covey's whole, one of the second unattributed whole, and either
		// joined to the other would book the other's movement to it.
		if cur >= 0 && chunks[cur].list() <= 0 && in.LOut+in.LCov <= 0 {
			if a, b := chunks[cur].noListKind(), stepNoListKind(in); a != noListNone && b != noListNone && a != b {
				cur = -1
			}
		}
		// Nor do they share one with list (review of e82d640f, finding 4): a
		// chunk with list and a booking without a line is incomplete, with
		// no unattributed part, so one lineless +1 in it would hide what its
		// list measures beyond the rate (another machine's points went to the
		// parts). A step with such bookings and no list closes a chunk with
		// list before itself, and a step with list closes a chunk of such
		// bookings only.
		if cur >= 0 {
			c, lists := &chunks[cur], in.LOut+in.LCov > 0
			if (!lists && in.UnlinedBookings > 0 && c.list() > 0) || (lists && c.list() <= 0 && c.unlined > 0) {
				cur = -1
			}
		}
		if cur < 0 {
			chunks = append(chunks, meterChunk{follows: sized})
			cur = len(chunks) - 1
		}
		sized = -1
		c := &chunks[cur]
		chunkOf[i] = cur
		c.add(i, in, counts)
		if c.points >= chunkMinPoints {
			cur, sized = -1, cur
		}
	}
	return chunkOf, chunks
}

// foldLag folds the meter's lag after a chunk into it (F3c): a chunk with no
// list that follows a chunk with list closed at its size is a run of
// readings that moved with no call, or did not move, right after use. Its
// steps join that chunk, in order, while the points they add stay within
// lagFoldPoints — the meter trails use by a point at most; from the first
// step that would pass it on, the run stays a chunk with no list of its own
// (unattributed whole: another machine, an estimate). Movement with no call
// and no adjacent chunk — at the start of the range, or after an unsplit or
// unpriced step, a reset or a change of plan price — stays a chunk of its
// own, and a chunk with no call that follows one with no call joins it
// whole. A chunk with covey bookings without a line is never folded and
// never folded into (review of d3498bee, F1): it is covey's whole (share 0)
// with no unattributed part, so movement with no call folded into it would
// go to covey — another machine's 20 points after 5 of covey's booked
// without a line read as covey's 25. It returns the chunks left and the
// steps' new chunk indexes; k is taken before it (chunkRates), on the
// chunks as they closed.
func foldLag(chunks []meterChunk, steps []shareStep, reasons []string) ([]meterChunk, []int) {
	root := make([]int, len(chunks))
	var out []meterChunk
	for i, c := range chunks {
		if c.list() > 0 || c.unlined > 0 || c.follows < 0 {
			root[i] = len(out)
			out = append(out, c)
			continue
		}
		r := root[c.follows]
		if out[r].list() <= 0 {
			if out[r].noCall() && c.noCall() {
				for _, k := range c.steps {
					out[r].add(k, steps[k], reasons[k] != SplitIdle)
				}
				root[i] = r
			} else {
				root[i] = len(out)
				c.follows = -1
				out = append(out, c)
			}
			continue
		}
		n := 0
		for ; n < len(c.steps); n++ {
			k := c.steps[n]
			if out[r].folded+steps[k].Points > lagFoldPoints {
				break
			}
			out[r].add(k, steps[k], reasons[k] != SplitIdle)
			out[r].folded += steps[k].Points
		}
		root[i] = r
		if n < len(c.steps) {
			rest := meterChunk{follows: -1}
			for _, k := range c.steps[n:] {
				rest.add(k, steps[k], reasons[k] != SplitIdle)
			}
			root[i] = len(out)
			out = append(out, rest)
		}
	}
	chunkOf := make([]int, len(steps))
	for i := range chunkOf {
		chunkOf[i] = -1
	}
	for j, c := range out {
		for _, k := range c.steps {
			chunkOf[k] = j
		}
	}
	return out, chunkOf
}

// chunkRates is F3c's k per plan price: the min over every stretch of
// adjacent chunks (each opened right after the one before it closed at its
// size, so at one plan price) with no incomplete chunk, kPoints ≥
// rateMinPoints and kList > 0 of (kPoints + S_c) / kList. Any such stretch's
// true points lie within its read points ± S_c as one chunk's do, and the
// min over every stretch, not only the longest, finds a clean one: readings
// of a point or less make chunks of 5 that alone never reach 10. A chunk
// with no call in a stretch only adds points. A line spread over several
// spans (spreadLine) counts in a stretch's list only when the stretch holds
// all of it, outside a reset step: kList is then at most the list of the
// stretch's use however the line's use fell in time (review of d3498bee,
// F2). It returns the prices in the order they first appear, with k (nil
// when none).
//
// O(n²) in the chunks of a run of adjacent ones (a reset, an unsplit step
// or an incomplete chunk ends it): a week of readings every few minutes is
// a few hundred chunks (well under a millisecond); the review measured 0.9 s
// at 11,000 adjacent 5-point chunks and 13 s at 40,000, which a seat's
// meter (reset weekly, 5 points a chunk, 100 points a week) cannot reach.
func chunkRates(chunks []meterChunk, steps []shareStep) []ExternalMeterPlanRate {
	var rates []ExternalMeterPlanRate
	at := map[float64]int{}
	for _, c := range chunks {
		if c.rate == nil {
			continue
		}
		if _, ok := at[*c.rate]; !ok {
			at[*c.rate] = len(rates)
			rates = append(rates, ExternalMeterPlanRate{USDPerPoint: *c.rate})
		}
	}
	// ends[b]: the spread lines whose result falls in chunk b.
	ends := make([][]spreadSpan, len(chunks))
	for b, c := range chunks {
		for _, k := range c.steps {
			ends[b] = append(ends[b], steps[k].Spread...)
		}
	}
	for a := range chunks {
		var points, list float64
		var price *float64
		lo := chunks[a].steps[0]
		for b := a; b < len(chunks); b++ {
			c := &chunks[b]
			if (b > a && c.follows != b-1) || c.incomplete() {
				break
			}
			points += c.kPoints
			list += c.kList
			// The stretch's steps are contiguous from lo: a line ending in
			// chunk b that began at or after lo lies in it whole.
			for _, ln := range ends[b] {
				if ln.First >= lo && !steps[ln.First].Reset {
					list += ln.USD
				}
			}
			if price == nil {
				price = c.rate
			}
			if price == nil || points < rateMinPoints || list <= 0 {
				continue
			}
			r := &rates[at[*price]]
			if v := (points + chunkSlackPoints) / list; r.PointsPerListUSD == nil || v < *r.PointsPerListUSD {
				r.PointsPerListUSD = &v
			}
		}
	}
	return rates
}

// chunkUnattributed sets each chunk's U_c: max(0, P_c − S_c − k·L_c) at its
// plan price's k, P_c whole for a chunk with no call, 0 without k, for an
// incomplete chunk (one with no list but covey bookings without a line
// among them) and for one whose use is unknown. L_c is lHigh
// (chunkListHigh), so U_c stays at most the true unattributed points.
func chunkUnattributed(chunks []meterChunk, rates []ExternalMeterPlanRate) {
	at := map[float64]int{}
	for i, r := range rates {
		at[r.USDPerPoint] = i
	}
	for i := range chunks {
		c := &chunks[i]
		switch {
		case c.noCall():
			// The meter moved and nobody reported a call in the whole chunk.
			c.unattributed, c.frac = c.points, 1
		case c.list() <= 0 || c.incomplete() || c.rate == nil:
		default:
			k := rates[at[*c.rate]].PointsPerListUSD
			if k == nil {
				continue
			}
			if u := c.points - chunkSlackPoints - *k*c.lHigh; u > 0 && c.points > 0 {
				c.unattributed, c.frac = u, u/c.points
			}
		}
	}
}

// chunkListHigh sets each chunk's lHigh: its list with each spread line
// (spreadLine) that ran in one of its steps counted at its whole list, not
// its proportional part — an upper bound on the list of the chunk's use
// however the line's use fell in time (review of d3498bee, F2). chunkOf is
// each step's chunk after foldLag.
func chunkListHigh(chunks []meterChunk, chunkOf []int, steps []shareStep) {
	for i := range chunks {
		c := &chunks[i]
		c.lHigh = c.list()
		for _, k := range c.steps {
			c.lHigh -= steps[k].LCovSpread
		}
	}
	var seen []int
	for e := range steps {
		for _, ln := range steps[e].Spread {
			seen = seen[:0]
			for k := max(ln.First, 0); k <= e; k++ {
				if c := chunkOf[k]; c >= 0 && !slices.Contains(seen, c) {
					seen = append(seen, c)
					chunks[c].lHigh += ln.USD
				}
			}
		}
	}
}

// chunkShareRange is the range of a chunk's true outside share however its
// spread lines' use fell in time (review of e82d640f, finding 1): lo = Lo_c /
// lHigh, every spread line that ran in it at its whole list; hi = Lo_c / (L_c
// − its spread parts), none of them (0 without outside list). rate is the
// chunk's plan price, 0 without one. A chunk with no list has lo = hi = 0.
func chunkShareRange(c meterChunk, steps []shareStep) (lo, hi, rate float64) {
	if c.rate != nil {
		rate = *c.rate
	}
	if c.list() <= 0 || c.lOut <= 0 {
		return 0, 0, rate
	}
	spread, lines := 0.0, false
	for _, k := range c.steps {
		spread += steps[k].LCovSpread
		lines = lines || steps[k].LCovSpread > 0
	}
	if !lines && c.lHigh <= c.list() {
		return c.share(), c.share(), rate
	}
	hi = 1
	if d := c.list() - spread; d > 0 {
		hi = math.Min(1, c.lOut/d)
	}
	lo = c.share()
	if c.lHigh > 0 {
		lo = math.Min(lo, c.lOut/c.lHigh)
	}
	return lo, math.Max(hi, c.share()), rate
}

// rateTV is ½·TV of the steps' plan prices over a chunk, ½(r_1 +
// Σ|r_{k+1} − r_k| + r_n), each step at its own price and 0 without a plan
// (#126 findings 24 and 26): the $ range of a part whose share is 1 over
// the chunk (#126 finding 19). A part's is its share times this.
func rateTV(steps []shareStep, idx []int) float64 {
	r := func(k int) float64 {
		if p := steps[idx[k]].Rate; p != nil {
			return *p
		}
		return 0
	}
	n := len(idx)
	if n == 0 {
		return 0
	}
	sum := r(0) + r(n-1)
	for k := 0; k+1 < n; k++ {
		sum += math.Abs(r(k+1) - r(k))
	}
	return sum / 2
}

// sinceCut is how a split chunk is reported from since on (sinceCuts).
type sinceCut struct {
	// frac: the unattributed fraction of each of its steps in the range.
	frac float64
	// extraPoints, extraUSD: the chunk's attributed movement beyond that of
	// its steps in the range, added to its parts' slack.
	extraPoints, extraUSD float64
}

// sinceCuts is each chunk's cut at since (review of d3498bee, F3). Only one
// chunk can straddle since; the others keep their frac and add nothing. Of
// a split chunk with steps before since, P_b points (P_in in the range):
//
//	U_in = max(0, U_c − P_b − S_c): the true use before since is at most
//	       P_b + S_c (a whole percent at each end, a point of lag), so the
//	       true unattributed use in the range is at least U*_c − (P_b + S_c)
//	       ≥ U_in, and "at least" stays true of the range's part; spread
//	       over its steps by points (frac = U_in / P_in).
//	extra  = max(0, (P_c − U_c) − (P_in − U_in)): the chunk's attributed
//	       points not in the range's, the outside sessions' and covey's
//	       parts' share of it added to their slack. A part's true use in
//	       the range is at most its true use in the chunk, which its range
//	       bounds (≤ its chunk-level part + 2·s); the range's part plus
//	       s·extra is at least that chunk-level part. Without it a chunk of
//	       4 covey points then 3 outside ones cut between them showed the
//	       outside sessions' multiple as "≥ 14×" for a true 10×.
//
// A chunk with no call keeps frac 1 (unattributed whole, an estimate).
func sinceCuts(chunks []meterChunk, steps []shareStep, since time.Time) []sinceCut {
	cuts := make([]sinceCut, len(chunks))
	for j := range chunks {
		c := &chunks[j]
		cuts[j].frac = c.frac
		var before, in float64
		cut := false
		for _, k := range c.steps {
			if steps[k].At.Before(since) {
				cut = true
				before += steps[k].Points
			} else {
				in += steps[k].Points
			}
		}
		if !cut || !c.split() {
			continue
		}
		uIn := 0.0
		if c.unattributed > 0 {
			uIn = math.Max(0, c.unattributed-before-chunkSlackPoints)
		}
		cuts[j].frac = 0
		if in > 0 {
			cuts[j].frac = uIn / in
		}
		cuts[j].extraPoints = math.Max(0, (c.points-c.unattributed)-(in-uIn))
		if c.rate != nil {
			cuts[j].extraUSD = cuts[j].extraPoints * *c.rate
		}
	}
	return cuts
}

// splitSeat is F3 and F3c for one seat over its steps, in order: each step
// split by its chunk or not, with the seat's sums, its chunks' ranges and
// its agents. Every step keeps covey + outside + unattributed + unsplit = R,
// and each chunk's parts are the chunk-level split: covey_c = (P_c − U_c) ·
// rate · Lc_c / L_c, the outside sessions' likewise, placed over its steps
// by their movement.
func splitSeat(seat meterSeat, sources []string, steps []shareStep) (ExternalMeterTotal, []ExternalMeterStep) {
	return splitSeatSince(seat, sources, steps, time.Time{})
}

// splitSeatSince is splitSeat over steps that begin before the range: they
// are chunked from their first (seatSteps' anchor) and k may come from their
// chunks, but only the steps at or after since are reported — in the steps,
// the sums, the agents — and only the chunks with such a step in the ranges
// and counts.
func splitSeatSince(seat meterSeat, sources []string, steps []shareStep, since time.Time) (ExternalMeterTotal, []ExternalMeterStep) {
	tot := ExternalMeterTotal{SecretKey: seat.key, SecretSlot: seat.slot, Sources: append([]string{}, sources...),
		Agents: []ExternalMeterAgent{}, PlanRates: []ExternalMeterPlanRate{}}
	// Every linked source has a part, $0 until a split step gives it one.
	tot.SourceParts = []ExternalMeterSourcePart{}
	for _, src := range sources {
		tot.SourceParts = addSourceParts(tot.SourceParts, ExternalMeterSourcePart{Source: src})
	}
	out := make([]ExternalMeterStep, 0, len(steps))
	agents := map[uuid.UUID]*ExternalMeterAgent{}
	agent := func(id uuid.UUID) *ExternalMeterAgent {
		a, ok := agents[id]
		if !ok {
			a = &ExternalMeterAgent{AgentID: id}
			agents[id] = a
		}
		return a
	}
	reasons := make([]string, len(steps))
	for i, in := range steps {
		reasons[i] = stepReason(in)
	}
	_, chunks := seatChunks(steps, reasons)
	// k on the chunks as they closed; then the lag after a chunk joins it,
	// and U_c is taken on the chunks it left.
	tot.PlanRates = append(tot.PlanRates, chunkRates(chunks, steps)...)
	chunks, chunkOf := foldLag(chunks, steps, reasons)
	chunkListHigh(chunks, chunkOf, steps)
	chunkUnattributed(chunks, tot.PlanRates)
	cuts := sinceCuts(chunks, steps, since)
	// The seat's k: the one of the latest chunk whose plan price has one;
	// when that is not the latest plan price, the price it is at.
	var latest *float64
	for i := len(chunks) - 1; i >= 0 && tot.PointsPerListUSD == nil; i-- {
		if r := chunks[i].rate; r != nil {
			if latest == nil {
				latest = r
			}
			for _, pr := range tot.PlanRates {
				if pr.USDPerPoint == *r && pr.PointsPerListUSD != nil {
					v := *pr.PointsPerListUSD
					tot.PointsPerListUSD = &v
					if *r != *latest {
						at := *r
						tot.PointsPerListOlderPlanUSD = &at
					}
				}
			}
		}
	}

	hours := map[time.Time]bool{}
	rates := map[float64]bool{}
	excluded := floorCovExcluded(steps, since)
	excludedLead := floorCovExcludedBy(steps, since, true)
	for i, in := range steps {
		if in.At.Before(since) {
			continue
		}
		// The step's floor lists (FloorListUSD): its lists without what lay or
		// began before since.
		floorOut, floorCov := math.Max(0, in.LOut-in.BeforeSinceOut), math.Max(0, in.LCov-excluded[i])
		floorLead := math.Max(0, in.LOutLead-in.BeforeSinceOutLead) + math.Max(0, in.LCovLead-excludedLead[i])
		st := ExternalMeterStep{At: in.At, From: in.From, SecretKey: seat.key, SecretSlot: seat.slot,
			Points: in.Points, Reset: in.Reset, ExternalListUSD: in.LOut, CoveyListUSD: in.LCov,
			ExternalLeadUSD: in.LOutLead, CoveyLeadUSD: in.LCovLead, Reported: in.Reported}
		tot.StepCount++
		hours[in.At.Truncate(time.Hour)] = true
		tot.UnpricedCalls += in.UnpricedCalls
		tot.UnpricedTokens += in.UnpricedTokens
		unsplitList := func() {
			for id, l := range in.Agents {
				agent(id).UnsplitListUSD += l
			}
		}
		st.Reason = reasons[i]
		if st.Reason == SplitUsage {
			// Billed by usage then: no dent, no split, not unpriced.
			tot.UsageSteps++
			tot.UsagePoints += in.Points
			unsplitList()
			out = append(out, st)
			continue
		}
		if st.Reason == SplitUnpriced {
			tot.UnpricedSteps++
			tot.UnpricedPoints += in.Points
			unsplitList()
			out = append(out, st)
			continue
		}
		r := 0.0
		if in.Rate != nil {
			r = in.Points * *in.Rate
			if in.Points != 0 {
				rates[*in.Rate] = true
				tot.PointRate = math.Max(tot.PointRate, *in.Rate)
			}
		}
		st.RealUSD = &r
		tot.RealUSD += r
		tot.Points += in.Points
		tot.FloorListUSD += floorOut + floorCov
		tot.FloorLeadUSD += floorLead
		var c *meterChunk
		if chunkOf[i] >= 0 {
			c = &chunks[chunkOf[i]]
		}
		switch {
		case st.Reason == SplitNotReported || st.Reason == SplitOutsideUnpriced || c == nil || c.unknown():
			// c.unknown(): a chunk with no list, covey bookings without a
			// line and outside calls without a price: not split, counted with
			// the readings whose outside calls had no price (never covey's).
			st.UnsplitRealUSD = r
			tot.UnsplitRealUSD += r
			if in.Points != 0 {
				tot.UnsplitSteps++
			}
			if st.Reason == SplitOutsideUnpriced || (c != nil && c.unknown()) {
				tot.OutsideUnpricedRealUSD += r
				if in.Points != 0 {
					tot.OutsideUnpricedSteps++
				}
			}
			unsplitList()
		case c.noCall():
			// A chunk with no list at all: the meter moved and nobody
			// reported a call (lag, or another machine). Unattributed whole,
			// never covey's, never "not split". No covey line, so no agent
			// has a list here; an idle step of it has R = 0.
			st.UnattributedRealUSD, st.UnattributedPoints = r, in.Points
			tot.UnattributedNoCallRealUSD += r
			tot.UnattributedNoCallPoints += in.Points
		case st.Reason == SplitIdle:
			// Nothing to split (R = 0); the chunk's ranges see it at its own
			// price (#126 finding 26).
		default:
			// The chunk's unattributed part first, spread by points; the
			// rest by the chunk's list share (F3c), a step that moved with
			// no call alike.
			frac := cuts[chunkOf[i]].frac
			st.UnattributedRealUSD, st.UnattributedPoints = r*frac, in.Points*frac
			attributed, attrPoints := r-st.UnattributedRealUSD, in.Points-st.UnattributedPoints
			share := c.share()
			rOut := attributed * share
			rCov := attributed - rOut
			st.Split, st.ExternalRealUSD, st.CoveyRealUSD = true, &rOut, &rCov
			tot.SplitSteps++
			tot.ExternalRealUSD += rOut
			tot.CoveyRealUSD += rCov
			tot.SplitExternalListUSD += in.LOut
			tot.SplitCoveyListUSD += in.LCov
			tot.SplitExternalLeadUSD += in.LOutLead
			tot.SplitCoveyLeadUSD += in.LCovLead
			tot.SplitExternalFloorListUSD += floorOut
			tot.SplitCoveyFloorListUSD += floorCov
			tot.SplitPoints += in.Points
			tot.SplitExternalPoints += attrPoints * share
			tot.SplitCoveyPoints += attrPoints * (1 - share)
			for id, l := range in.Agents {
				agent(id).SplitListUSD += l
			}
			// The outside part goes to the sources by their share of the
			// chunk's L_out (Lo_s,c / Lo_c), as covey's goes to the agents;
			// each source's list is its own in the step.
			if c.lOut > 0 || len(in.SrcOut) > 0 {
				names := map[string]bool{}
				for src := range c.srcOut {
					names[src] = true
				}
				for src := range in.SrcOut {
					names[src] = true
				}
				for src := range names {
					p := ExternalMeterSourcePart{Source: src, ListUSD: in.SrcOut[src]}
					if c.lOut > 0 {
						p.RealUSD = rOut * c.srcOut[src] / c.lOut
					}
					st.SourceParts = addSourceParts(st.SourceParts, p)
				}
				tot.SourceParts = addSourceParts(tot.SourceParts, st.SourceParts...)
			}
			// covey's part goes to the agents by their share of the chunk's
			// L_cov, not the step's; in a chunk with no list (covey's whole),
			// by their share of its bookings without a line.
			if c.lCov > 0 {
				for id, l := range c.agents {
					agent(id).RealUSD += rCov * l / c.lCov
				}
			} else if c.list() <= 0 {
				for id, u := range c.unlinedAgents {
					agent(id).RealUSD += rCov * u / c.unlinedUSD
				}
			}
		}
		tot.UnattributedRealUSD += st.UnattributedRealUSD
		tot.UnattributedPoints += st.UnattributedPoints
		if st.UnattributedPoints > 0 {
			tot.UnattributedSteps++
		}
		out = append(out, st)
	}
	// The ranges, one term per split chunk with a step in the range (see
	// SplitRuns): S_c = 2 points times the part's share, at 2·½·TV of the
	// chunk's steps' prices in $; a chunk that begins before since adds the
	// attributed movement of its steps before it (sinceCuts).
	for j, c := range chunks {
		if !c.split() || steps[c.steps[len(c.steps)-1]].At.Before(since) {
			continue
		}
		if c.incomplete() {
			tot.IncompleteChunks++
		}
		tot.SplitRuns++
		cut := cuts[j]
		s, tv := c.share(), chunkSlackPoints*rateTV(steps, c.steps)+cut.extraUSD
		tot.SplitSlackPoints += chunkSlackPoints + cut.extraPoints
		tot.SplitSlackUSD += tv
		tot.SplitExternalSlackPoints += (chunkSlackPoints + cut.extraPoints) * s
		tot.SplitExternalSlackUSD += s * tv
		tot.SplitCoveySlackPoints += (chunkSlackPoints + cut.extraPoints) * (1 - s)
		tot.SplitCoveySlackUSD += (1 - s) * tv
		// The share's own range (review of e82d640f, finding 1): s takes each
		// spread line at its proportional part, which may put its list in the
		// wrong chunk (a back-loaded turn's in an earlier one). The chunk's
		// true outside share lies in [s_lo, s_hi]: s_lo = Lo_c / lHigh (every
		// spread line that ran in it whole), s_hi = Lo_c / (L_c − its spread
		// parts) (none of them), 0 without outside list. Each part's slack
		// adds the distance from s to its end of the range on the chunk's
		// attributed points in the range, its extra points and S_c.
		if lo, sp, rs := chunkShareRange(c, steps); lo < s || sp > s {
			var b float64
			for _, k := range c.steps {
				if !steps[k].At.Before(since) {
					b += steps[k].Points * (1 - cut.frac)
				}
			}
			b += cut.extraPoints + chunkSlackPoints
			tot.SplitExternalSlackPoints += b * (sp - s)
			tot.SplitExternalSlackUSD += b * (sp - s) * rs
			tot.SplitCoveySlackPoints += b * (s - lo)
			tot.SplitCoveySlackUSD += b * (s - lo) * rs
		}
	}
	tot.PointRateMixed = len(rates) > 1
	tot.HoursWithReading = len(hours)
	if split := tot.ExternalRealUSD + tot.CoveyRealUSD; split > 0 {
		sh := tot.ExternalRealUSD / split
		tot.Share = &sh
	}
	for _, a := range agents {
		tot.Agents = append(tot.Agents, *a)
	}
	sort.Slice(tot.Agents, func(i, j int) bool {
		if tot.Agents[i].SplitListUSD != tot.Agents[j].SplitListUSD {
			return tot.Agents[i].SplitListUSD > tot.Agents[j].SplitListUSD
		}
		return tot.Agents[i].AgentID.String() < tot.Agents[j].AgentID.String()
	})
	return tot, out
}

// meterSums puts the seats together: the steps in order, the hours they are
// placed in, and the whole. The slack in points is summed as it is; it is a
// figure for the whole only while every seat moved at one price
// (PointRateMixed false), the dollars always are.
func meterSums(totals []ExternalMeterTotal, steps []ExternalMeterStep) ExternalMeter {
	out := emptyMeter()
	rates := map[float64]bool{}
	for _, t := range totals {
		out.Credentials = append(out.Credentials, t)
		a, b := &out.ExternalMeterSums, t.ExternalMeterSums
		a.RealUSD += b.RealUSD
		a.ExternalRealUSD += b.ExternalRealUSD
		a.CoveyRealUSD += b.CoveyRealUSD
		a.UnsplitRealUSD += b.UnsplitRealUSD
		a.UnattributedRealUSD += b.UnattributedRealUSD
		a.UnattributedPoints += b.UnattributedPoints
		a.UnattributedSteps += b.UnattributedSteps
		a.UnattributedNoCallRealUSD += b.UnattributedNoCallRealUSD
		a.UnattributedNoCallPoints += b.UnattributedNoCallPoints
		a.IncompleteChunks += b.IncompleteChunks
		a.SplitExternalListUSD += b.SplitExternalListUSD
		a.SplitCoveyListUSD += b.SplitCoveyListUSD
		a.SplitExternalLeadUSD += b.SplitExternalLeadUSD
		a.SplitCoveyLeadUSD += b.SplitCoveyLeadUSD
		a.FloorListUSD += b.FloorListUSD
		a.FloorLeadUSD += b.FloorLeadUSD
		a.SplitExternalFloorListUSD += b.SplitExternalFloorListUSD
		a.SplitCoveyFloorListUSD += b.SplitCoveyFloorListUSD
		a.Points += b.Points
		a.SplitPoints += b.SplitPoints
		a.SplitExternalPoints += b.SplitExternalPoints
		a.SplitCoveyPoints += b.SplitCoveyPoints
		a.SplitRuns += b.SplitRuns
		a.SplitSlackPoints += b.SplitSlackPoints
		a.SplitSlackUSD += b.SplitSlackUSD
		a.SplitExternalSlackPoints += b.SplitExternalSlackPoints
		a.SplitExternalSlackUSD += b.SplitExternalSlackUSD
		a.SplitCoveySlackPoints += b.SplitCoveySlackPoints
		a.SplitCoveySlackUSD += b.SplitCoveySlackUSD
		a.PointRate = math.Max(a.PointRate, b.PointRate)
		a.PointRateMixed = a.PointRateMixed || b.PointRateMixed
		if b.PointRate > 0 {
			rates[b.PointRate] = true
		}
		a.StepCount += b.StepCount
		a.SplitSteps += b.SplitSteps
		a.UnsplitSteps += b.UnsplitSteps
		a.OutsideUnpricedSteps += b.OutsideUnpricedSteps
		a.OutsideUnpricedRealUSD += b.OutsideUnpricedRealUSD
		a.UnpricedSteps += b.UnpricedSteps
		a.UnpricedPoints += b.UnpricedPoints
		a.UsageSteps += b.UsageSteps
		a.UsagePoints += b.UsagePoints
		a.UnpricedCalls += b.UnpricedCalls
		a.UnpricedTokens += b.UnpricedTokens
		a.UnmeteredListUSD += b.UnmeteredListUSD
		a.UnmeteredCoveyListUSD += b.UnmeteredCoveyListUSD
		a.UnmeteredBeforeListUSD += b.UnmeteredBeforeListUSD
		a.UnmeteredCoveyBeforeListUSD += b.UnmeteredCoveyBeforeListUSD
		a.UnmeteredLeadUSD += b.UnmeteredLeadUSD
		a.UnmeteredCoveyLeadUSD += b.UnmeteredCoveyLeadUSD
		a.UnmeteredBeforeLeadUSD += b.UnmeteredBeforeLeadUSD
		a.UnmeteredCoveyBeforeLeadUSD += b.UnmeteredCoveyBeforeLeadUSD
		a.SourceParts = addSourceParts(a.SourceParts, b.SourceParts...)
	}
	out.PointRateMixed = out.PointRateMixed || len(rates) > 1
	if split := out.ExternalRealUSD + out.CoveyRealUSD; split > 0 {
		sh := out.ExternalRealUSD / split
		out.Share = &sh
	}

	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i], steps[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.SecretKey != b.SecretKey {
			return a.SecretKey < b.SecretKey
		}
		return a.SecretSlot < b.SecretSlot
	})
	out.Steps = append(out.Steps, steps...)
	type hk struct {
		h    time.Time
		key  string
		slot int
	}
	byHour := map[hk]*ExternalMeterHour{}
	for _, st := range steps {
		k := hk{st.At.Truncate(time.Hour), st.SecretKey, st.SecretSlot}
		h, ok := byHour[k]
		if !ok {
			h = &ExternalMeterHour{Hour: k.h, SecretKey: k.key, SecretSlot: k.slot, SourceParts: []ExternalMeterSourcePart{}}
			byHour[k] = h
		}
		h.Steps++
		h.ExternalListUSD += st.ExternalListUSD
		h.CoveyListUSD += st.CoveyListUSD
		if st.RealUSD == nil {
			if st.Reason == SplitUsage {
				h.UsagePoints += st.Points
			} else {
				h.UnpricedPoints += st.Points
			}
			continue
		}
		h.RealUSD = addUSD(h.RealUSD, *st.RealUSD)
		h.Points += st.Points
		h.UnsplitRealUSD += st.UnsplitRealUSD
		h.UnattributedRealUSD += st.UnattributedRealUSD
		h.UnattributedPoints += st.UnattributedPoints
		if st.UnattributedPoints > 0 {
			h.UnattributedSteps++
		}
		if st.Split {
			h.SplitSteps++
			h.ExternalRealUSD += *st.ExternalRealUSD
			h.CoveyRealUSD += *st.CoveyRealUSD
			h.SourceParts = addSourceParts(h.SourceParts, st.SourceParts...)
		}
	}
	for _, h := range byHour {
		out.Hours = append(out.Hours, *h)
	}
	sort.Slice(out.Hours, func(i, j int) bool {
		a, b := out.Hours[i], out.Hours[j]
		if !a.Hour.Equal(b.Hour) {
			return a.Hour.Before(b.Hour)
		}
		if a.SecretKey != b.SecretKey {
			return a.SecretKey < b.SecretKey
		}
		return a.SecretSlot < b.SecretSlot
	})
	return out
}
