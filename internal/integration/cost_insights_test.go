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

	"covey/internal/backlog"
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
	admin.expect(http.MethodGet, "/api/v1/cost/cells?days=1&bucket=hour", nil, http.StatusOK)

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

	rt := uuid.New()
	base := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	for _, r := range []struct {
		at  time.Duration
		pct float64
	}{{0, 9}, {30 * time.Minute, 10}, {70 * time.Minute, 12}, {100 * time.Minute, 3}} {
		if err := s.obs.AddUsageReading(ctx, agent.OrgID, rt, 0, true, 1, r.pct, "", "", false, base.Add(r.at)); err != nil {
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
	// 9 → 10 in the first hour; 10 → 12, then a reset to 3 (counted as 3) in the second.
	if got[base.UTC().Format("15")] != 1 || got[base.Add(time.Hour).UTC().Format("15")] != 5 {
		t.Errorf("meter steps per hour = %v, want 1 then 5", got)
	}
}

// The provider's answer is kept as history, and each reading carries the
// list-price cost booked up to it — the meter read against the bill it would be.
func TestUsageReadingsCarryTheCostSoFar(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("abo-zaehler")
	rt := uuid.New()

	if err := s.obs.AddUsageReading(ctx, agent.OrgID, rt, 0, true, 3, 9, "", "Nov 20", false, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddCost(ctx, agent.ID, nil, 30, observability.Tokens{Output: 10}, "claude-opus-5-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddUsageReading(ctx, agent.OrgID, rt, 0, true, 5, 10, "", "Nov 20", false, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// An unsupported answer is kept but not shown as a reading.
	if err := s.obs.AddUsageReading(ctx, agent.OrgID, rt, 0, false, -1, -1, "", "", false, time.Now()); err != nil {
		t.Fatal(err)
	}

	rs, err := s.obs.UsageReadings(ctx, agent.OrgID, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("readings = %d, want 2 supported ones", len(rs))
	}
	if rs[0].CostUSD != 0 || rs[1].CostUSD != 30 {
		t.Errorf("cost so far = %v / %v, want 0 / 30", rs[0].CostUSD, rs[1].CostUSD)
	}
	if rs[1].WeekPercent == nil || *rs[1].WeekPercent != 10 {
		t.Errorf("week percent = %v, want 10", rs[1].WeekPercent)
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
