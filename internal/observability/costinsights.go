package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Cost insights (migrations 0142, 0144): what a run consumed per model, and
// what the provider said about the subscription meanwhile.
//
// Claude Code reports its usage per model only in its `result` lines. Such a
// line is filed as a runtime recording event like every other one, and
// recordings are pruned after their retention — so the split per model is
// copied into model_usage at the moment the event is filed.
//
// EVERY result line carries the session's RUNNING totals (modelUsage,
// total_cost_usd), restored across --resume: a resumed session's line includes
// everything the session did before, in this task and in earlier ones. What
// the line itself added is model_usage_since_last_result (CLI 2.1.x), and that
// is what a row holds (basis since_last). A CLI without the field gets the
// running total minus what the session's earlier rows already hold (basis
// derived). Either way a row is one invocation's own share, so plain SUMs over
// any window are right and every row sits on its own task and time.
//
// One share has no line of its own: a process that dies without a result line
// (killed, timed out, its sandbox gone) still spent money, and the session's
// running total keeps it — model_usage_since_last_result of the next line does
// not. Where a line's running total grew by more than the line's own share
// since the session's previous line, the difference is filed as a row of its
// own (basis gap, migration 0147) and booked as a cost entry of its own, see
// modelUsageGapInsert.

// CredentialRef names the credential a run went out on: its place in the
// runtime (RuntimeID/Ord — a Reorder moves values between places), the secret
// value behind it (SecretKey/SecretSlot — that does not move), and its kind
// (api_key | subscription), which says what money means on it. The zero value
// is "not attributable", as for every run from before the runtimes.
type CredentialRef struct {
	RuntimeID  uuid.UUID
	Ord        int
	SecretKey  string
	SecretSlot int
	Kind       string
}

// columns returns the nullable column values: runtime_id, credential_ord,
// secret_key, secret_slot.
func (c CredentialRef) columns() (rt *uuid.UUID, ord *int, key *string, slot *int) {
	if c.RuntimeID != uuid.Nil {
		rt, ord = &c.RuntimeID, &c.Ord
	}
	if c.SecretKey != "" {
		key, slot = &c.SecretKey, &c.SecretSlot
	}
	return
}

// jsonInt reads a whole number out of the model entry (alias m), 0 for
// anything that is not a JSON number. A line is the engine's to shape: a value
// written as 1.2e3 or as a string must not make the insert fail. Numbers go
// through numeric first, so 1.2e3 still lands as 1200.
func jsonInt(field string) string { return jsonIntOf("m.value", field) }

// jsonIntOf is jsonInt for the entry the JSON expression v names.
func jsonIntOf(v, field string) string {
	return "CASE WHEN jsonb_typeof(" + v + "->'" + field + "') = 'number' THEN (" + v + "->>'" + field + "')::numeric::bigint ELSE 0 END"
}

// cacheWrite5m is the entry's 5-minute cache write — or, where the entry has
// no split by lifetime (an older CLI writes only cacheCreationInputTokens),
// the plain sum. The daemon books the plain sum in that case (cacheCreation),
// and a row without it would hold fewer tokens than the booking and price
// them at nothing. The plain sum goes to 5 minutes, the API's default
// lifetime; the 1-hour write is opt-in and came with the split.
func cacheWrite5m(v string) string {
	five, hour := jsonIntOf(v, "cacheCreationEphemeral5mInputTokens"), jsonIntOf(v, "cacheCreationEphemeral1hInputTokens")
	return "CASE WHEN (" + five + ") + (" + hour + ") = 0 THEN " + jsonIntOf(v, "cacheCreationInputTokens") + " ELSE " + five + " END"
}

// modelUsageInsert files the model_usage rows of one recording event ($1).
// $2..$6 are the credential: runtime_id, credential_ord, secret_key,
// secret_slot, kind.
//
// The derived fallback subtracts what the session's EARLIER rows of the same
// model hold: their sum is the running total before this line, whichever basis
// they were filed with. A model that added nothing leaves no row.
var modelUsageInsert = `
WITH e AS (
	SELECT id, org_id, agent_id, task_id, payload, created_at,
		COALESCE(payload->>'session_id', '') AS session_id,
		COALESCE(jsonb_typeof(payload->'model_usage_since_last_result') = 'object', false) AS since_last
	FROM recording_events
	WHERE id = $1 AND payload->>'type' = 'result'),
v AS (
	SELECT e.*, m.key AS model,
		` + jsonInt("inputTokens") + ` AS input_tokens,
		` + jsonInt("outputTokens") + ` AS output_tokens,
		` + jsonInt("thinkingTokens") + ` AS thinking_tokens,
		` + jsonInt("cacheReadInputTokens") + ` AS cache_read_tokens,
		` + cacheWrite5m("m.value") + ` AS cache_write_5m_tokens,
		` + jsonInt("cacheCreationEphemeral1hInputTokens") + ` AS cache_write_1h_tokens,
		` + jsonInt("webSearchRequests") + ` AS web_search_requests,
		CASE WHEN jsonb_typeof(m.value->'costUSD') = 'number' THEN (m.value->>'costUSD')::numeric ELSE 0 END AS engine_usd,
		CASE WHEN jsonb_typeof(m.value->'costBasis') = 'string' THEN m.value->>'costBasis' ELSE '' END AS cost_basis
	FROM e
	CROSS JOIN LATERAL (SELECT CASE WHEN e.since_last THEN e.payload->'model_usage_since_last_result'
		ELSE e.payload->'modelUsage' END AS usage) u
	CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(u.usage) = 'object' THEN u.usage ELSE '{}'::jsonb END) AS m(key, value)
	WHERE jsonb_typeof(m.value) = 'object'),
d AS (
	SELECT v.org_id, v.agent_id, v.task_id, v.id, v.session_id, v.model, v.payload, v.created_at, v.since_last, v.cost_basis,
		v.input_tokens - p.input_tokens AS input_tokens,
		v.output_tokens - p.output_tokens AS output_tokens,
		v.thinking_tokens - p.thinking_tokens AS thinking_tokens,
		v.cache_read_tokens - p.cache_read_tokens AS cache_read_tokens,
		v.cache_write_5m_tokens - p.cache_write_5m_tokens AS cache_write_5m_tokens,
		v.cache_write_1h_tokens - p.cache_write_1h_tokens AS cache_write_1h_tokens,
		v.web_search_requests - p.web_search_requests AS web_search_requests,
		v.engine_usd - p.engine_usd AS engine_usd
	FROM v
	CROSS JOIN LATERAL (
		SELECT COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens,
			COALESCE(SUM(thinking_tokens),0) AS thinking_tokens, COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens,
			COALESCE(SUM(cache_write_5m_tokens),0) AS cache_write_5m_tokens,
			COALESCE(SUM(cache_write_1h_tokens),0) AS cache_write_1h_tokens,
			COALESCE(SUM(web_search_requests),0) AS web_search_requests, COALESCE(SUM(engine_usd),0) AS engine_usd
		FROM model_usage p
		WHERE NOT v.since_last AND v.session_id <> ''
		  AND p.session_id = v.session_id AND p.model = v.model AND p.event_id < v.id) p)
INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
	input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
	cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
	engine_usd, cost_basis, created_at, basis, started_at,
	runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
	service_tier, speed, inference_geo, line_uuid)
SELECT d.org_id, d.agent_id, d.task_id, d.id, d.session_id, d.model,
	d.input_tokens, d.output_tokens, d.thinking_tokens, d.cache_read_tokens,
	d.cache_write_5m_tokens, d.cache_write_1h_tokens, d.web_search_requests,
	d.engine_usd, d.cost_basis, d.created_at,
	CASE WHEN d.since_last THEN 'since_last' ELSE 'derived' END,
	CASE WHEN jsonb_typeof(d.payload->'duration_ms') = 'number'
		THEN d.created_at - ((d.payload->>'duration_ms')::numeric::float8 * interval '1 millisecond') END,
	$2::uuid, $3::smallint, $4::text, $5::smallint, $6::text,
	COALESCE(d.payload->'usage'->>'service_tier', ''),
	COALESCE(d.payload->'usage'->>'speed', ''),
	COALESCE(d.payload->'usage'->>'inference_geo', ''),
	CASE WHEN jsonb_typeof(d.payload->'uuid') = 'string' THEN d.payload->>'uuid' END
FROM d
WHERE d.input_tokens <> 0 OR d.output_tokens <> 0 OR d.thinking_tokens <> 0 OR d.cache_read_tokens <> 0
   OR d.cache_write_5m_tokens <> 0 OR d.cache_write_1h_tokens <> 0 OR d.web_search_requests <> 0 OR d.engine_usd <> 0
ON CONFLICT DO NOTHING`

// figures are the figures model_usage keeps per model, each with the
// expression that reads it out of a per-model entry — the JSON expression v.
// Guarded like jsonInt: what is not a JSON number is 0; the 5-minute cache
// write falls back to the plain sum like cacheWrite5m.
var figures = []struct {
	name string
	read func(v string) string
}{
	{"input_tokens", func(v string) string { return jsonIntOf(v, "inputTokens") }},
	{"output_tokens", func(v string) string { return jsonIntOf(v, "outputTokens") }},
	{"thinking_tokens", func(v string) string { return jsonIntOf(v, "thinkingTokens") }},
	{"cache_read_tokens", func(v string) string { return jsonIntOf(v, "cacheReadInputTokens") }},
	{"cache_write_5m_tokens", cacheWrite5m},
	{"cache_write_1h_tokens", func(v string) string { return jsonIntOf(v, "cacheCreationEphemeral1hInputTokens") }},
	{"web_search_requests", func(v string) string { return jsonIntOf(v, "webSearchRequests") }},
	{"engine_usd", func(v string) string {
		return "CASE WHEN jsonb_typeof(" + v + "->'costUSD') = 'number' THEN (" + v + "->>'costUSD')::numeric ELSE 0 END"
	}},
}

// usageFigures reads one per-model entry — the JSON expression v — into the
// figures model_usage keeps, each column named with the prefix p.
func usageFigures(v, p string) string {
	parts := make([]string, len(figures))
	for i, c := range figures {
		parts[i] = c.read(v) + " AS " + p + c.name
	}
	return strings.Join(parts, ",\n\t\t")
}

// usageEach applies x to the name of each figure, named with the prefix p.
func usageEach(x func(name string) string, p string) string {
	parts := make([]string, len(figures))
	for i, c := range figures {
		parts[i] = x(c.name) + " AS " + p + c.name
	}
	return strings.Join(parts, ",\n\t\t")
}

// modelUsageGapInsert files the gap a result line ($1) reveals, per model:
// what the session's running total (modelUsage) grew by since the session's
// previous lines, minus what was booked for those lines and what this line
// says it added itself (model_usage_since_last_result). Whatever is left was
// spent between the lines without a booking — a process of the session that
// ended without a result line, whose cost Claude Code kept in the running
// total. $2..$6 are the credential, as for modelUsageInsert.
//
// The running total before this line is, per model and per figure, the
// largest one the session's earlier FILED lines report (any basis, a gap row
// included) — read back from the recording, not from the rows. The largest,
// not the latest: a running total cannot shrink, so the largest is the
// session's latest state whatever order the lines arrived in. A line without
// a uuid that is sent again after later lines is filed again; as the latest
// line it would put the running total back and book the lines after it a
// second time. That rests on the totals only growing within a session.
// Should a session ever come back with smaller totals (a CLI that does not
// restore them on resume, a forked session), every later gap stays hidden
// until its total climbs past the old largest one: it errs towards booking
// too little, never twice.
//
// A line whose filing failed (an error in the rows, the gap or its booking —
// one transaction) left its recording and nothing else. Its share is
// subtracted only where it was booked all the same; otherwise it stays in the
// gap. Booked means one of two things:
//
//   - an invocation entry of the line's task and of the session, booked
//     after the line and before the task's next run started (a lifecycle
//     record "working" — the orchestrator writes one at the start of every
//     run): the daemon reported that run's own spend, the line's included;
//   - the line belongs to this line's run (the same task, no run started
//     since): that run's report is still to come, and holds the line.
//
// A run killed (kill switch, budget pause, sandbox gone) never reports, nor
// does one whose BookRun failed; its end files its failed lines once more
// (FileFailedLines) and BookUnreported books their rows, so for this gap they
// are filed lines like any other. What stays here is a failed line of a run
// booked from its rows (from_deltas), and one the run's end could not file
// (that filing failed too, or the control plane went down before the run's
// end): the share of such a line is the gap's.
//
// Those lines are looked for in the recording of the session's tasks only —
// the tasks its filed lines belong to, this line's, and the tasks this line's
// task continues (a turn-limit continuation carries the session into a child
// task, origin "continuation:<parent>"; a child in the middle of the chain
// often holds two lines, and one failure leaves it without a filed one) —
// after the filed line with the largest running total and before this one.
// Bounded by the largest total, not the latest filed line: a late copy is
// filed after the lines that followed its original. A session outlives a
// run: a blocked task resumes it days later. Over the agent's or the
// install's recording that window holds every other run in between; over the
// session's tasks it holds their own transcript. The per-task subquery
// compares (task_id, id) as a row, which only idx_recording_task can answer,
// and its ORDER BY keeps the planner from folding it into one scan of the
// whole id range. Copies of one failed line count once (the same running
// total), and a failed line whose total is not above the bound's is an old
// line arriving late: its share is already inside the bound's total.
//
// Measured from line to line, not against the sum of the session's rows: a
// session whose earlier lines were never filed (before 0142, or pruned before
// its backfill) would otherwise have its whole history booked again as a gap.
// Without an earlier filed line — the session's first line here, a session
// none of whose lines was filed yet, or earlier recordings already pruned —
// nothing is filed: a gap is only booked where both ends are known.
//
// It books nothing twice, but for the case it cannot see (below):
//
//   - After a process WITH a result line, the running total grew by that
//     line's share and this one's: no gap, where the earlier line was filed
//     and where it failed but was booked.
//   - After one WITHOUT, the growth holds its spend once, and this line is the
//     previous one for every later line: none of them finds it again.
//   - The turn-limit handover resumes the same session and writes a line of
//     its own; a handover killed before its line is found by the session's
//     next line, if the session is resumed again.
//   - Covey's sub-agent runs are sessions of their own; Claude Code's own
//     sub-agents are inside the line's share.
//   - A line filed twice (a retried event) finds its first copy: the growth
//     is 0, the difference minus its share. The same for a copy arriving after
//     later lines, and for every line after that copy.
//
// What it cannot see: a failed line before the bound whose total is larger
// than every filed line's (a failure and an out-of-order arrival together:
// the next line's gap holds its share again, booked twice where its run
// reported). A run killed after a failed line of its own was followed by a
// filed one is no longer among them: that line's gap counts the failed line
// as its run's, still to come, and the run's end files it (FileFailedLines).
//
// Only a difference above a millionth of a dollar is filed, and only upwards:
// a negative difference is never booked. Tokens follow the money, each floored
// at 0. Lines without a session, without the per-line field (their derived
// rows already are the running total's difference) or without a running total
// file nothing here.
var modelUsageGapInsert = `
WITH RECURSIVE e AS (
	SELECT id, org_id, agent_id, task_id, payload, created_at, payload->>'session_id' AS session_id
	FROM recording_events
	WHERE id = $1 AND payload->>'type' = 'result'
	  AND jsonb_typeof(payload->'session_id') = 'string' AND payload->>'session_id' <> ''
	  AND jsonb_typeof(payload->'model_usage_since_last_result') = 'object'
	  AND jsonb_typeof(payload->'modelUsage') = 'object'),
filed AS (
	SELECT DISTINCT p.event_id AS id, p.task_id
	FROM model_usage p, e
	WHERE p.session_id = e.session_id AND p.session_id <> '' AND p.event_id < e.id),
filed_lines AS (
	SELECT r.id, r.payload->'modelUsage' AS usage, ` + lineTotal("r.payload") + ` AS total
	FROM recording_events r
	WHERE r.id IN (SELECT id FROM filed)),
bound AS (
	SELECT id, total FROM filed_lines ORDER BY total DESC, id LIMIT 1),
chain(task_id) AS (
	SELECT task_id FROM e WHERE task_id IS NOT NULL
	UNION
	SELECT t.parent_task_id
	FROM chain JOIN backlog_tasks t ON t.id = chain.task_id
	WHERE t.origin LIKE 'continuation:%' AND t.parent_task_id IS NOT NULL),
tasks AS (
	SELECT task_id FROM filed WHERE task_id IS NOT NULL
	UNION
	SELECT task_id FROM chain),
w AS (
	SELECT x.id, x.task_id, x.kind, x.payload, x.created_at
	FROM e, bound, tasks
	CROSS JOIN LATERAL (
		SELECT r.id, r.task_id, r.kind, r.payload, r.created_at
		FROM recording_events r
		WHERE (r.task_id, r.id) > (tasks.task_id, bound.id)
		  AND (r.task_id, r.id) < (tasks.task_id, e.id)
		  AND r.kind IN ('` + KindRuntime + `', '` + KindLifecycle + `')
		ORDER BY r.task_id, r.id) AS x
	WHERE (x.kind = '` + KindRuntime + `' AND x.payload->>'type' = 'result' AND x.payload->>'session_id' = e.session_id
	       AND x.id NOT IN (SELECT id FROM filed))
	   OR (x.kind = '` + KindLifecycle + `' AND x.payload->>'status' = 'working')),
failed AS (
	SELECT w.id, w.task_id, w.payload, w.created_at, ` + lineTotal("w.payload") + ` AS total
	FROM w WHERE w.kind = '` + KindRuntime + `'),
booked AS (
	SELECT DISTINCT ON (fl.total) fl.payload
	FROM e, bound, failed fl
	LEFT JOIN LATERAL (
		SELECT nr.created_at FROM w nr
		WHERE nr.kind = '` + KindLifecycle + `' AND nr.task_id = fl.task_id AND nr.id > fl.id
		ORDER BY nr.id LIMIT 1) AS next_run ON true
	WHERE fl.total > bound.total
	  AND ((next_run.created_at IS NULL AND fl.task_id = e.task_id)
	    OR EXISTS (
		SELECT 1 FROM cost_entries c
		WHERE c.agent_id = e.agent_id AND c.created_at >= fl.created_at
		  AND (next_run.created_at IS NULL OR c.created_at < next_run.created_at)
		  AND c.task_id = fl.task_id AND c.session_id = e.session_id AND c.basis = '` + BasisInvocation + `'))
	ORDER BY fl.total, fl.id),
f AS (
	SELECT m.key AS model,
		` + usageFigures("m.value", "") + `
	FROM filed_lines
	CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(filed_lines.usage) = 'object' THEN filed_lines.usage ELSE '{}'::jsonb END) AS m(key, value)
	WHERE jsonb_typeof(m.value) = 'object'),
prev AS (
	SELECT f.model,
		` + usageEach(func(n string) string { return "max(f." + n + ")" }, "p_") + `
	FROM f
	GROUP BY f.model),
bs AS (
	SELECT m.key AS model,
		` + usageFigures("m.value", "") + `
	FROM booked
	CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(booked.payload->'model_usage_since_last_result') = 'object'
		THEN booked.payload->'model_usage_since_last_result' ELSE '{}'::jsonb END) AS m(key, value)
	WHERE jsonb_typeof(m.value) = 'object'),
shares AS (
	SELECT bs.model,
		` + usageEach(func(n string) string { return "sum(bs." + n + ")" }, "k_") + `
	FROM bs
	GROUP BY bs.model),
g AS (
	SELECT e.id, e.org_id, e.agent_id, e.task_id, e.created_at, e.session_id, m.key AS model,
		` + usageFigures("m.value", "c_") + `,
		` + usageEach(func(n string) string { return "COALESCE(prev.p_" + n + ", 0)" }, "p_") + `,
		` + usageEach(func(n string) string { return "COALESCE(shares.k_" + n + ", 0)" }, "k_") + `,
		` + usageFigures("(e.payload->'model_usage_since_last_result'->m.key)", "s_") + `
	FROM e
	CROSS JOIN LATERAL jsonb_each(e.payload->'modelUsage') AS m(key, value)
	LEFT JOIN prev ON prev.model = m.key
	LEFT JOIN shares ON shares.model = m.key
	WHERE jsonb_typeof(m.value) = 'object' AND EXISTS (SELECT 1 FROM bound))
INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
	input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
	cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
	engine_usd, created_at, basis,
	runtime_id, credential_ord, secret_key, secret_slot, credential_kind)
SELECT g.org_id, g.agent_id, g.task_id, g.id, g.session_id, g.model,
	GREATEST(g.c_input_tokens - g.p_input_tokens - g.k_input_tokens - g.s_input_tokens, 0),
	GREATEST(g.c_output_tokens - g.p_output_tokens - g.k_output_tokens - g.s_output_tokens, 0),
	GREATEST(g.c_thinking_tokens - g.p_thinking_tokens - g.k_thinking_tokens - g.s_thinking_tokens, 0),
	GREATEST(g.c_cache_read_tokens - g.p_cache_read_tokens - g.k_cache_read_tokens - g.s_cache_read_tokens, 0),
	GREATEST(g.c_cache_write_5m_tokens - g.p_cache_write_5m_tokens - g.k_cache_write_5m_tokens - g.s_cache_write_5m_tokens, 0),
	GREATEST(g.c_cache_write_1h_tokens - g.p_cache_write_1h_tokens - g.k_cache_write_1h_tokens - g.s_cache_write_1h_tokens, 0),
	GREATEST(g.c_web_search_requests - g.p_web_search_requests - g.k_web_search_requests - g.s_web_search_requests, 0),
	g.c_engine_usd - g.p_engine_usd - g.k_engine_usd - g.s_engine_usd, g.created_at, '` + BasisGap + `',
	$2::uuid, $3::smallint, $4::text, $5::smallint, $6::text
FROM g
WHERE g.c_engine_usd - g.p_engine_usd - g.k_engine_usd - g.s_engine_usd > 0.000001
ON CONFLICT DO NOTHING`

// lineTotal is a result line's running total over all of its models — the
// expression r is the line's payload, its modelUsage what the gap compares.
func lineTotal(r string) string {
	return "(SELECT COALESCE(SUM(CASE WHEN jsonb_typeof(t.value->'costUSD') = 'number' THEN (t.value->>'costUSD')::numeric END), 0)\n\t\t" +
		"FROM jsonb_each(CASE WHEN jsonb_typeof(" + r + "->'modelUsage') = 'object' THEN " + r + "->'modelUsage' ELSE '{}'::jsonb END) AS t)"
}

// gapBooking books the gap rows of one recording event ($1) as one cost entry
// of basis gap and links them to it, so that what the budget, the capacity
// steering and the booked side of the cost page read holds the dead process's
// spend too — beside the runs, never inside one: a run's own entry is what
// its daemon reported, and the gap is exactly what no daemon reported. The
// entry's model is the gap's costliest one, its time the line's.
const gapBooking = `
WITH g AS (
	SELECT id FROM model_usage WHERE event_id = $1 AND basis = '` + BasisGap + `' AND cost_entry_id IS NULL
	FOR UPDATE),
s AS (
	SELECT mu.agent_id, mu.task_id, mu.session_id, mu.runtime_id, mu.credential_ord, mu.secret_key, mu.secret_slot,
		SUM(mu.engine_usd) AS usd, SUM(mu.input_tokens) AS input_tokens, SUM(mu.output_tokens) AS output_tokens,
		SUM(mu.cache_read_tokens) AS cache_read_tokens,
		SUM(mu.cache_write_5m_tokens + mu.cache_write_1h_tokens) AS cache_creation_tokens,
		(array_agg(mu.model ORDER BY mu.engine_usd DESC))[1] AS model, MAX(mu.created_at) AS created_at
	FROM model_usage mu WHERE mu.id IN (SELECT id FROM g)
	GROUP BY 1, 2, 3, 4, 5, 6, 7),
ins AS (
	INSERT INTO cost_entries (agent_id, task_id, usd, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		model, runtime_id, credential_ord, secret_key, secret_slot, basis, session_id, created_at)
	SELECT agent_id, task_id, usd, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		model, runtime_id, credential_ord, secret_key, secret_slot, '` + BasisGap + `', session_id, created_at
	FROM s
	RETURNING id)
UPDATE model_usage mu SET cost_entry_id = (SELECT id FROM ins)
WHERE mu.id IN (SELECT id FROM g)`

// carriesModelUsage is the cheap pre-check: most runtime lines are transcript.
func carriesModelUsage(raw []byte) bool {
	return (bytes.Contains(raw, []byte(`"modelUsage"`)) || bytes.Contains(raw, []byte(`"model_usage_since_last_result"`))) &&
		bytes.Contains(raw, []byte(`"result"`))
}

// RecordRuntime files a runtime event; a Claude Code `result` event also
// leaves one model_usage row per model, booked against cred.
//
// Two statements on purpose. The recording is the record, the per-model rows
// are derived from it: a line the engine shaped unexpectedly must never cost
// the recording its line, which is what one statement did when a cast failed.
// The second statement's error is returned (wrapped) for the caller to log;
// the line is filed either way.
//
// The per-model rows, the gap the line reveals and the gap's booking are one
// transaction: a gap row without its booking would be spend the booked side
// never sees.
func (s *Store) RecordRuntime(ctx context.Context, orgID, agentID uuid.UUID, taskID *uuid.UUID, cred CredentialRef, payload json.RawMessage) error {
	if !carriesModelUsage(payload) || !json.Valid(payload) {
		return s.Record(ctx, orgID, agentID, taskID, KindRuntime, payload)
	}
	var id int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO recording_events (org_id, agent_id, task_id, kind, payload)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, orgID, agentID, taskID, KindRuntime, []byte(payload)).Scan(&id); err != nil {
		return err
	}
	if err := s.fileModelUsage(ctx, id, cred); err != nil {
		return fmt.Errorf("model usage of recording event %d: %w", id, err)
	}
	return nil
}

// fileModelUsage files the per-model rows of recording event id, then the gap
// it reveals, and books the gap.
func (s *Store) fileModelUsage(ctx context.Context, id int64, cred CredentialRef) error {
	rt, ord, key, slot := cred.columns()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, modelUsageInsert, id, rt, ord, key, slot, cred.Kind); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, modelUsageGapInsert, id, rt, ord, key, slot, cred.Kind)
	if err != nil {
		return fmt.Errorf("gap: %w", err)
	}
	if tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, gapBooking, id); err != nil {
			return fmt.Errorf("booking the gap: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// failedLines selects the result lines of a task's latest run ($1 the task,
// $2 its agent) whose filing failed and that only filing them now can book,
// oldest first — see FileFailedLines. The run starts at the task's latest
// lifecycle record "working"; the orchestrator writes one per run.
//
//   - A line that left no model_usage row (no row of its event, any basis).
//   - Of a session whose run did not report: no entry of the task since the
//     run's start, of that session or of none (a daemon from before the
//     field names no session), with a basis other than gap or unreported. An
//     invocation report holds the line already; a from_deltas or legacy one
//     leaves its share to the gap, as modelUsageGapInsert says.
//   - Above every earlier filed line of its session: a line whose running
//     total is not is a copy or an old line arriving late, its share inside
//     that filed line's total (the gap's bound, the same rule).
//   - Without model_usage_since_last_result, only where no later line of the
//     session was filed: a later derived row is the running total minus the
//     earlier rows, the failed line's share included.
//
// Copies of one line count once, the first: the same session and running
// total, as the gap counts them (a copy carries the line's uuid, and a line
// without one its total).
var failedLines = `
WITH run AS (
	SELECT r.id, r.created_at FROM recording_events r
	WHERE r.task_id = $1 AND r.kind = '` + KindLifecycle + `' AND r.payload->>'status' = 'working'
	ORDER BY r.id DESC LIMIT 1),
l AS (
	SELECT r.id, COALESCE(r.payload->>'session_id', '') AS session_id, run.created_at AS started,
		jsonb_typeof(r.payload->'model_usage_since_last_result') = 'object' AS since_last,
		` + lineTotal("r.payload") + ` AS total
	FROM run, recording_events r
	WHERE r.task_id = $1 AND r.id > run.id AND r.agent_id = $2
	  AND r.kind = '` + KindRuntime + `' AND r.payload->>'type' = 'result'
	  AND (jsonb_typeof(r.payload->'modelUsage') = 'object' OR jsonb_typeof(r.payload->'model_usage_since_last_result') = 'object')
	  AND NOT EXISTS (SELECT 1 FROM model_usage mu WHERE mu.event_id = r.id)),
u AS (
	SELECT DISTINCT ON (l.session_id, l.total) l.*
	FROM l
	ORDER BY l.session_id, l.total, l.id)
SELECT u.id FROM u
WHERE NOT EXISTS (
	SELECT 1 FROM cost_entries c
	WHERE c.task_id = $1 AND c.agent_id = $2 AND c.created_at >= u.started
	  AND c.session_id IN (u.session_id, '')
	  AND c.basis NOT IN ('` + BasisGap + `', '` + BasisUnreported + `'))
  AND NOT EXISTS (
	SELECT 1 FROM model_usage p JOIN recording_events f ON f.id = p.event_id
	WHERE u.session_id <> '' AND p.session_id = u.session_id AND p.event_id < u.id
	  AND ` + lineTotal("f.payload") + ` >= u.total)
  AND (u.since_last OR NOT EXISTS (
	SELECT 1 FROM model_usage p
	WHERE u.session_id <> '' AND p.session_id = u.session_id AND p.event_id > u.id))
ORDER BY u.id`

// FileFailedLines files once more the result lines of the task's latest run
// whose filing failed, when nothing else will book them (failedLines says
// which): at the end of a run that never reported — killed, paused at its
// budget, its sandbox gone, or its booking failed — before BookUnreported,
// which then books their rows like any other line's. Each line goes through
// fileModelUsage on its own, oldest first, against cred, the run's
// credential: its rows, the gap it reveals and the gap's booking.
//
// A line filed this way is a filed line for every gap after it, so a line
// of the session resumed later does not count it among the failed ones: it
// is booked once. Left unfiled, nothing would book it where no later line of
// the session comes, and a later line of the same run would subtract it as
// its run's, the report still to come.
//
// It returns how many lines it filed. The first failure ends it, the lines
// before it staying filed: a line filed after a failed one would subtract
// that one as its run's, the report still to come, and lose it. Left
// unfiled, both stay with the gap of the session's next line.
func (s *Store) FileFailedLines(ctx context.Context, agentID, taskID uuid.UUID, cred CredentialRef) (int, error) {
	rows, err := s.pool.Query(ctx, failedLines, taskID, agentID)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	for i, id := range ids {
		if err := s.fileModelUsage(ctx, id, cred); err != nil {
			return i, fmt.Errorf("model usage of recording event %d: %w", id, err)
		}
	}
	return len(ids), nil
}

// ModelTokens is what one model consumed, with the cache writes split by TTL.
type ModelTokens struct {
	Input        int64 `json:"input_tokens"`
	Output       int64 `json:"output_tokens"`
	Thinking     int64 `json:"thinking_tokens"`
	CacheRead    int64 `json:"cache_read_tokens"`
	CacheWrite5m int64 `json:"cache_write_5m_tokens"`
	CacheWrite1h int64 `json:"cache_write_1h_tokens"`
	WebSearches  int64 `json:"web_search_requests"`
}

// ModelUsageRow is one agent × model × credential kind over the window.
type ModelUsageRow struct {
	AgentID   uuid.UUID `json:"agent_id"`
	AgentSlug string    `json:"agent_slug"`
	AgentName string    `json:"agent_name"`
	Model     string    `json:"model"`
	// CredentialKind is what the runs went out on (api_key | subscription),
	// empty where the run was not attributable. It says what money means here:
	// on a subscription seat EngineUSD and ListUSD are what the work would have
	// cost through the API, not what was paid.
	CredentialKind string `json:"credential_kind"`
	// Runs counts tasks (a line without a task counts on its own).
	Runs int64 `json:"runs"`
	ModelTokens
	// EngineUSD is what the engine itself priced (Claude Code's costUSD).
	EngineUSD float64 `json:"engine_usd"`
	// ListUSD is the same tokens priced with covey's own list (nil: model unknown).
	ListUSD *float64 `json:"list_usd"`
}

// ModelUsageReport is the per-model view of the organisation's runs.
type ModelUsageReport struct {
	Rows []ModelUsageRow `json:"rows"`
	// RunsWithout counts runs booked in cost_entries that have no per-model
	// record (cut off before Claude Code wrote its result, an engine without
	// result lines, or older than the recordings that were still there when
	// 0142 ran).
	RunsWithout int64   `json:"runs_without_breakdown"`
	USDWithout  float64 `json:"usd_without_breakdown"`
}

// CredentialFilter narrows a cost read to one secret value. An empty
// SecretKey means every credential.
type CredentialFilter struct {
	SecretKey  string
	SecretSlot int
}

// ModelUsage sums model_usage per agent, model and credential kind since
// `since`. The rows are deltas, so this is a plain SUM over an index range.
func (s *Store) ModelUsage(ctx context.Context, orgID uuid.UUID, since time.Time, f CredentialFilter) (ModelUsageReport, error) {
	rep := ModelUsageReport{Rows: []ModelUsageRow{}}
	rows, err := s.pool.Query(ctx, `SELECT mu.agent_id, a.slug, a.display_name, mu.model, mu.credential_kind,
		COUNT(DISTINCT COALESCE(mu.task_id::text, mu.event_id::text)),
		SUM(mu.input_tokens), SUM(mu.output_tokens), SUM(mu.thinking_tokens), SUM(mu.cache_read_tokens),
		SUM(mu.cache_write_5m_tokens), SUM(mu.cache_write_1h_tokens), SUM(mu.web_search_requests),
		SUM(mu.engine_usd)::float8
		FROM model_usage mu JOIN agents a ON a.id = mu.agent_id
		WHERE mu.org_id = $1 AND mu.created_at >= $2
		  AND ($3::text = '' OR (mu.secret_key = $3 AND mu.secret_slot = $4::smallint))
		GROUP BY 1,2,3,4,5 ORDER BY SUM(mu.engine_usd) DESC`, orgID, since, f.SecretKey, f.SecretSlot)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ModelUsageRow
		if err := rows.Scan(&r.AgentID, &r.AgentSlug, &r.AgentName, &r.Model, &r.CredentialKind, &r.Runs,
			&r.Input, &r.Output, &r.Thinking, &r.CacheRead, &r.CacheWrite5m, &r.CacheWrite1h, &r.WebSearches,
			&r.EngineUSD); err != nil {
			return rep, err
		}
		if p, ok := ListPrice(r.Model); ok {
			v := p.Cost(r.ModelTokens)
			r.ListUSD = &v
		}
		rep.Rows = append(rep.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	// A booking without a breakdown is one no line was linked to (0144).
	err = s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT ce.task_id), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		WHERE a.org_id = $1 AND ce.created_at >= $2 AND ce.task_id IS NOT NULL
		  AND ($3::text = '' OR (ce.secret_key = $3 AND ce.secret_slot = $4::smallint))
		  AND NOT EXISTS (SELECT 1 FROM model_usage mu WHERE mu.cost_entry_id = ce.id)`,
		orgID, since, f.SecretKey, f.SecretSlot).Scan(&rep.RunsWithout, &rep.USDWithout)
	return rep, err
}

// The subscription meter (migrations 0142, 0145): the provider's own reading
// of a credential's utilisation, from two sources covey can tell apart.
//
//   - usage_cmd: `claude -p /usage`, asked by covey once per credential and
//     cached. Prose, whole percents, a rate limit of its own; a rate-limited
//     answer is the engine's cached one (stale), up to an hour old.
//   - inband: Claude Code's rate_limit_event in the run's own stream — every
//     window the provider's headers name, its utilisation and its reset as
//     an epoch. Free, but only there while a run runs.
//
// Both are keyed on the secret value (a Reorder moves places, not values), and
// both read an ACCOUNT-wide meter: whatever else uses the account — a person
// in claude.ai, another machine — moves it too.

// Reading sources and the window the plan's price is spread over.
const (
	SourceUsageCmd = "usage_cmd"
	SourceInband   = "inband"
	SourceManual   = "manual"
	// WindowSevenDay is the weekly all-models window. Other weekly windows
	// (seven_day_opus, …) are sub-limits of the same allowance: their points
	// are shown, never priced — that would count the same work twice.
	WindowSevenDay = "seven_day"
)

// MeterReading is one reading to file.
type MeterReading struct {
	Credential CredentialRef
	Source     string
	WindowKind string
	Supported  bool
	// WindowPercent/WeekPercent: negative means "not reported".
	WindowPercent, WeekPercent float64
	// WindowResets/WeekResets: the reset as the engine printed it.
	WindowResets, WeekResets string
	// ResetsAt is the reset of the reading's window as an instant (zero:
	// unknown).
	ResetsAt time.Time
	// Status is the provider's (allowed, allowed_warning, rejected), Reason
	// covey's code for a reading without a fresh figure.
	Status, Reason string
	Stale          bool
	// FetchedAt is when the figure was read from the provider; zero means
	// "when it arrived", which is what received_at says anyway.
	FetchedAt time.Time
}

// secret returns the nullable secret columns.
func (c CredentialRef) secret() (key *string, slot *int) {
	if c.SecretKey == "" {
		return nil, nil
	}
	return &c.SecretKey, &c.SecretSlot
}

// AddUsageReading keeps a provider answer. A reading for a credential without
// a place in a runtime is not kept: nobody could attribute it.
func (s *Store) AddUsageReading(ctx context.Context, orgID uuid.UUID, r MeterReading) error {
	if r.Credential.RuntimeID == uuid.Nil || r.Credential.Ord < 0 {
		return nil
	}
	pct := func(v float64) *float64 {
		if v < 0 {
			return nil
		}
		return &v
	}
	var fetched, resets *time.Time
	if !r.FetchedAt.IsZero() {
		fetched = &r.FetchedAt
	}
	if !r.ResetsAt.IsZero() {
		resets = &r.ResetsAt
	}
	if r.Source == "" {
		r.Source = SourceUsageCmd
	}
	if r.WindowKind == "" {
		r.WindowKind = WindowSevenDay
	}
	key, slot := r.Credential.secret()
	_, err := s.pool.Exec(ctx, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord,
		secret_key, secret_slot, credential_kind, source, window_kind, supported,
		window_percent, week_percent, window_resets, week_resets, resets_at, status, reason, stale,
		fetched_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,COALESCE($18::timestamptz, now()), now())
		ON CONFLICT DO NOTHING`,
		orgID, r.Credential.RuntimeID, r.Credential.Ord, key, slot, r.Credential.Kind, r.Source, r.WindowKind,
		r.Supported, pct(r.WindowPercent), pct(r.WeekPercent),
		strings.TrimSpace(r.WindowResets), strings.TrimSpace(r.WeekResets), resets,
		r.Status, r.Reason, r.Stale, fetched)
	return err
}

// IsRateLimitEvent is the cheap pre-check for a Claude Code rate_limit_event.
func IsRateLimitEvent(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"rate_limit_event"`)) && bytes.Contains(raw, []byte(`"rate_limit_info"`))
}

// inbandInsert files a rate_limit_event's windows ($7) as readings of the
// credential $2..$6 (runtime_id, ord, secret_key, secret_slot, kind) — the
// expression of the backfill in 0145: one row per window, five-hour windows in
// window_percent, the weekly ones in week_percent, the utilisation in percent.
// A line from before unifiedWindows names its one window in rateLimitType.
const inbandInsert = `
INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
	source, window_kind, supported, window_percent, week_percent, resets_at, status, stale, fetched_at, received_at)
SELECT $1::uuid, $2::uuid, $3::int, $4::text, $5::smallint, $6::text, 'inband', w.key, true,
	CASE WHEN w.key LIKE 'five_hour%' THEN w.pct END,
	CASE WHEN w.key NOT LIKE 'five_hour%' THEN w.pct END,
	w.resets_at, w.status, false, now(), now()
FROM (SELECT $7::jsonb AS payload) e
CROSS JOIN LATERAL (
	SELECT m.key,
		round((m.value->>'utilization')::numeric * 100, 4)::float8 AS pct,
		CASE WHEN jsonb_typeof(m.value->'resetsAt') = 'number'
			THEN to_timestamp((m.value->>'resetsAt')::float8) END AS resets_at,
		CASE WHEN jsonb_typeof(m.value->'status') = 'string' THEN m.value->>'status' ELSE '' END AS status
	FROM jsonb_each(CASE
		WHEN jsonb_typeof(e.payload->'rate_limit_info'->'unifiedWindows') = 'object'
			THEN e.payload->'rate_limit_info'->'unifiedWindows'
		WHEN jsonb_typeof(e.payload->'rate_limit_info'->'rateLimitType') = 'string'
			THEN jsonb_build_object(e.payload->'rate_limit_info'->>'rateLimitType', e.payload->'rate_limit_info')
		ELSE '{}'::jsonb END) AS m(key, value)
	WHERE jsonb_typeof(m.value) = 'object' AND jsonb_typeof(m.value->'utilization') = 'number') w
WHERE e.payload->>'type' = 'rate_limit_event'
ON CONFLICT DO NOTHING`

// AddInbandReadings files a Claude Code rate_limit_event as one reading per
// window it names (five_hour, seven_day, …), source inband, against the
// credential the run went out on. A line of another type, or a run without a
// credential, files nothing.
func (s *Store) AddInbandReadings(ctx context.Context, orgID uuid.UUID, cred CredentialRef, payload json.RawMessage) error {
	if cred.RuntimeID == uuid.Nil || cred.Ord < 0 || !IsRateLimitEvent(payload) || !json.Valid(payload) {
		return nil
	}
	key, slot := cred.secret()
	_, err := s.pool.Exec(ctx, inbandInsert, orgID, cred.RuntimeID, cred.Ord, key, slot, cred.Kind, []byte(payload))
	return err
}

// UsageReading is one reading of the provider's meter.
type UsageReading struct {
	RuntimeID      uuid.UUID `json:"runtime_id"`
	CredentialOrd  int       `json:"credential_ord"`
	SecretKey      *string   `json:"secret_key"`
	SecretSlot     *int      `json:"secret_slot"`
	CredentialKind string    `json:"credential_kind"`
	// Source: usage_cmd | inband | manual. WindowKind: the provider's window
	// (seven_day, five_hour, …).
	Source        string     `json:"source"`
	WindowKind    string     `json:"window_kind"`
	Supported     bool       `json:"supported"`
	WindowPercent *float64   `json:"window_percent"`
	WeekPercent   *float64   `json:"week_percent"`
	WindowResets  string     `json:"window_resets,omitempty"`
	WeekResets    string     `json:"week_resets,omitempty"`
	ResetsAt      *time.Time `json:"resets_at"`
	Status        string     `json:"status"`
	// Reason is covey's code for a reading without a fresh figure
	// (no_runtime_config, no_reporter, exec_failed, unparsed, stale).
	Reason     string    `json:"reason"`
	Stale      bool      `json:"stale"`
	FetchedAt  time.Time `json:"fetched_at"`
	ReceivedAt time.Time `json:"received_at"`
	// CostUSD is the measured list price on this credential, from the start
	// of the requested window up to this reading: the per-line usage
	// (model_usage, Claude Code's own price per line) of the runs that went
	// out on the same secret value. Not the booked cost, and nothing of any
	// other credential. Nil for a reading that names no secret.
	CostUSD *float64 `json:"cost_usd"`
}

// UsageReadings returns the readings since `since`, oldest first — the
// unsupported ones too, with their reason, so a credential that cannot be read
// shows as such — each with the measured cost on its credential up to it.
//
// One window-function stream: the credential's model_usage lines and its
// readings in time order, the lines' cost summed as it goes. A line at the
// same instant as a reading counts before it.
func (s *Store) UsageReadings(ctx context.Context, orgID uuid.UUID, since time.Time) ([]UsageReading, error) {
	rows, err := s.pool.Query(ctx, `WITH ev AS (
		SELECT mu.secret_key, mu.secret_slot, mu.created_at AS at, 0 AS o, mu.engine_usd AS usd, NULL::bigint AS reading
		FROM model_usage mu
		WHERE mu.org_id = $1 AND mu.created_at >= $2 AND mu.secret_key IS NOT NULL
		UNION ALL
		SELECT r.secret_key, r.secret_slot, r.fetched_at, 1, 0, r.id
		FROM usage_readings r
		WHERE r.org_id = $1 AND r.fetched_at >= $2),
	c AS (
		SELECT reading, SUM(usd) OVER (PARTITION BY secret_key, secret_slot ORDER BY at, o
			ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cum
		FROM ev)
	SELECT r.runtime_id, r.credential_ord, r.secret_key, r.secret_slot, r.credential_kind, r.source, r.window_kind,
		r.supported, r.window_percent, r.week_percent, r.window_resets, r.week_resets, r.resets_at,
		r.status, r.reason, r.stale, r.fetched_at, r.received_at,
		CASE WHEN r.secret_key IS NOT NULL THEN c.cum::float8 END
	FROM c JOIN usage_readings r ON r.id = c.reading
	ORDER BY r.fetched_at, r.id`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageReading{}
	for rows.Next() {
		var u UsageReading
		if err := rows.Scan(&u.RuntimeID, &u.CredentialOrd, &u.SecretKey, &u.SecretSlot, &u.CredentialKind,
			&u.Source, &u.WindowKind, &u.Supported, &u.WindowPercent, &u.WeekPercent,
			&u.WindowResets, &u.WeekResets, &u.ResetsAt, &u.Status, &u.Reason, &u.Stale,
			&u.FetchedAt, &u.ReceivedAt, &u.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
