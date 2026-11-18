package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/observability"
)

// A Claude Code `result` line as it arrives from the daemon: an Opus run that
// also made small Haiku calls, cache writes split by lifetime (shape and
// numbers from a real run). A CLI without model_usage_since_last_result: the
// figures are the session's running totals, here the session's first line.
const resultLine = `{"type":"result","subtype":"success","session_id":"sess-1","uuid":"line-1","num_turns":31,
 "total_cost_usd":1.3991645,
 "modelUsage":{
  "claude-opus-5-5":{"costUSD":1.3809534,"inputTokens":60,"outputTokens":26305,"thinkingTokens":19448,
    "cacheReadInputTokens":1356027,"cacheCreationInputTokens":72926,
    "cacheCreationEphemeral1hInputTokens":72926,"cacheCreationEphemeral5mInputTokens":0,"webSearchRequests":0,"costBasis":"list"},
  "claude-haiku-5-5":{"costUSD":0.0182111,"inputTokens":42576,"outputTokens":7907,"thinkingTokens":4793,
    "cacheReadInputTokens":0,"cacheCreationInputTokens":0,
    "cacheCreationEphemeral1hInputTokens":0,"cacheCreationEphemeral5mInputTokens":0,"webSearchRequests":1,"costBasis":"list"}}}`

// ccUsage is one model's figures in a Claude Code result line.
type ccUsage struct {
	USD                            float64
	In, Out, Think, Read, W1h, Web int64
}

func (u ccUsage) entry() map[string]any {
	return map[string]any{"costUSD": u.USD, "inputTokens": u.In, "outputTokens": u.Out, "thinkingTokens": u.Think,
		"cacheReadInputTokens": u.Read, "cacheCreationInputTokens": u.W1h,
		"cacheCreationEphemeral5mInputTokens": 0, "cacheCreationEphemeral1hInputTokens": u.W1h,
		"webSearchRequests": u.Web, "costBasis": "list"}
}

// recordedLine is one result line of a recorded session: running totals in
// modelUsage, the line's own share in model_usage_since_last_result.
type recordedLine struct {
	task     int // which of the session's tasks wrote it
	duration int
	total    float64
	cumul    map[string]ccUsage
	since    map[string]ccUsage
}

const (
	opus  = "claude-opus-5-5"
	haiku = "claude-haiku-5-5"
)

// Session 0b3bc4ce of the first cost evening: a run that hit its turn limit
// (main line plus the one-turn handover summary), its continuation (again with
// a turn limit and a summary), and a second continuation. Haiku side calls
// happen once, early; the running totals carry them on every later line.
var (
	sideCalls = ccUsage{0.0147441, 29681, 3552, 1878, 0, 0, 1}
	session   = []recordedLine{
		{0, 360607, 1.8177047,
			map[string]ccUsage{opus: {1.8029606, 60, 34246, 23165, 1781843, 95179, 0}, haiku: sideCalls},
			map[string]ccUsage{opus: {1.8029606, 60, 34246, 23165, 1781843, 95179, 0}, haiku: sideCalls}},
		{0, 11416, 2.5497767,
			map[string]ccUsage{opus: {2.5350326, 62, 35324, 23328, 1781843, 183992, 0}, haiku: sideCalls},
			map[string]ccUsage{opus: {0.732072, 2, 1078, 163, 0, 88813, 0}}},
		{1, 411604, 4.7895863,
			map[string]ccUsage{opus: {4.7748422, 122, 66113, 40605, 5041711, 305469, 0}, haiku: sideCalls},
			map[string]ccUsage{opus: {2.2398096, 60, 30789, 17277, 3259868, 121477, 0}}},
		{1, 9806, 5.7923505,
			map[string]ccUsage{opus: {5.7776064, 124, 67029, 40655, 5058292, 428109, 0}, haiku: sideCalls},
			map[string]ccUsage{opus: {1.0027642, 2, 916, 50, 16581, 122640, 0}}},
		{2, 247990, 6.4510877,
			map[string]ccUsage{opus: {6.4363436, 140, 79242, 45632, 6212878, 451046, 0}, haiku: sideCalls},
			map[string]ccUsage{opus: {0.6587372, 16, 12213, 4977, 1154586, 22937, 0}}},
	}
	// What a daemon from before the cost basis booked per task: the running
	// total, and after a turn limit the handover's running total on top.
	bookedBefore = []float64{4.367481, 10.581937, 6.451088}
	// What each task really added, as model_usage stores it (each line's
	// costUSD to 1e-6). Together the session's last running total, 6.4511.
	ownShare = []float64{2.549777, 3.242574, 0.658737}
)

const sessionTotal = 6.451088

// json renders the line. withSince=false is a CLI without the per-line field.
func (l recordedLine) json(sessionID string, n int, withSince bool) string {
	usage := func(m map[string]ccUsage) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v.entry()
		}
		return out
	}
	line := map[string]any{"type": "result", "subtype": "success", "session_id": sessionID,
		"uuid": fmt.Sprintf("%s-line-%d", sessionID, n), "duration_ms": l.duration, "total_cost_usd": l.total,
		"usage":      map[string]any{"service_tier": "standard", "speed": "standard", "inference_geo": "global"},
		"modelUsage": usage(l.cumul)}
	if withSince {
		line["model_usage_since_last_result"] = usage(l.since)
	}
	raw, _ := json.Marshal(line)
	return string(raw)
}

// sinceUSD is what the task's lines added, unrounded: what a daemon with the
// cost basis sends for it.
func sinceUSD(task int) float64 {
	sum := 0.0
	for _, l := range session {
		if l.task == task {
			for _, u := range l.since {
				sum += u.USD
			}
		}
	}
	return sum
}

// mockLines is a mock body that replays the task's lines of the session.
func mockLines(sessionID string, task int, withSince bool) string {
	var b strings.Builder
	for n, l := range session {
		if l.task == task {
			b.WriteString("[mock:line " + l.json(sessionID, n, withSince) + "]\n")
		}
	}
	return b.String()
}

// runChain runs the session's three tasks through the real orchestrator, the
// later two as continuations that resume it, each with the given cost message.
func runChain(t *testing.T, s *stack, agentID uuid.UUID, sessionID string, cost func(task int) string) []uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var ids []uuid.UUID
	for i := range bookedBefore {
		body := mockLines(sessionID, i, true) + "[mock:cost " + cost(i) + "]"
		var (
			task backlog.Task
			err  error
		)
		if i == 0 {
			task, err = s.backlog.Create(ctx, s.orgID, agentID, "Sylius bench", body, "manual", 3)
		} else {
			task, err = s.backlog.CreateChild(ctx, ids[i-1], backlog.ChildSpec{Title: "Sylius bench", Body: body,
				Origin: "continuation:" + ids[i-1].String(), SessionID: sessionID, ResumeInput: "carry on"})
		}
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "task done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
		ids = append(ids, task.ID)
	}
	return ids
}

// The split per model only ever lived in the recording, which is pruned. Filing
// a result line must leave one row per model — once, however often the same
// line were filed — and the report must price it with the list so that a
// drift between engine and list shows up.
func TestModelUsageFromResultLine(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("modell-verbrauch")
	task := uuid.New()
	none := observability.CredentialRef{}

	// An ordinary transcript line files nothing per model.
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, json.RawMessage(`{"type":"assistant","message":{"model":"claude-opus-5-5"}}`)); err != nil {
		t.Fatal(err)
	}
	// The same line twice (a re-sent event): the recording keeps both, the
	// consumption counts once.
	for range 2 {
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, json.RawMessage(resultLine)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1`, task).Scan(&n)
	if n != 2 {
		t.Fatalf("model_usage rows = %d, want 2 (opus + haiku)", n)
	}
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM recording_events WHERE task_id=$1 AND kind='runtime'`, task).Scan(&n)
	if n != 3 {
		t.Fatalf("recording events = %d, want 3", n)
	}

	rep, err := s.obs.ModelUsage(ctx, agent.OrgID, time.Now().Add(-time.Hour), observability.CredentialFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]observability.ModelUsageRow{}
	for _, r := range rep.Rows {
		byModel[r.Model] = r
	}
	opusRow := byModel[opus]
	if opusRow.CacheWrite1h != 72926 || opusRow.CacheRead != 1356027 || opusRow.Thinking != 19448 {
		t.Errorf("opus tokens not filed as reported: %+v", opusRow.ModelTokens)
	}
	if opusRow.Runs != 1 {
		t.Errorf("runs = %d, want the one task", opusRow.Runs)
	}
	// The list reproduces the engine's own price (1-hour cache writes at 2x input).
	if opusRow.ListUSD == nil || math.Abs(*opusRow.ListUSD-opusRow.EngineUSD) > 0.0001 {
		t.Errorf("opus list price %v vs engine %v", opusRow.ListUSD, opusRow.EngineUSD)
	}
	haikuRow := byModel[haiku]
	if haikuRow.WebSearches != 1 || haikuRow.ListUSD == nil {
		t.Errorf("haiku row incomplete: %+v", haikuRow)
	}

	// Over the API, same window rules as the other cost endpoints.
	got := admin.expect(http.MethodGet, "/api/v1/cost/models?days=1", nil, http.StatusOK)
	if rows, _ := got["rows"].([]any); len(rows) != 2 {
		t.Errorf("GET /cost/models rows = %v, want 2", got["rows"])
	}
	admin.expect(http.MethodGet, "/api/v1/cost/models?days=1&secret_slot=minus", nil, http.StatusBadRequest)
}

// The chart's cells carry both figures for the same runs: the booked (reported)
// cost and the measured one, the per-line deltas split by cost type — and a
// booking without any line shows up as "unattributed" rather than
// disappearing. Each cell is one credential: two seats are two cells.
func TestCostCellsReportedAndMeasured(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("kosten-zellen")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), Ord: 0,
		SecretKey: "claude_code_oauth_token", SecretSlot: 0, Kind: "subscription"}
	key := observability.CredentialRef{RuntimeID: seat.RuntimeID, Ord: 1,
		SecretKey: "anthropic_api_key", SecretSlot: 1, Kind: "api_key"}

	// A run on the seat, booked the way the orchestrator books Claude Code.
	task := uuid.New()
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, seat, json.RawMessage(resultLine)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: 1.3991645,
		Tokens: observability.Tokens{Output: 34212}, Model: opus, Credential: seat,
		Basis: observability.BasisInvocation}, false); err != nil {
		t.Fatal(err)
	}
	// A run on the API key: another session's last line, another task.
	keyTask := uuid.New()
	line := session[4].json("sess-key", 4, true)
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &keyTask, key, json.RawMessage(line)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &keyTask, USD: 0.6587372,
		Model: opus, Credential: key, Basis: observability.BasisInvocation}, false); err != nil {
		t.Fatal(err)
	}
	// And a run on the seat without any breakdown.
	other := uuid.New()
	if _, err := s.obs.BookCost(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &other, USD: 2.5,
		Tokens: observability.Tokens{Output: 10}, Model: opus, Credential: seat}); err != nil {
		t.Fatal(err)
	}

	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "day", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cells.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(cells.Agents))
	}
	byKey := map[string]observability.CostCell{}
	for _, c := range cells.Cells {
		if c.SecretKey == nil || c.SecretSlot == nil {
			t.Fatalf("a cell of an attributed run carries no credential: %+v", c)
		}
		byKey[*c.SecretKey] = c
	}
	// One day, one agent: the cells are split by the secret value only.
	if len(cells.Cells) != 2 || len(byKey) != 2 {
		t.Fatalf("cells = %d (%v), want one per credential", len(cells.Cells), byKey)
	}
	sc, kc := byKey[seat.SecretKey], byKey[key.SecretKey]
	if sc.CredentialKind != "subscription" || kc.CredentialKind != "api_key" || *kc.SecretSlot != 1 {
		t.Errorf("credential columns: seat %q, key %q slot %d", sc.CredentialKind, kc.CredentialKind, *kc.SecretSlot)
	}
	if math.Abs(sc.Reported-3.899165) > 1e-6 || math.Abs(kc.Reported-0.658737) > 1e-6 {
		t.Errorf("reported = %v / %v, want 3.899165 / 0.658737", sc.Reported, kc.Reported)
	}

	// Measured is the sum of the deltas, priced per type; the booking without
	// lines stands beside it, unsplit.
	priced := func(c observability.CostCell) float64 {
		sum := 0.0
		for k, v := range c.Measured {
			if k != observability.TypeUnattributed {
				sum += v
			}
		}
		return sum
	}
	rows, err := s.pool.Query(ctx, `SELECT model, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens,
		cache_write_1h_tokens, web_search_requests FROM model_usage WHERE task_id = $1`, task)
	if err != nil {
		t.Fatal(err)
	}
	deltas, lines := 0.0, 0
	for rows.Next() {
		var model string
		var tok observability.ModelTokens
		if err := rows.Scan(&model, &tok.Input, &tok.Output, &tok.CacheRead, &tok.CacheWrite5m, &tok.CacheWrite1h, &tok.WebSearches); err != nil {
			t.Fatal(err)
		}
		p, ok := observability.ListPrice(model)
		if !ok {
			t.Fatalf("no list price for %s", model)
		}
		deltas += p.Cost(tok)
		lines++
	}
	rows.Close()
	if lines != 2 || math.Abs(priced(sc)-deltas) > 1e-9 {
		t.Errorf("measured %v vs the deltas' list price %v (%d lines, parts %v)", priced(sc), deltas, lines, sc.Measured)
	}
	if math.Abs(sc.Measured[observability.TypeUnattributed]-2.5) > 0.001 {
		t.Errorf("unattributed = %v, want 2.5", sc.Measured[observability.TypeUnattributed])
	}
	if math.Abs(priced(kc)-0.6587372) > 0.0001 || kc.Measured[observability.TypeUnattributed] != 0 {
		t.Errorf("the key's cell measured %v, want its one line (0.6587)", kc.Measured)
	}
	if sc.Measured[observability.TypeCacheWrite] <= sc.Measured[observability.TypeCacheRead] {
		t.Errorf("for this run the 1-hour cache writes cost more than the cache reads: %v", sc.Measured)
	}

	// What Claude Code priced the same lines at, and their tokens: the
	// figures of the same calls, never the booking.
	var engine float64
	var output, write1h int64
	if err := s.pool.QueryRow(ctx, `SELECT SUM(engine_usd)::float8, SUM(output_tokens), SUM(cache_write_1h_tokens)
		FROM model_usage WHERE task_id = $1`, task).Scan(&engine, &output, &write1h); err != nil {
		t.Fatal(err)
	}
	if math.Abs(sc.EngineUSD-engine) > 1e-9 || sc.Tokens["output"] != output || sc.Tokens["cache_write_1h"] != write1h {
		t.Errorf("seat cell engine %v tokens %v, want engine %v, output %d, 1h writes %d", sc.EngineUSD, sc.Tokens, engine, output, write1h)
	}
	if math.Abs(cells.Agents[0].Measured-(priced(sc)+priced(kc))) > 1e-9 {
		t.Errorf("agent measured = %v, want the priced lines of both cells (%v)", cells.Agents[0].Measured, priced(sc)+priced(kc))
	}
	// The booking-check: per credential and basis, the line-less booking
	// under the empty basis of an older daemon.
	bases := map[string]int64{}
	for _, b := range cells.Bookings {
		if b.SecretKey == nil {
			t.Fatalf("an attributed booking without a credential: %+v", b)
		}
		bases[*b.SecretKey+"/"+b.Basis] += b.Entries
	}
	if bases[seat.SecretKey+"/"+observability.BasisInvocation] != 1 || bases[seat.SecretKey+"/"] != 1 ||
		bases[key.SecretKey+"/"+observability.BasisInvocation] != 1 {
		t.Errorf("bookings per credential and basis = %v", bases)
	}
	cg := admin.expect(http.MethodGet, "/api/v1/cost/cells?days=1&bucket=hour", nil, http.StatusOK)
	if _, ok := cg["bookings"].([]any); !ok {
		t.Errorf("GET /cost/cells lacks bookings: %v", cg["bookings"])
	}

	// Narrowed to the key: its line, and nothing of the seat.
	rep, err := s.obs.ModelUsage(ctx, agent.OrgID, time.Now().Add(-time.Hour),
		observability.CredentialFilter{SecretKey: key.SecretKey, SecretSlot: key.SecretSlot})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].CredentialKind != "api_key" || rep.RunsWithout != 0 {
		t.Errorf("filtered to the key: %+v", rep)
	}
	got := admin.expect(http.MethodGet, "/api/v1/cost/models?days=1&secret_key=anthropic_api_key&secret_slot=1", nil, http.StatusOK)
	if rows, _ := got["rows"].([]any); len(rows) != 1 {
		t.Errorf("GET /cost/models for the key: rows = %v, want 1", got["rows"])
	}
}

// The priciest runs are ranked by what their lines cost at the list, not by
// what was booked: a booking can be an older daemon's running total. A run
// without lines keeps only its booking and has no recomputed figure.
func TestRunCostsRankedByRecomputedCost(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("kosten-rang")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), Ord: 0,
		SecretKey: "claude_code_oauth_token", SecretSlot: 0, Kind: "subscription"}
	lined, err := s.backlog.Create(ctx, agent.OrgID, agent.ID, "Mit Zeilen", "", "heartbeat", 0)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := s.backlog.Create(ctx, agent.OrgID, agent.ID, "Ohne Zeilen", "", "heartbeat", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &lined.ID, seat, json.RawMessage(resultLine)); err != nil {
		t.Fatal(err)
	}
	// Booked low on purpose: the ranking must not follow the booking.
	if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &lined.ID, USD: 0.01,
		Model: opus, Credential: seat, Basis: observability.BasisInvocation}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.obs.BookCost(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &bare.ID, USD: 1.0,
		Model: opus, Credential: seat}); err != nil {
		t.Fatal(err)
	}
	var engine float64
	if err := s.pool.QueryRow(ctx, `SELECT SUM(engine_usd)::float8 FROM model_usage WHERE task_id = $1`, lined.ID).Scan(&engine); err != nil {
		t.Fatal(err)
	}
	if engine <= 1.0 {
		t.Fatalf("the fixture's lines cost %v; the test needs more than the bare run's booking", engine)
	}
	runs, err := s.obs.RunCosts(ctx, agent.OrgID, nil, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].TaskID != lined.ID {
		t.Fatalf("runs = %+v, want the run with lines first", runs)
	}
	if r := runs[0]; r.MeasuredUSD == nil || r.EngineUSD == nil || math.Abs(*r.EngineUSD-engine) > 1e-6 ||
		math.Abs(*r.MeasuredUSD-engine) > 1e-4 {
		t.Errorf("recomputed %v, Claude Code %v, want both ≈ %v", r.MeasuredUSD, r.EngineUSD, engine)
	}
	if runs[1].MeasuredUSD != nil || runs[1].TotalUSD != 1.0 {
		t.Errorf("the run without lines: %+v", runs[1])
	}
}

// A CLI without model_usage_since_last_result reports only running totals: a
// handover's second line repeats the first. The row is the difference to what
// the session's earlier rows hold, so the sum is the session's last total.
// And the meter's movement per period becomes the third figure in the cells.
func TestCumulativeSessionAndMeterSteps(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("kumuliert")
	task := uuid.New()
	first := `{"type":"result","session_id":"same","modelUsage":{"claude-opus-5-5":{"costUSD":5.0,"outputTokens":250000}}}`
	again := `{"type":"result","session_id":"same","modelUsage":{"claude-opus-5-5":{"costUSD":5.4,"outputTokens":270000}}}`
	for _, l := range []string{first, again} {
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{}, json.RawMessage(l)); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.obs.ModelUsage(ctx, agent.OrgID, time.Now().Add(-time.Hour), observability.CredentialFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 || math.Abs(rep.Rows[0].EngineUSD-5.4) > 1e-9 || rep.Rows[0].Output != 270000 {
		t.Fatalf("want the session's last running total (5.4 USD, 270000 out), got %+v", rep.Rows)
	}
	var derived int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1 AND basis='derived'`, task).Scan(&derived)
	if derived != 2 {
		t.Errorf("derived rows = %d, want both lines", derived)
	}

	seat := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "kumuliert_token", Kind: "subscription"}
	base := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	for _, r := range []struct {
		at  time.Duration
		pct float64
	}{{0, 9}, {30 * time.Minute, 10}, {70 * time.Minute, 12}, {100 * time.Minute, 3}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: seat,
			Supported: true, WindowPercent: 1, WeekPercent: r.pct, FetchedAt: base.Add(r.at)}); err != nil {
			t.Fatal(err)
		}
	}
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "hour", base.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, st := range cells.Subscription {
		got[st.Period.UTC().Format("15")] += st.Points
	}
	// 9 → 10 in the first hour; 10 → 12, then a collapse to 3 without any
	// reset time (counted as a new week, 3) in the second.
	if got[base.UTC().Format("15")] != 1 || got[base.Add(time.Hour).UTC().Format("15")] != 5 {
		t.Errorf("meter steps per hour = %v, want 1 then 5", got)
	}
}

// Each reading carries the MEASURED list price on its own credential up to
// it: the model_usage lines of the runs that went out on the same secret,
// summed in time order — not the booked cost, and nothing of another seat.
// Unsupported readings are listed too, with their reason.
func TestUsageReadingsMeasuredPerCredential(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("abo-zaehler")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	other := observability.CredentialRef{RuntimeID: seat.RuntimeID, Ord: 1, SecretKey: "claude_code_oauth_token", SecretSlot: 1, Kind: "subscription"}
	start := time.Now().Add(-time.Hour)

	reading := func(c observability.CredentialRef, pct float64, supported bool, reason string) {
		t.Helper()
		r := observability.MeterReading{Credential: c, Supported: supported, Reason: reason,
			WindowPercent: -1, WeekPercent: -1, FetchedAt: time.Now()}
		if supported {
			r.WeekPercent = pct
		}
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, r); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	line := func(c observability.CredentialRef, n int) {
		t.Helper()
		task := uuid.New()
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, c, json.RawMessage(session[n].json("sess-"+task.String(), n, true))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A booking that is no line at all must not count either.
	if err := s.obs.AddCost(ctx, agent.ID, nil, 30, observability.Tokens{Output: 10}, opus, uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}
	reading(seat, 9, true, "")
	line(seat, 4)  // 0.6587372
	line(other, 1) // 0.732072, on the other value
	reading(seat, 10, true, "")
	reading(other, 0, false, "exec_failed")
	line(seat, 3) // 1.0027642
	reading(seat, 11, true, "")
	reading(other, 20, true, "")

	rs, err := s.obs.UsageReadings(ctx, agent.OrgID, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 5 {
		t.Fatalf("readings = %d, want 5 (the unsupported one too)", len(rs))
	}
	// What each reading must carry: the lines of its own credential up to it,
	// from model_usage itself.
	var want []float64
	for _, r := range rs {
		var sum float64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(engine_usd),0)::float8 FROM model_usage
			WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND created_at >= $4 AND created_at <= $5`,
			agent.OrgID, *r.SecretKey, *r.SecretSlot, start, r.FetchedAt).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		want = append(want, sum)
	}
	for i, r := range rs {
		if r.CostUSD == nil || math.Abs(*r.CostUSD-want[i]) > 1e-9 {
			t.Errorf("reading %d (%s#%d, %v%%): cost so far %v, want %v", i, *r.SecretKey, *r.SecretSlot, r.WeekPercent, r.CostUSD, want[i])
		}
	}
	if math.Abs(want[0]) > 1e-9 || math.Abs(want[1]-0.658737) > 1e-6 || math.Abs(want[3]-(0.658737+1.002764)) > 1e-6 || math.Abs(want[4]-0.732072) > 1e-6 {
		t.Errorf("fixture: per credential %v", want)
	}
	if rs[2].Supported || rs[2].Reason != "exec_failed" || rs[2].WeekPercent != nil {
		t.Errorf("the unsupported reading: %+v", rs[2])
	}
	if rs[1].Source != observability.SourceUsageCmd || rs[1].WindowKind != observability.WindowSevenDay {
		t.Errorf("source/window: %q/%q", rs[1].Source, rs[1].WindowKind)
	}

	got := admin.expectList(http.MethodGet, "/api/v1/cost/usage-readings?days=1", nil, http.StatusOK)
	if len(got) != 5 {
		t.Fatalf("GET /cost/usage-readings: %d rows", len(got))
	}
	first := got[0]
	for _, k := range []string{"source", "window_kind", "resets_at", "reason", "cost_usd", "secret_key"} {
		if _, ok := first[k]; !ok {
			t.Errorf("GET /cost/usage-readings lacks %q: %v", k, first)
		}
	}
	admin.expectList(http.MethodGet, "/api/v1/cost/subscription?days=1", nil, http.StatusOK)
}

// The evening that started this: a session that hit its turn limit, was
// handed over in a one-turn summary, continued in a second task (turn limit,
// summary) and finished in a third. Every result line reports the session's
// running total. Booked per run, that was 4.37 + 10.58 + 6.45 = 21.40 for
// 6.45 of work. Through the real orchestrator, from a daemon that sends each
// run's own spend and from one that still sends the running totals, every
// task has to carry exactly its own share, the bookings and the lines have to
// agree on 6.4511, and every line has to know which booking holds it.
func TestResumedTurnLimitedSessionBooksCostDeltas(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	for _, gen := range []struct {
		name, slug, basis string
		cost              func(task int) string
	}{
		{"daemon with cost basis", "bench-neu", observability.BasisInvocation,
			func(i int) string { return fmt.Sprintf(`{"usd":%v,"basis":"invocation"}`, sinceUSD(i)) }},
		{"daemon before the cost basis", "bench-alt", observability.BasisFromDeltas,
			func(i int) string { return fmt.Sprintf(`{"usd":%v}`, bookedBefore[i]) }},
	} {
		t.Run(gen.name, func(t *testing.T) {
			agent := s.newSupportAgent(gen.slug)
			tasks := runChain(t, s, agent.ID, uuid.NewString(), gen.cost)

			var booked, measured float64
			var unlinked int
			_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(usd),0)::float8 FROM cost_entries WHERE agent_id=$1`, agent.ID).Scan(&booked)
			_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(engine_usd),0)::float8, count(*) FILTER (WHERE cost_entry_id IS NULL)
				FROM model_usage WHERE agent_id=$1`, agent.ID).Scan(&measured, &unlinked)
			if math.Abs(booked-sessionTotal) > 1e-6 || math.Abs(measured-sessionTotal) > 1e-6 {
				t.Errorf("booked %v, lines %v — want both at the session's last total %v", booked, measured, sessionTotal)
			}
			if unlinked != 0 {
				t.Errorf("%d lines are not linked to a booking", unlinked)
			}
			for i, task := range tasks {
				var (
					n            int
					usd, lineUSD float64
					basis        string
				)
				if err := s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(usd),0)::float8, COALESCE(MAX(basis),'')
					FROM cost_entries WHERE task_id=$1`, task).Scan(&n, &usd, &basis); err != nil {
					t.Fatal(err)
				}
				_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(mu.engine_usd),0)::float8 FROM model_usage mu
					JOIN cost_entries ce ON ce.id = mu.cost_entry_id WHERE ce.task_id=$1`, task).Scan(&lineUSD)
				if n != 1 || math.Abs(usd-ownShare[i]) > 1e-6 || math.Abs(lineUSD-ownShare[i]) > 1e-6 {
					t.Errorf("task %d: %d entries, booked %v, its lines %v — want its own share %v (not 0, not the session's %v)",
						i, n, usd, lineUSD, ownShare[i], bookedBefore[i])
				}
				if basis != gen.basis {
					t.Errorf("task %d booked with basis %q, want %q", i, basis, gen.basis)
				}
			}
			// The lines keep what the CLI said about them.
			var started, tier int
			_ = s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE started_at < created_at), count(*) FILTER (WHERE service_tier = 'standard')
				FROM model_usage WHERE agent_id=$1`, agent.ID).Scan(&started, &tier)
			if started != 6 || tier != 6 {
				t.Errorf("of 6 lines, %d carry a start and %d a service tier", started, tier)
			}
		})
	}
}

// A daemon from before the cost basis sends the session's running total. The
// booking is the sum of the run's own lines instead, tokens included (all
// models, cache writes as 5 min + 1 h) — the daemon's figure only when the
// run left no line to compute it from. Also for a CLI that writes no
// per-line field, where the lines are derived from the running totals.
func TestOldDaemonCostBookedFromDeltas(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("alter-daemon")
	run := func(body string) uuid.UUID {
		t.Helper()
		task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Lauf", body, "manual", 3)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "task done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
		return task.ID
	}
	entry := func(task uuid.UUID) (usd float64, out, cacheWrite int64, basis string) {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT usd::float8, output_tokens, cache_creation_tokens, basis
			FROM cost_entries WHERE task_id=$1`, task).Scan(&usd, &out, &cacheWrite, &basis); err != nil {
			t.Fatal(err)
		}
		return
	}

	// The continuation of the evening's session, with the per-line field.
	cont := run(mockLines(uuid.NewString(), 1, true) + `[mock:cost {"usd":10.581937,"output_tokens":31705}]`)
	usd, out, cw, basis := entry(cont)
	if math.Abs(usd-ownShare[1]) > 1e-6 || basis != observability.BasisFromDeltas {
		t.Errorf("continuation booked %v (%s), want its lines' %v (from_deltas)", usd, basis, ownShare[1])
	}
	if out != 30789+916 || cw != 121477+122640 {
		t.Errorf("tokens out %d, cache write %d — want the lines' sums", out, cw)
	}

	// The first task on a CLI without the per-line field: derived lines.
	first := run(mockLines(uuid.NewString(), 0, false) + `[mock:cost {"usd":4.367481}]`)
	usd, out, _, basis = entry(first)
	if math.Abs(usd-ownShare[0]) > 1e-6 || basis != observability.BasisFromDeltas {
		t.Errorf("derived lines booked %v (%s), want %v (from_deltas)", usd, basis, ownShare[0])
	}
	if out != 34246+3552+1078 {
		t.Errorf("output tokens %d, want every model's", out)
	}

	// No line at all: the daemon's figure is all there is.
	bare := run(`[mock:cost {"usd":10.58}]`)
	if usd, _, _, basis := entry(bare); usd != 10.58 || basis != observability.BasisLegacy {
		t.Errorf("a run without lines booked %v (%s), want 10.58 (legacy)", usd, basis)
	}
}

// A line the engine shaped unexpectedly is still filed in the recording: the
// per-model rows come from a second statement, guarded number by number, and
// what still fails there is returned, not swallowed.
func TestRecordRuntimeModelUsageSurvivesBadCast(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("schief")
	task := uuid.New()
	none := observability.CredentialRef{}

	odd := `{"type":"result","session_id":"odd","model_usage_since_last_result":{"claude-opus-5-5":
		{"inputTokens":1.2e3,"outputTokens":"many","costUSD":"n/a","cacheReadInputTokens":10,"webSearchRequests":null}}}`
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, json.RawMessage(odd)); err != nil {
		t.Fatalf("a guarded line must file cleanly: %v", err)
	}
	var in, out, read int64
	var usd float64
	if err := s.pool.QueryRow(ctx, `SELECT input_tokens, output_tokens, cache_read_tokens, engine_usd::float8
		FROM model_usage WHERE task_id=$1`, task).Scan(&in, &out, &read, &usd); err != nil {
		t.Fatal(err)
	}
	if in != 1200 || out != 0 || read != 10 || usd != 0 {
		t.Errorf("guarded values = in %d out %d read %d usd %v, want 1200/0/10/0", in, out, read, usd)
	}

	// Beyond what a bigint holds: the row cannot be filed, the line still is.
	huge := `{"type":"result","session_id":"huge","model_usage_since_last_result":{"claude-opus-5-5":{"outputTokens":1e30}}}`
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, json.RawMessage(huge)); err == nil {
		t.Error("a row that cannot be filed has to be reported")
	}
	var lines int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM recording_events WHERE task_id=$1 AND kind='runtime'`, task).Scan(&lines)
	if lines != 2 {
		t.Errorf("recording events = %d, want both lines", lines)
	}
}

// --- The subscription meter (migration 0145) ---

// mockSeatEngine is the mock engine on a subscription seat. The mock itself
// needs no credential and is therefore never asked for its meter; an engine
// that declares one is, and that is the path these tests drive — the daemon,
// the protocol and the filing as they run for Claude Code, without a provider.
const mockSeatEngine = "mock-seat"

type mockSeat struct{ daemon.Mock }

func (mockSeat) Name() string { return mockSeatEngine }

func init() {
	daemon.RegisterRuntime(daemon.RuntimeDescriptor{
		Name: mockSeatEngine, Label: "Mock (seat)",
		Description: "The mock engine on a subscription seat, for the meter tests.",
		Credentials: []daemon.RuntimeCredential{{Kind: daemon.CredSubscription, Label: "subscription",
			Secret: "mock_seat_token", EnvVar: "MOCK_SEAT_TOKEN"}},
		Capabilities: daemon.RuntimeCapabilities{Resume: true, SkillsDir: ".claude/skills", ResultUsage: true},
		New:          func() daemon.Runtime { return mockSeat{} },
	})
}

// meterSeatAgent creates an agent on a seat of the mock-seat engine.
func (s *stack) meterSeatAgent(t *testing.T, slug string) (agents.Agent, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	rt, err := s.runtimes.Create(ctx, s.orgID, mockSeatEngine, "Mock-Sitz "+slug, "")
	if err != nil {
		t.Fatal(err)
	}
	s.seat(t, rt.ID, daemon.CredSubscription, "mock_seat_token", "mock-"+slug, "A")
	agent, err := s.registry.Create(ctx, s.orgID, slug, "Zaehler-Agent", mockSeatEngine, &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.registry.SaveConfig(ctx, agent.ID, map[string]string{
		"SOUL.md":   "# Zaehler-Agent\n\n## Role\nMisst.",
		"ACCESS.md": "- system: zammad scope: read",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}
	if err := s.runtimes.Assign(ctx, s.orgID, agent.ID, rt.ID); err != nil {
		t.Fatal(err)
	}
	return agent, rt.ID
}

// The control plane asks for the meter the moment the link is up — before the
// first task brings the daemon its config. A daemon that looked the engine up
// in that config answered "not supported" within milliseconds, and that was
// the only reading the live path ever filed. The question names the engine
// now: a fresh daemon asks it, and a supported reading lands on the secret.
func TestUsageRequestBeforeInjectConfig(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent, rt := s.meterSeatAgent(t, "zaehler-frisch")
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Lauf", "[mock:result fertig]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
	waitFor(t, "a usage reading", 10*time.Second, func() bool {
		var n int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_readings WHERE runtime_id = $1`, rt).Scan(&n)
		return n > 0
	})

	var (
		supported              bool
		week, window           *float64
		reason, key, kind, src string
		slot                   int
		resets                 *time.Time
		fetched                time.Time
	)
	if err := s.pool.QueryRow(ctx, `SELECT supported, week_percent, window_percent, reason, secret_key, secret_slot,
		credential_kind, source, resets_at, fetched_at FROM usage_readings WHERE runtime_id = $1 ORDER BY id LIMIT 1`, rt).
		Scan(&supported, &week, &window, &reason, &key, &slot, &kind, &src, &resets, &fetched); err != nil {
		t.Fatal(err)
	}
	if !supported || reason != "" {
		t.Fatalf("the first reading is supported=%v, reason %q — the daemon did not ask the named engine", supported, reason)
	}
	if week == nil || *week != 13 || window == nil || *window != 4 {
		t.Errorf("percents week %v, window %v — want the mock's 13 / 4", week, window)
	}
	if key != "mock_seat_token" || slot != 0 || kind != "subscription" || src != observability.SourceUsageCmd {
		t.Errorf("filed on %s#%d (%s), source %s", key, slot, kind, src)
	}
	want, _ := daemon.ParseResets("Nov 20, 4:59pm", "Europe/Berlin", fetched)
	if resets == nil || !resets.Equal(want) {
		t.Errorf("resets_at = %v, want %v", resets, want)
	}
}

// event9802 is a rate_limit_event as the first cost evening recorded it.
const event9802 = `{"type": "rate_limit_event", "uuid": "16494fc1-cc52-4575-bb1f-af1456f515bf", "session_id": "49ca4226-eebf-452c-ad27-7b50d0b33282", "rate_limit_info": {"status": "allowed", "resetsAt": 1795190400, "overageStatus": "rejected", "rateLimitType": "seven_day", "isUsingOverage": false, "unifiedWindows": {"seven_day": {"status": "allowed", "resetsAt": 1795190400, "utilization": 0.13}}, "overageDisabledReason": "out_of_credits"}}`

// twoWindows names the five-hour and the weekly window at once.
const twoWindows = `{"type":"rate_limit_event","session_id":"s2","rate_limit_info":{"status":"allowed_warning","unifiedWindows":{"five_hour":{"status":"allowed_warning","resetsAt":1795100000,"utilization":0.41},"seven_day":{"status":"allowed","resetsAt":1795190400,"utilization":0.135}}}}`

// Claude Code's rate_limit_event is the provider's meter in band: every
// window it names, its utilisation and its reset. Each window becomes one
// reading of the run's credential, source inband — directly and through the
// orchestrator's filing of a run's line.
func TestInbandReadingFiled(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("im-band")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "claude_code_oauth_token", Kind: "subscription"}

	if err := s.obs.AddInbandReadings(ctx, agent.OrgID, seat, json.RawMessage(event9802)); err != nil {
		t.Fatal(err)
	}
	// Nothing for a run without a credential, nothing for another line.
	if err := s.obs.AddInbandReadings(ctx, agent.OrgID, observability.CredentialRef{}, json.RawMessage(event9802)); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddInbandReadings(ctx, agent.OrgID, seat, json.RawMessage(resultLine)); err != nil {
		t.Fatal(err)
	}
	type row struct {
		window, status, key, src string
		week, five               *float64
		resets                   *time.Time
	}
	read := func(where string, args ...any) []row {
		t.Helper()
		rows, err := s.pool.Query(ctx, `SELECT window_kind, status, COALESCE(secret_key,''), source, week_percent, window_percent, resets_at
			FROM usage_readings WHERE `+where+` ORDER BY window_kind`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.window, &r.status, &r.key, &r.src, &r.week, &r.five, &r.resets); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	got := read(`org_id = $1 AND runtime_id = $2`, agent.OrgID, seat.RuntimeID)
	if len(got) != 1 {
		t.Fatalf("readings = %d, want one per window", len(got))
	}
	r := got[0]
	if r.src != observability.SourceInband || r.window != "seven_day" || r.status != "allowed" || r.key != "claude_code_oauth_token" {
		t.Errorf("reading: %+v", r)
	}
	if r.week == nil || *r.week != 13 || r.five != nil {
		t.Errorf("percent = %v (five-hour %v), want 13 in week_percent", r.week, r.five)
	}
	if want := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC); r.resets == nil || !r.resets.Equal(want) {
		t.Errorf("resets_at = %v, want %v", r.resets, want)
	}

	// Through a run: the line is filed as a runtime event, and its windows as
	// readings of the phase's credential.
	runner, rt := s.meterSeatAgent(t, "im-band-lauf")
	task, err := s.backlog.Create(ctx, s.orgID, runner.ID, "Lauf", "[mock:line "+twoWindows+"]\n[mock:result fertig]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
	got = read(`runtime_id = $1 AND source = 'inband'`, rt)
	if len(got) != 2 {
		t.Fatalf("in-band readings of the run = %d, want five_hour + seven_day", len(got))
	}
	if got[0].window != "five_hour" || got[0].five == nil || *got[0].five != 41 || got[0].week != nil || got[0].status != "allowed_warning" {
		t.Errorf("five-hour reading: %+v", got[0])
	}
	if got[1].window != "seven_day" || got[1].week == nil || *got[1].week != 13.5 || got[1].key != "mock_seat_token" {
		t.Errorf("weekly reading: %+v", got[1])
	}
}

// meterReading is one reading of a meterSteps case.
type meterReading struct {
	min    int // minutes after the window's start; negative: before it
	pct    float64
	stale  bool
	resets time.Time
	source string
	rt     uuid.UUID // zero: the case's runtime
	ord    int
}

// meterSteps against the real database, case by case. Every case is its own
// secret; the steps of a case are summed per source, the way a reader of one
// credential and one source would.
func TestMeterStepsCases(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("zaehler-faelle")
	since := time.Now().Add(-5 * time.Hour).Truncate(time.Hour)
	week1 := since.Add(48 * time.Hour)
	week2 := week1.Add(7 * 24 * time.Hour)
	other := uuid.New()

	cases := []struct {
		name     string
		key      string
		readings []meterReading
		want     map[string]float64 // source → points
	}{
		{"a lagging reading adds nothing", "lag", []meterReading{
			{min: -30, pct: 10}, {min: 10, pct: 9}, {min: 20, pct: 10}, {min: 30, pct: 11}},
			map[string]float64{"usage_cmd": 1}},
		{"a stale reading is dropped", "stale", []meterReading{
			{min: -30, pct: 30}, {min: 10, pct: 20, stale: true}, {min: 20, pct: 31}},
			map[string]float64{"usage_cmd": 1}},
		{"two credentials keep their own baselines (a)", "cred-a", []meterReading{
			{min: -120, pct: 10}, {min: 30, pct: 12}},
			map[string]float64{"usage_cmd": 2}},
		{"two credentials keep their own baselines (b)", "cred-b", []meterReading{
			{min: -60, pct: 20}, {min: 40, pct: 25}},
			map[string]float64{"usage_cmd": 5}},
		{"a reset by resets_at counts the new week from 0", "reset", []meterReading{
			{min: -30, pct: 95, resets: week1}, {min: 10, pct: 3, resets: week2}, {min: 20, pct: 5, resets: week2}},
			map[string]float64{"usage_cmd": 3 + 2}},
		{"a drop within the same week (resets_at unchanged) is a lag", "same-week", []meterReading{
			{min: -30, pct: 30, resets: week1}, {min: 10, pct: 14, resets: week1}, {min: 20, pct: 31, resets: week1}},
			map[string]float64{"usage_cmd": 1}},
		{"in-band 12 after /usage 13: no reset, nothing extra", "sources", []meterReading{
			{min: -30, pct: 10, resets: week1}, {min: 10, pct: 13, resets: week1},
			{min: 20, pct: 12, resets: week1, source: "inband"}},
			map[string]float64{"usage_cmd": 3, "inband": 0}},
		{"the same secret on two runtimes is one meter", "shared", []meterReading{
			{min: -30, pct: 10}, {min: 10, pct: 11, rt: other, ord: 2}, {min: 20, pct: 12}, {min: 30, pct: 12, rt: other, ord: 2}},
			map[string]float64{"usage_cmd": 2}},
		{"after a Reorder the history follows the secret", "reordered", []meterReading{
			{min: -30, pct: 10, ord: 0}, {min: 30, pct: 14, ord: 1}},
			map[string]float64{"usage_cmd": 4}},
		{"12 -> 3 without reset times is still a new week, counted as 3", "collapse", []meterReading{
			{min: -30, pct: 12}, {min: 30, pct: 3}},
			map[string]float64{"usage_cmd": 3}},
	}
	for _, c := range cases {
		rt := uuid.New()
		for _, r := range c.readings {
			cred := observability.CredentialRef{RuntimeID: rt, Ord: r.ord, SecretKey: c.key, Kind: "subscription"}
			if r.rt != uuid.Nil {
				cred.RuntimeID = r.rt
			}
			if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: cred,
				Source: r.source, Supported: true, WindowPercent: -1, WeekPercent: r.pct, Stale: r.stale,
				ResetsAt: r.resets, FetchedAt: since.Add(time.Duration(r.min) * time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
	}

	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "hour", since)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]float64{}
	for _, st := range cells.Subscription {
		if got[st.SecretKey] == nil {
			got[st.SecretKey] = map[string]float64{}
		}
		got[st.SecretKey][st.Source] += st.Points
	}
	for _, c := range cases {
		if fmt.Sprint(got[c.key]) != fmt.Sprint(c.want) {
			t.Errorf("%s: points per source %v, want %v", c.name, got[c.key], c.want)
		}
	}

	// The coverage says what the movement counts from and how sure it is.
	cov := map[string]observability.MeterCoverage{}
	for _, m := range cells.Meter {
		cov[m.SecretKey+"/"+m.Source] = m
	}
	if l := cov["lag/usage_cmd"]; !l.BaselineBefore || l.BaselinePercent != 10 || l.Points != 1 ||
		l.PointsLow != 0 || l.PointsHigh != 2 || l.LastPercent != 11 || l.Readings != 3 {
		t.Errorf("coverage of the lagging case: %+v", l)
	}
	// The baseline's week ended before the window's first reading: the window
	// spans one week, begun by a reset and counted from 0.
	if r := cov["reset/usage_cmd"]; len(r.Segments) != 1 || !r.Segments[0].Reset || r.Segments[0].From != 0 ||
		r.Segments[0].Points != 5 || r.Segments[0].To != 5 {
		t.Errorf("segments of the reset case: %+v", r.Segments)
	}
	// A collapse right after the baseline: one week in the window, 3 points,
	// known to ±1.
	if c := cov["collapse/usage_cmd"]; len(c.Segments) != 1 || c.PointsLow != 2 || c.PointsHigh != 4 {
		t.Errorf("coverage of the collapse case: %+v", c)
	}
	if b := cov["sources/inband"]; b.BaselineBefore || b.Readings != 1 {
		t.Errorf("the in-band source has no reading before the window: %+v", b)
	}
	if st := cells.Subscription[0]; st.USD != nil || st.UnpricedPoints != st.Points {
		t.Errorf("without a plan a step is unpriced: %+v", st)
	}
}

// The spend the meter's movement is divided into is the credential's lines
// between the baseline and the last reading, by their own timestamps: the
// same whatever bucket the chart is drawn in, including a line after the
// baseline but before the window (a cell would not hold it) and none outside.
func TestMeterMeasuredSpendIndependentOfBucket(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("zaehler-spanne")
	seat := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "spanne_token", Kind: "subscription"}
	other := observability.CredentialRef{RuntimeID: seat.RuntimeID, Ord: 1, SecretKey: "spanne_token", SecretSlot: 1, Kind: "subscription"}
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-24 * time.Hour)

	line := func(c observability.CredentialRef, at time.Time, outputTokens int) {
		t.Helper()
		task := uuid.New()
		l := fmt.Sprintf(`{"type":"result","session_id":"s-%s","modelUsage":{"claude-opus-5-5":{"costUSD":1,"outputTokens":%d}}}`, task, outputTokens)
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, c, json.RawMessage(l)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE model_usage SET created_at = $1 WHERE task_id = $2`, at, task); err != nil {
			t.Fatal(err)
		}
	}
	// Opus output is $20 per million: 250000 tokens are $5.
	line(seat, since.Add(-3*time.Hour), 250000) // before the baseline: out
	line(seat, since.Add(-1*time.Hour), 250000) // after it, before the window: in ($5)
	line(seat, since.Add(5*time.Hour), 125000)  // in ($2.50)
	line(other, since.Add(6*time.Hour), 250000) // another value: out
	line(seat, since.Add(19*time.Hour), 50000)  // in ($1)
	line(seat, since.Add(21*time.Hour), 250000) // after the last reading: out
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{since.Add(-2 * time.Hour), 9}, {since.Add(time.Hour), 12}, {since.Add(20 * time.Hour), 30}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: seat,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	for _, bucket := range []string{"hour", "day", "week", "month"} {
		cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, bucket, since)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, m := range cells.Meter {
			if m.SecretKey != "spanne_token" || m.SecretSlot != 0 {
				continue
			}
			found = true
			if !m.BaselineBefore || m.Points != 21 || math.Abs(m.MeasuredUSD-8.5) > 1e-9 {
				t.Errorf("bucket %s: baseline before %v, %v points, measured %v USD — want 21 points and 8.50 USD",
					bucket, m.BaselineBefore, m.Points, m.MeasuredUSD)
			}
		}
		if !found {
			t.Errorf("bucket %s: no coverage for the seat", bucket)
		}
	}
}

// A plan price is dated: a change mid-window prices the points before it at
// the old rate and the points after it at the new one, and PUT only ever
// appends — the row before is closed, never rewritten.
func TestCredentialPlanValidity(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("plan-preis")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	path := fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord)

	since := time.Now().Add(-5 * time.Hour).Truncate(time.Hour)
	change := since.Add(time.Hour)
	put := func(amount float64, from time.Time, status int) map[string]any {
		t.Helper()
		return admin.expect(http.MethodPut, path, map[string]any{"plan_name": "Max", "amount": amount,
			"currency": "USD", "period": "month", "tax_basis": "net", "valid_from": from}, status)
	}
	put(100, since.Add(-time.Hour), http.StatusOK)
	put(200, change, http.StatusOK)
	// Never overwritten: the same start again is a conflict, and nothing moved.
	put(999, change, http.StatusConflict)
	// What cannot be priced is refused.
	admin.expect(http.MethodPut, path, map[string]any{"amount": 10, "period": "week"}, http.StatusBadRequest)
	admin.expect(http.MethodPut, path, map[string]any{"period": "month"}, http.StatusBadRequest)
	admin.expect(http.MethodPut, path, map[string]any{"amount": 10, "period": "month", "currency": "EUR"}, http.StatusBadRequest)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/9/plan", rt.ID),
		map[string]any{"amount": 10, "period": "month"}, http.StatusNotFound)
	// A price entered after the fact for an earlier period ends where the
	// next one begins.
	put(50, since.Add(-3*time.Hour), http.StatusOK)

	plans, err := s.runtimes.Plans(ctx, s.orgID, rt.ID, ord)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 {
		t.Fatalf("plans = %d, want 3 rows", len(plans))
	}
	if plans[0].Amount != 200 || plans[0].ValidTo != nil || !plans[0].ValidFrom.Equal(change) {
		t.Errorf("current plan: %+v", plans[0])
	}
	if plans[1].Amount != 100 || plans[1].ValidTo == nil || !plans[1].ValidTo.Equal(change) {
		t.Errorf("the plan before the change was not closed at it: %+v", plans[1])
	}
	if plans[2].Amount != 50 || plans[2].ValidTo == nil || !plans[2].ValidTo.Equal(plans[1].ValidFrom) {
		t.Errorf("the backdated plan: %+v", plans[2])
	}
	if got := admin.expectList(http.MethodGet, path, nil, http.StatusOK); len(got) != 3 {
		t.Errorf("GET plan: %d rows", len(got))
	}

	// The meter: 10 before the window, 12 before the change, 15 after it.
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{since.Add(-30 * time.Minute), 10}, {since.Add(30 * time.Minute), 12}, {change.Add(30 * time.Minute), 15}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "hour", since)
	if err != nil {
		t.Fatal(err)
	}
	perPoint := func(monthly float64) float64 { return monthly * 12 / 52 / 100 }
	byPeriod := map[time.Time]observability.MeterStep{}
	for _, st := range cells.Subscription {
		byPeriod[st.Period] = st
	}
	before, after := byPeriod[since.UTC()], byPeriod[change.UTC()]
	if before.Points != 2 || before.USD == nil || math.Abs(*before.USD-2*perPoint(100)) > 1e-9 {
		t.Errorf("before the change: %v points, %v USD — want 2 at the old rate (%v)", before.Points, before.USD, 2*perPoint(100))
	}
	if after.Points != 3 || after.USD == nil || math.Abs(*after.USD-3*perPoint(200)) > 1e-9 {
		t.Errorf("after the change: %v points, %v USD — want 3 at the new rate (%v)", after.Points, after.USD, 3*perPoint(200))
	}
	if len(cells.Plans) != 2 {
		t.Errorf("plans valid in the window = %d, want the two around the change", len(cells.Plans))
	}
	for _, p := range cells.Plans {
		if math.Abs(p.USDPerPoint-perPoint(p.Amount)) > 1e-12 {
			t.Errorf("usd_per_point of %v/month = %v", p.Amount, p.USDPerPoint)
		}
	}
	got := admin.expect(http.MethodGet, "/api/v1/cost/cells?days=1&bucket=hour", nil, http.StatusOK)
	for _, k := range []string{"subscription", "meter", "plans"} {
		if _, ok := got[k].([]any); !ok {
			t.Errorf("GET /cost/cells lacks %q", k)
		}
	}
}
