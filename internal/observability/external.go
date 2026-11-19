package observability

import (
	"context"
	"errors"
	"fmt"
	"math"
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
//     list; k is taken on the chunks as they closed, before this.
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
	// externalMaxHosts bounds the hosts one push reports through.
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
// sent; nil when none did), and PushedAt, its latest push.
type ExternalHost struct {
	Host            string     `json:"host"`
	ReportedThrough *time.Time `json:"reported_through"`
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
		if _, err := tx.Exec(ctx, `INSERT INTO external_usage_hosts (org_id, source, host, reported_through, pushed_at)
			SELECT $1, $2, h.host, h.at, $5 FROM unnest($3::text[], $4::timestamptz[]) AS h(host, at)
			ON CONFLICT (org_id, source, host) DO UPDATE SET
				reported_through = GREATEST(external_usage_hosts.reported_through, EXCLUDED.reported_through),
				pushed_at = GREATEST(external_usage_hosts.pushed_at, EXCLUDED.pushed_at)`,
			orgID, source, hosts, ats, now.UTC()); err != nil {
			return res, err
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
	rows, err := q.Query(ctx, `SELECT source, host, reported_through, pushed_at FROM external_usage_hosts
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
		if err := rows.Scan(&src, &h.Host, &h.ReportedThrough, &h.PushedAt); err != nil {
			return nil, err
		}
		if h.ReportedThrough != nil {
			at := h.ReportedThrough.UTC()
			h.ReportedThrough = &at
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
	UnpricedTokens int64      `json:"unpriced_tokens"`
	UnpricedCalls  int64      `json:"unpriced_calls"`
	UnpricedModels []string   `json:"unpriced_models"`
	FirstAt        *time.Time `json:"first_at"`
	LastAt         *time.Time `json:"last_at"`
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
}

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
	// IncompleteChunks: chunks with list price whose use is not all in it —
	// outside calls without a price, or covey bookings without a line. They
	// set no rate and have no unattributed part, so their parts may hold
	// use of another machine, and the use of those calls is shared at the
	// priced calls' shares.
	IncompleteChunks int `json:"incomplete_chunks"`
	// Share: ExternalRealUSD over the split steps' real cost; nil without
	// one.
	Share *float64 `json:"share"`
	// SplitExternalListUSD / SplitCoveyListUSD: L_out and L_cov over the
	// split steps, the list prices the three multiples divide.
	SplitExternalListUSD float64 `json:"split_external_list_usd"`
	SplitCoveyListUSD    float64 `json:"split_covey_list_usd"`
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
	// SplitRuns: since F3c the number of chunks with list price (the field
	// keeps its name). The meter shows whole percents, so each chunk's
	// movement is known to ±1 point from its two end readings:
	// SplitSlackPoints / SplitSlackUSD, the attributed whole's range. A
	// part's range is ±½·TV over each chunk's steps of its coefficient,
	// TV = f_1 + Σ|f_{k+1} − f_k| + f_n with f_k = s_c·rate_k (s_c the
	// chunk's outside share Lo_c / L_c, 1 − s_c for covey, 1 for the whole;
	// rate_k each reading's own plan price per point, 0 without a plan):
	// Split{External,Covey}Slack{Points,USD}. Within a chunk s_c and the
	// attributed fraction a_c = 1 − U_c / P_c are one number each, so a
	// part's true coefficient a_c·s_c·rate_k has a TV at most the one given.
	// Across chunks a_c changes, and a sum over contiguous chunks would not
	// bound it: each chunk is its own term, so the ranges stay wide enough.
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
	// one: the lowest rate (points per list $) of a chunk with every call
	// priced that moved 10 points or more, at the chunk's upper end; nil
	// without such a chunk, when only chunks with no list are
	// unattributed. PlanRates: k per plan price, in the order the prices
	// first appear (points are percent of one plan's limit, so each plan
	// has its own).
	PointsPerListUSD *float64                `json:"points_per_list_usd"`
	PlanRates        []ExternalMeterPlanRate `json:"plan_rates"`
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
		GROUP BY 1, 2, 3, 4`, orgID, since)
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
		tot, st := splitSeat(seat, linked[seat], in)
		tot.MeterSource = cov.Source
		if tot.UnmeteredListUSD, err = s.unmeteredList(ctx, orgID, since, linked[seat], in); err != nil {
			return out, err
		}
		totals, steps = append(totals, tot), append(steps, st...)
	}
	out.Meter = meterSums(totals, steps)
	return out, nil
}

func emptyMeter() ExternalMeter {
	return ExternalMeter{Steps: []ExternalMeterStep{}, Hours: []ExternalMeterHour{}, Credentials: []ExternalMeterTotal{}}
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
	// Agents: each agent's part of LCov.
	Agents                        map[uuid.UUID]float64
	UnpricedCalls, UnpricedTokens int64
	// UnlinedBookings: covey bookings on the seat in the span that no line
	// was linked to (an older daemon, a run cut off): use that moved the
	// meter but is not in LCov.
	UnlinedBookings int64
	Reported        bool
	// Usage: read while the seat's basis was usage (0151); its points are
	// the usage charges', not the plan's.
	Usage bool
}

// seatSteps builds a seat's steps from its meter's readings in the window
// (the source the page reads), each with the lists of its span and whether
// it was reported. A meter read only before the window, or never, has none.
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
	var steps []shareStep
	for k := 1; k < len(rs); k++ {
		r := rs[k]
		if r.at.Before(since) {
			continue
		}
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
	if err := s.stepLists(ctx, orgID, seat, sources, steps); err != nil {
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
// in one step at most.
func (s *Store) stepLists(ctx context.Context, orgID uuid.UUID, seat meterSeat, sources []string, steps []shareStep) error {
	from, to := spanArrays(steps)
	if len(sources) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT s.i, e.model, e.reported_usd IS NOT NULL, count(*),
			SUM(e.input), SUM(e.output), SUM(e.cache_read), SUM(e.cache_write_5m), SUM(e.cache_write_1h),
			COALESCE(SUM(e.reported_usd), 0)::float8
			FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS s(f, t, i)
			JOIN external_usage e ON e.org_id = $1 AND e.source = ANY($4) AND e.ts > s.f AND e.ts <= s.t
			GROUP BY 1, 2, 3`, orgID, from, to, sources)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				i           int64
				model       string
				hasReported bool
				calls       int64
				t           ModelTokens
				reported    float64
			)
			if err := rows.Scan(&i, &model, &hasReported, &calls, &t.Input, &t.Output, &t.CacheRead,
				&t.CacheWrite5m, &t.CacheWrite1h, &reported); err != nil {
				rows.Close()
				return err
			}
			st := &steps[i-1]
			if usd, ok := externalStepUSD(model, t, hasReported, reported); ok {
				st.LOut += usd
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

	rows, err := s.pool.Query(ctx, `SELECT s.i, m.agent_id, m.model,
		SUM(m.input_tokens), SUM(m.output_tokens), SUM(m.cache_read_tokens), SUM(m.cache_write_5m_tokens),
		SUM(m.cache_write_1h_tokens), SUM(m.web_search_requests), SUM(m.engine_usd)::float8
		FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS s(f, t, i)
		JOIN model_usage m ON m.org_id = $1 AND m.secret_key = $4 AND m.secret_slot = $5
			AND m.created_at > s.f AND m.created_at <= s.t
		GROUP BY 1, 2, 3`, orgID, from, to, seat.key, seat.slot)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			i      int64
			agent  uuid.UUID
			model  string
			t      ModelTokens
			engine float64
		)
		if err := rows.Scan(&i, &agent, &model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m,
			&t.CacheWrite1h, &t.WebSearches, &engine); err != nil {
			return err
		}
		usd := engine
		if pr, ok := ListPrice(model); ok {
			usd = pr.Cost(t)
		}
		st := &steps[i-1]
		st.LCov += usd
		if st.Agents == nil {
			st.Agents = map[uuid.UUID]float64{}
		}
		st.Agents[agent] += usd
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Bookings on the seat no line was linked to (as the cost cells'
	// measured "unattributed" type reads them): their use is not in L_cov,
	// so the step's chunk sets no rate and has no unattributed part (F3c).
	rows, err = s.pool.Query(ctx, `SELECT s.i, count(*)
		FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS s(f, t, i)
		JOIN cost_entries ce ON ce.secret_key = $4 AND ce.secret_slot = $5
			AND ce.created_at > s.f AND ce.created_at <= s.t AND ce.usd > 0.0005
		JOIN agents a ON a.id = ce.agent_id AND a.org_id = $1
		WHERE NOT EXISTS (SELECT 1 FROM model_usage m WHERE m.cost_entry_id = ce.id)
		GROUP BY 1`, orgID, from, to, seat.key, seat.slot)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var i, n int64
		if err := rows.Scan(&i, &n); err != nil {
			return err
		}
		steps[i-1].UnlinedBookings += n
	}
	return rows.Err()
}

// unmeteredList is L_out of the linked calls in the window that lie in no
// step's span: the steps' spans are contiguous, from the first step's
// previous reading through the last reading, so it is every call outside
// that stretch.
func (s *Store) unmeteredList(ctx context.Context, orgID uuid.UUID, since time.Time, sources []string, steps []shareStep) (float64, error) {
	if len(sources) == 0 {
		return 0, nil
	}
	// No step: an empty stretch, (since, since].
	from, to := since, since
	if len(steps) > 0 {
		from, to = steps[0].From, steps[len(steps)-1].At
	}
	rows, err := s.pool.Query(ctx, `SELECT model, reported_usd IS NOT NULL,
		SUM(input), SUM(output), SUM(cache_read), SUM(cache_write_5m), SUM(cache_write_1h),
		COALESCE(SUM(reported_usd), 0)::float8
		FROM external_usage
		WHERE org_id = $1 AND source = ANY($2) AND ts >= $3 AND NOT (ts > $4 AND ts <= $5)
		GROUP BY 1, 2`, orgID, sources, since, from, to)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var usd float64
	for rows.Next() {
		var (
			model       string
			hasReported bool
			t           ModelTokens
			reported    float64
		)
		if err := rows.Scan(&model, &hasReported, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m,
			&t.CacheWrite1h, &reported); err != nil {
			return 0, err
		}
		if v, ok := externalStepUSD(model, t, hasReported, reported); ok {
			usd += v
		}
	}
	return usd, rows.Err()
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
// binary search.
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
	calls := map[hostKey][]time.Time{}
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
		if _, ok := known[k]; !ok {
			// Rows of a host the hosts table does not know (uploaded before
			// it existed): active by its calls, and with no watermark.
			known[k] = ExternalHost{Host: k.host}
			keys = append(keys, k)
		}
		calls[k] = append(calls[k], ts.UTC())
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	// small part of it.
	chunkMinPoints = 5
	// rateMinPoints: a chunk sets the rate k only from this much movement
	// on (its points without a reset step's): below it the slack S_c is too
	// large a part of it.
	rateMinPoints = 10
	// chunkSlackPoints is S_c: one point for the whole percents at the
	// chunk's two ends and one of lag at its end (a reading may trail the
	// use before it by a point, as meterQuery reads a drop of one).
	chunkSlackPoints = 2.0
)

// meterChunk is one chunk of a seat's steps (F3c).
type meterChunk struct {
	// steps: the indexes of its steps, in order.
	steps []int
	// points: P_c; lOut, lCov: Lo_c and Lc_c; agents: each agent's part
	// of Lc_c.
	points, lOut, lCov float64
	agents             map[uuid.UUID]float64
	// kPoints, kList: its points and list without a reset step's, for k.
	kPoints, kList float64
	// rate: the plan price per point of its steps that moved or have list
	// (one per chunk: a change of price closes it); nil when none has one.
	rate *float64
	// incomplete: a step of it had outside calls without a price or covey
	// bookings without a line: use that is not in its list.
	incomplete bool
	// unattributed: U_c; frac: U_c / P_c, the fraction of each step's
	// movement that is unattributed (1 for a chunk with no list).
	unattributed, frac float64
	// follows: the chunk this one was opened right after, when that one
	// closed at its size at the step before (nothing closed it before this
	// step); −1 otherwise. A chunk with no list that follows one is the
	// meter's lag after it and is folded into it (foldLag).
	follows int
}

func (c *meterChunk) list() float64 { return c.lOut + c.lCov }

// share is the chunk's outside share Lo_c / L_c; 0 without list.
func (c *meterChunk) share() float64 {
	if l := c.list(); l > 0 {
		return c.lOut / l
	}
	return 0
}

// seatChunks walks a seat's steps in time order into chunks (F3c): a split,
// idle or moved_without_reported_call step joins the current chunk, opening
// one when none is open, and the chunk closes at the reading its points
// reach chunkMinPoints. A not_reported, outside_unpriced, unpriced or usage step
// closes it before itself and stays in none; a reset closes it before
// itself, and so does a step that moved or has list at another plan price
// than the chunk's (an idle step, with neither, rides along at its own
// price). chunkOf[i] is step i's chunk, −1 for none. A chunk opened right
// after one that closed at its size, with nothing closing it in between
// (no unsplit or unpriced step, no reset, no change of plan price), records
// it in follows.
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
				if r := chunks[cur].rate; r != nil && *r != *in.Rate {
					cur = -1
				}
			} else if sized >= 0 {
				if r := chunks[sized].rate; r != nil && *r != *in.Rate {
					sized = -1
				}
			}
		}
		if cur < 0 {
			chunks = append(chunks, meterChunk{follows: sized})
			cur = len(chunks) - 1
		}
		sized = -1
		c := &chunks[cur]
		chunkOf[i] = cur
		c.steps = append(c.steps, i)
		c.points += in.Points
		c.lOut += in.LOut
		c.lCov += in.LCov
		if !in.Reset {
			c.kPoints += in.Points
			c.kList += in.LOut + in.LCov
		}
		if counts && in.Rate != nil && c.rate == nil {
			c.rate = in.Rate
		}
		if in.UnpricedCalls > 0 || in.UnlinedBookings > 0 {
			c.incomplete = true
		}
		for id, l := range in.Agents {
			if c.agents == nil {
				c.agents = map[uuid.UUID]float64{}
			}
			c.agents[id] += l
		}
		if c.points >= chunkMinPoints {
			cur, sized = -1, cur
		}
	}
	return chunkOf, chunks
}

// foldLag folds each chunk with no list that follows a chunk closed at its
// size into that chunk (F3c): a run of readings that moved with no call, or
// did not move, right after use is the meter's lag and carry, not another
// machine. Only movement with no adjacent chunk — at the start of the range,
// or after an unsplit or unpriced step, a reset or a change of plan price —
// stays a chunk of its own, unattributed whole. A chain of such chunks folds
// into the first one. It returns the chunks left and the steps' new chunk
// indexes; k is taken before it (chunkRates), on the chunks as they closed.
func foldLag(chunks []meterChunk, chunkOf []int) ([]meterChunk, []int) {
	root := make([]int, len(chunks))
	var out []meterChunk
	for i, c := range chunks {
		if c.list() <= 0 && c.follows >= 0 {
			r := root[c.follows]
			t := &out[r]
			t.steps = append(t.steps, c.steps...)
			t.points += c.points
			t.incomplete = t.incomplete || c.incomplete
			root[i] = r
			continue
		}
		root[i] = len(out)
		out = append(out, c)
	}
	for i, c := range chunkOf {
		if c >= 0 {
			chunkOf[i] = root[c]
		}
	}
	return out, chunkOf
}

// chunkRates is F3c's k per plan price: min over the complete chunks at
// that price with kPoints ≥ rateMinPoints and kList > 0 of (kPoints + S_c) /
// kList. It returns the prices in the order they first appear, with k (nil
// when none).
func chunkRates(chunks []meterChunk) []ExternalMeterPlanRate {
	var rates []ExternalMeterPlanRate
	at := map[float64]int{}
	for _, c := range chunks {
		if c.rate == nil {
			continue
		}
		i, ok := at[*c.rate]
		if !ok {
			i = len(rates)
			at[*c.rate] = i
			rates = append(rates, ExternalMeterPlanRate{USDPerPoint: *c.rate})
		}
		if c.incomplete || c.kPoints < rateMinPoints || c.kList <= 0 {
			continue
		}
		if v := (c.kPoints + chunkSlackPoints) / c.kList; rates[i].PointsPerListUSD == nil || v < *rates[i].PointsPerListUSD {
			rates[i].PointsPerListUSD = &v
		}
	}
	return rates
}

// chunkUnattributed sets each chunk's U_c: max(0, P_c − S_c − k·L_c) at its
// plan price's k, P_c whole for a chunk with no list, 0 without k and for
// an incomplete chunk.
func chunkUnattributed(chunks []meterChunk, rates []ExternalMeterPlanRate) {
	at := map[float64]int{}
	for i, r := range rates {
		at[r.USDPerPoint] = i
	}
	for i := range chunks {
		c := &chunks[i]
		switch {
		case c.list() <= 0:
			// The meter moved and nobody reported a call in the whole chunk.
			c.unattributed, c.frac = c.points, 1
		case c.incomplete || c.rate == nil:
		default:
			k := rates[at[*c.rate]].PointsPerListUSD
			if k == nil {
				continue
			}
			if u := c.points - chunkSlackPoints - *k*c.list(); u > 0 && c.points > 0 {
				c.unattributed, c.frac = u, u/c.points
			}
		}
	}
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

// splitSeat is F3 and F3c for one seat over its steps, in order: each step
// split by its chunk or not, with the seat's sums, its chunks' ranges and
// its agents. Every step keeps covey + outside + unattributed + unsplit = R,
// and each chunk's parts are the chunk-level split: covey_c = (P_c − U_c) ·
// rate · Lc_c / L_c, the outside sessions' likewise, placed over its steps
// by their movement.
func splitSeat(seat meterSeat, sources []string, steps []shareStep) (ExternalMeterTotal, []ExternalMeterStep) {
	tot := ExternalMeterTotal{SecretKey: seat.key, SecretSlot: seat.slot, Sources: append([]string{}, sources...),
		Agents: []ExternalMeterAgent{}, PlanRates: []ExternalMeterPlanRate{}}
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
	chunkOf, chunks := seatChunks(steps, reasons)
	// k on the chunks as they closed at 5 points; then the lag after a chunk
	// joins it, and U_c is taken on the chunks it left.
	tot.PlanRates = append(tot.PlanRates, chunkRates(chunks)...)
	chunks, chunkOf = foldLag(chunks, chunkOf)
	chunkUnattributed(chunks, tot.PlanRates)
	// The seat's k: the one of the latest chunk whose plan price has one.
	for i := len(chunks) - 1; i >= 0 && tot.PointsPerListUSD == nil; i-- {
		if r := chunks[i].rate; r != nil {
			for _, pr := range tot.PlanRates {
				if pr.USDPerPoint == *r && pr.PointsPerListUSD != nil {
					v := *pr.PointsPerListUSD
					tot.PointsPerListUSD = &v
				}
			}
		}
	}

	hours := map[time.Time]bool{}
	rates := map[float64]bool{}
	for i, in := range steps {
		st := ExternalMeterStep{At: in.At, From: in.From, SecretKey: seat.key, SecretSlot: seat.slot,
			Points: in.Points, Reset: in.Reset, ExternalListUSD: in.LOut, CoveyListUSD: in.LCov, Reported: in.Reported}
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
		var c *meterChunk
		if chunkOf[i] >= 0 {
			c = &chunks[chunkOf[i]]
		}
		switch {
		case st.Reason == SplitNotReported || st.Reason == SplitOutsideUnpriced || c == nil:
			st.UnsplitRealUSD = r
			tot.UnsplitRealUSD += r
			if in.Points != 0 {
				tot.UnsplitSteps++
			}
			if st.Reason == SplitOutsideUnpriced {
				tot.OutsideUnpricedRealUSD += r
				if in.Points != 0 {
					tot.OutsideUnpricedSteps++
				}
			}
			unsplitList()
		case c.list() <= 0:
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
			st.UnattributedRealUSD, st.UnattributedPoints = r*c.frac, in.Points*c.frac
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
			tot.SplitPoints += in.Points
			tot.SplitExternalPoints += attrPoints * share
			tot.SplitCoveyPoints += attrPoints * (1 - share)
			for id, l := range in.Agents {
				agent(id).SplitListUSD += l
			}
			// covey's part goes to the agents by their share of the chunk's
			// L_cov, not the step's.
			if c.lCov > 0 {
				for id, l := range c.agents {
					agent(id).RealUSD += rCov * l / c.lCov
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
	// The ranges, one term per chunk with list price (see SplitRuns).
	for _, c := range chunks {
		if c.incomplete && c.list() > 0 {
			tot.IncompleteChunks++
		}
		if c.list() <= 0 {
			continue
		}
		tot.SplitRuns++
		s, tv := c.share(), rateTV(steps, c.steps)
		tot.SplitSlackPoints++
		tot.SplitSlackUSD += tv
		tot.SplitExternalSlackPoints += s
		tot.SplitExternalSlackUSD += s * tv
		tot.SplitCoveySlackPoints += 1 - s
		tot.SplitCoveySlackUSD += (1 - s) * tv
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
			h = &ExternalMeterHour{Hour: k.h, SecretKey: k.key, SecretSlot: k.slot}
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
