package observability

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
//   - F3, the meter share, per UTC hour h and meter credential c:
//     R_out = R · L_out / (L_out + L_cov), with R the meter's real cost (the
//     priced steps of the credential's meter, as on the page), L_out F1 over
//     the calls of the sources linked to c and L_cov covey's recomputed list
//     price on c. L_out = 0 gives R_out = 0; an hour without a reading has
//     no R and so no R_out either.

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
type ExternalUploadResult struct {
	Source          string           `json:"source"`
	Received        int              `json:"received"`
	Inserted        int              `json:"inserted"`
	Updated         int              `json:"updated"`
	Unchanged       int              `json:"unchanged"`
	MeterCredential *MeterCredential `json:"meter_credential"`
}

// ValidateExternalSource checks a source label.
func ValidateExternalSource(source string) error {
	if source == "" {
		return ErrInvalidExternal{Reason: "source is required"}
	}
	if !utf8.ValidString(source) || utf8.RuneCountInString(source) > externalMaxSource {
		return ErrInvalidExternal{Reason: fmt.Sprintf("source must be at most %d characters", externalMaxSource)}
	}
	return nil
}

// ValidateExternalRows checks an upload's rows against #120's rules: every
// required field present, counts ≥ 0, ts not after now + 5 min, at most
// ExternalMaxRows, each message_id once.
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
			if f.v == "" {
				return bad("%s is required", f.name)
			}
			if !utf8.ValidString(f.v) || len(f.v) > externalMaxField {
				return bad("%s must be valid UTF-8 of at most %d bytes", f.name, externalMaxField)
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
// replaces the one with its (source, message_id) or is added, and the
// source's meter link is changed as the upload says — in one transaction, so
// a refused link stores no row either.
func (s *Store) UpsertExternalUsage(ctx context.Context, orgID uuid.UUID, source string, link MeterLinkChange,
	rows []ExternalRow, now time.Time) (ExternalUploadResult, error) {
	res := ExternalUploadResult{Source: source, Received: len(rows)}
	if err := ValidateExternalSource(source); err != nil {
		return res, err
	}
	if err := ValidateExternalRows(rows, now); err != nil {
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
	return res, tx.Commit(ctx)
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
// and its meter link.
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
	// meter with; nil without one (no meter share).
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

// ExternalMeter is F3 over the window: per UTC hour and linked credential,
// and summed.
type ExternalMeter struct {
	Hours       []ExternalMeterHour  `json:"hours"`
	Credentials []ExternalMeterTotal `json:"credentials"`
	// RealUSD: Σ R over the hours with a reading, every linked credential;
	// ExternalRealUSD: Σ R_out over the same hours; Share the one over the
	// other (nil without real cost). The tile's figures.
	RealUSD         float64  `json:"real_usd"`
	ExternalRealUSD float64  `json:"external_real_usd"`
	Share           *float64 `json:"share"`
	// UnmeteredListUSD: L_out of hours without a reading, whose share has no
	// figure.
	UnmeteredListUSD float64 `json:"unmetered_list_usd"`
}

// ExternalMeterHour is one UTC hour of one linked credential's meter.
type ExternalMeterHour struct {
	Hour       time.Time `json:"hour"`
	SecretKey  string    `json:"secret_key"`
	SecretSlot int       `json:"secret_slot"`
	// RealUSD: R, the meter's priced movement in the hour; nil without a
	// reading (or with points no plan prices).
	RealUSD *float64 `json:"real_usd"`
	// ExternalListUSD: L_out; CoveyListUSD: L_cov.
	ExternalListUSD float64 `json:"external_list_usd"`
	CoveyListUSD    float64 `json:"covey_list_usd"`
	// ExternalRealUSD: R_out; CoveyRealUSD: R − R_out. Both nil with R.
	ExternalRealUSD *float64 `json:"external_real_usd"`
	CoveyRealUSD    *float64 `json:"covey_real_usd"`
}

// ExternalMeterTotal is one linked credential over the window.
type ExternalMeterTotal struct {
	SecretKey  string `json:"secret_key"`
	SecretSlot int    `json:"secret_slot"`
	// MeterSource: the reading source R is taken from (the one the page
	// picks: most readings, usage_cmd before manual before inband); empty
	// without readings in the window.
	MeterSource string   `json:"meter_source"`
	Sources     []string `json:"sources"`
	// Hours with a reading, and hours with outside calls but none.
	HoursWithReading    int      `json:"hours_with_reading"`
	HoursWithoutReading int      `json:"hours_without_reading"`
	RealUSD             float64  `json:"real_usd"`
	ExternalRealUSD     float64  `json:"external_real_usd"`
	Share               *float64 `json:"share"`
	UnmeteredListUSD    float64  `json:"unmetered_list_usd"`
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
// chart's bucket, and the meter share per hour. hourly returns the meter's
// steps cut per UTC hour with their coverage; it is only called when a source
// is linked to a meter.
func (s *Store) externalFor(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time,
	hourly func() ([]MeterStep, []MeterCoverage, error)) (ExternalUsage, error) {
	out := ExternalUsage{Sources: []ExternalSource{}, Cells: []ExternalCell{},
		Meter: ExternalMeter{Hours: []ExternalMeterHour{}, Credentials: []ExternalMeterTotal{}}}

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
			src = &ExternalSource{Source: name, UnpricedModels: []string{}}
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
	for name, src := range sources {
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

	if len(links) == 0 {
		return out, nil
	}
	lOut, lCov, err := s.meterLists(ctx, orgID, since, links)
	if err != nil {
		return out, err
	}
	steps, coverage, err := hourly()
	if err != nil {
		return out, err
	}
	out.Meter = meterShare(links, steps, coverage, lOut, lCov)
	return out, nil
}

// originOrder sorts a cell's origin: main, subagent, workflow, unsaid.
func originOrder(o *string) string {
	if o == nil {
		return "~"
	}
	return *o
}

// credHour is one UTC hour of one credential.
type credHour struct {
	hour time.Time
	key  string
	slot int
}

// meterLists are L_out and L_cov per UTC hour and linked credential: the
// outside calls of the sources linked to it priced with F1 (a model the list
// does not know has no figure and is left out), and covey's recomputed list
// price of the credential's lines (a model the list does not know at the
// engine's figure, as the agents' recomputed figure counts it).
func (s *Store) meterLists(ctx context.Context, orgID uuid.UUID, since time.Time, links map[string]MeterCredential) (map[credHour]float64, map[credHour]float64, error) {
	lOut, lCov := map[credHour]float64{}, map[credHour]float64{}
	names := make([]string, 0, len(links))
	keys := map[string]bool{}
	for name, m := range links {
		names = append(names, name)
		keys[m.SecretKey] = true
	}
	rows, err := s.pool.Query(ctx, `SELECT date_trunc('hour', ts, 'UTC'), source, model,
		SUM(input), SUM(output), SUM(cache_read), SUM(cache_write_5m), SUM(cache_write_1h)
		FROM external_usage
		WHERE org_id = $1 AND ts >= $2 AND source = ANY($3)
		GROUP BY 1, 2, 3`, orgID, since, names)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var (
			h           time.Time
			name, model string
			t           ModelTokens
		)
		if err := rows.Scan(&h, &name, &model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if usd := ExternalListUSD(model, t); usd != nil {
			m := links[name]
			lOut[credHour{h.UTC(), m.SecretKey, m.SecretSlot}] += *usd
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	secretKeys := make([]string, 0, len(keys))
	for k := range keys {
		secretKeys = append(secretKeys, k)
	}
	rows, err = s.pool.Query(ctx, `SELECT date_trunc('hour', created_at, 'UTC'), secret_key, secret_slot, model,
		SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_5m_tokens),
		SUM(cache_write_1h_tokens), SUM(web_search_requests), SUM(engine_usd)::float8
		FROM model_usage
		WHERE org_id = $1 AND created_at >= $2 AND secret_key = ANY($3)
		GROUP BY 1, 2, 3, 4`, orgID, since, secretKeys)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			h      time.Time
			key    string
			slot   int
			model  string
			t      ModelTokens
			engine float64
		)
		if err := rows.Scan(&h, &key, &slot, &model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m,
			&t.CacheWrite1h, &t.WebSearches, &engine); err != nil {
			return nil, nil, err
		}
		k := credHour{h.UTC(), key, slot}
		if pr, ok := ListPrice(model); ok {
			lCov[k] += pr.Cost(t)
		} else {
			lCov[k] += engine
		}
	}
	return lOut, lCov, rows.Err()
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

// stepReal is R of one hourly step: its priced movement, nil when some of its
// points have no plan price. No movement is $0 whatever the plan.
func stepReal(st MeterStep) *float64 {
	if st.Points == 0 {
		zero := 0.0
		return &zero
	}
	if st.USD == nil || st.UnpricedPoints > 0 {
		return nil
	}
	usd := *st.USD
	return &usd
}

// meterShare is F3. steps must be cut per UTC hour. An hour enters when the
// credential's meter was read in it, or when calls of a linked source were
// made in it; only the first kind has a figure.
func meterShare(links map[string]MeterCredential, steps []MeterStep, coverage []MeterCoverage,
	lOut, lCov map[credHour]float64) ExternalMeter {
	out := ExternalMeter{Hours: []ExternalMeterHour{}, Credentials: []ExternalMeterTotal{}}
	type cred struct {
		key  string
		slot int
	}
	byCred := map[cred][]string{}
	for name, m := range links {
		c := cred{m.SecretKey, m.SecretSlot}
		byCred[c] = append(byCred[c], name)
	}
	creds := make([]cred, 0, len(byCred))
	for c := range byCred {
		creds = append(creds, c)
	}
	sort.Slice(creds, func(i, j int) bool {
		if creds[i].key != creds[j].key {
			return creds[i].key < creds[j].key
		}
		return creds[i].slot < creds[j].slot
	})

	var totalOut float64
	for _, c := range creds {
		tot := ExternalMeterTotal{SecretKey: c.key, SecretSlot: c.slot, Sources: byCred[c]}
		sort.Strings(tot.Sources)
		hours := map[time.Time]*ExternalMeterHour{}
		hour := func(h time.Time) *ExternalMeterHour {
			eh, ok := hours[h]
			if !ok {
				eh = &ExternalMeterHour{Hour: h, SecretKey: c.key, SecretSlot: c.slot}
				hours[h] = eh
			}
			return eh
		}
		if cov, ok := pickCoverage(coverage, c.key, c.slot); ok {
			tot.MeterSource = cov.Source
			for _, st := range steps {
				if st.SecretKey != c.key || st.SecretSlot != c.slot || st.Source != cov.Source || st.WindowKind != cov.WindowKind {
					continue
				}
				eh := hour(st.Period.UTC())
				if r := stepReal(st); r != nil {
					eh.RealUSD = addUSD(eh.RealUSD, *r)
				}
			}
		}
		for k, v := range lOut {
			if k.key == c.key && k.slot == c.slot && v > 0 {
				hour(k.hour).ExternalListUSD = v
			}
		}
		for h, eh := range hours {
			eh.CoveyListUSD = lCov[credHour{h, c.key, c.slot}]
			if eh.RealUSD == nil {
				tot.HoursWithoutReading++
				tot.UnmeteredListUSD += eh.ExternalListUSD
				continue
			}
			tot.HoursWithReading++
			share := 0.0
			if eh.ExternalListUSD > 0 {
				share = *eh.RealUSD * eh.ExternalListUSD / (eh.ExternalListUSD + eh.CoveyListUSD)
			}
			rest := *eh.RealUSD - share
			eh.ExternalRealUSD, eh.CoveyRealUSD = &share, &rest
			tot.RealUSD += *eh.RealUSD
			tot.ExternalRealUSD += share
		}
		if tot.RealUSD > 0 {
			sh := tot.ExternalRealUSD / tot.RealUSD
			tot.Share = &sh
		}
		for _, eh := range hours {
			out.Hours = append(out.Hours, *eh)
		}
		out.Credentials = append(out.Credentials, tot)
		out.RealUSD += tot.RealUSD
		totalOut += tot.ExternalRealUSD
		out.UnmeteredListUSD += tot.UnmeteredListUSD
	}
	out.ExternalRealUSD = totalOut
	if out.RealUSD > 0 {
		sh := out.ExternalRealUSD / out.RealUSD
		out.Share = &sh
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
