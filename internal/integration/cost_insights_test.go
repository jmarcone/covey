package integration

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/observability"
)

// A Claude Code `result` line as it arrives from the daemon: an Opus run that
// also made small Haiku calls, cache writes split by lifetime (shape and
// numbers from a real run).
const resultLine = `{"type":"result","subtype":"success","session_id":"sess-1","num_turns":31,
 "total_cost_usd":1.3991645,
 "modelUsage":{
  "claude-opus-5-5":{"costUSD":1.3809534,"inputTokens":60,"outputTokens":26305,"thinkingTokens":19448,
    "cacheReadInputTokens":1356027,"cacheCreationInputTokens":72926,
    "cacheCreationEphemeral1hInputTokens":72926,"cacheCreationEphemeral5mInputTokens":0,"webSearchRequests":0,"costBasis":"list"},
  "claude-haiku-5-5":{"costUSD":0.0182111,"inputTokens":42576,"outputTokens":7907,"thinkingTokens":4793,
    "cacheReadInputTokens":0,"cacheCreationInputTokens":0,
    "cacheCreationEphemeral1hInputTokens":0,"cacheCreationEphemeral5mInputTokens":0,"webSearchRequests":1,"costBasis":"list"}}}`

// The split per model only ever lived in the recording, which is pruned. Filing
// a result line must leave one row per model — once, however often the same
// event were filed — and the report must price it with the list so that a
// drift between engine and list shows up.
func TestModelUsageFromResultLine(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("modell-verbrauch")
	task := uuid.New()

	// An ordinary transcript line files nothing per model.
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, json.RawMessage(`{"type":"assistant","message":{"model":"claude-opus-5-5"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, json.RawMessage(resultLine)); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1`, task).Scan(&n)
	if n != 2 {
		t.Fatalf("model_usage rows = %d, want 2 (opus + haiku)", n)
	}
	// The recording keeps the line as before.
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM recording_events WHERE task_id=$1 AND kind='runtime'`, task).Scan(&n)
	if n != 2 {
		t.Fatalf("recording events = %d, want 2", n)
	}

	rep, err := s.obs.ModelUsage(ctx, agent.OrgID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]observability.ModelUsageRow{}
	for _, r := range rep.Rows {
		byModel[r.Model] = r
	}
	opus := byModel["claude-opus-5-5"]
	if opus.CacheWrite1h != 72926 || opus.CacheRead != 1356027 || opus.Thinking != 19448 {
		t.Errorf("opus tokens not filed as reported: %+v", opus.ModelTokens)
	}
	// The list reproduces the engine's own price (1-hour cache writes at 2x input).
	if opus.ListUSD == nil || math.Abs(*opus.ListUSD-opus.EngineUSD) > 0.0001 {
		t.Errorf("opus list price %v vs engine %v", opus.ListUSD, opus.EngineUSD)
	}
	haiku := byModel["claude-haiku-5-5"]
	if haiku.WebSearches != 1 || haiku.ListUSD == nil {
		t.Errorf("haiku row incomplete: %+v", haiku)
	}

	// Over the API, same window rules as the other cost endpoints.
	got := admin.expect(http.MethodGet, "/api/v1/cost/models?days=1", nil, http.StatusOK)
	if rows, _ := got["rows"].([]any); len(rows) != 2 {
		t.Errorf("GET /cost/models rows = %v, want 2", got["rows"])
	}
}

// The chart's cells carry both figures for the same runs: the booked (reported)
// cost and the measured one split by cost type — and a booking without a
// breakdown shows up as "unattributed" rather than disappearing.
func TestCostCellsReportedAndMeasured(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("kosten-zellen")
	task := uuid.New()

	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, json.RawMessage(resultLine)); err != nil {
		t.Fatal(err)
	}
	// What covey books for that run, plus a second run without any breakdown.
	if err := s.obs.AddCost(ctx, agent.ID, &task, 1.3991645, observability.Tokens{Output: 34212}, "claude-opus-5-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	if err := s.obs.AddCost(ctx, agent.ID, &other, 2.5, observability.Tokens{Output: 10}, "claude-opus-5-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}

	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "day", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cells.Cells) != 1 || len(cells.Agents) != 1 {
		t.Fatalf("cells = %d, agents = %d, want 1/1", len(cells.Cells), len(cells.Agents))
	}
	c := cells.Cells[0]
	if math.Abs(c.Reported-3.8991645) > 1e-6 {
		t.Errorf("reported = %v, want 3.8991645", c.Reported)
	}
	measured := 0.0
	for _, v := range c.Measured {
		measured += v
	}
	// Measured covers the same runs: the breakdown (≈1.399) plus the unattributed 2.5.
	if math.Abs(measured-c.Reported) > 0.001 {
		t.Errorf("measured %v vs reported %v (parts %v)", measured, c.Reported, c.Measured)
	}
	if math.Abs(c.Measured[observability.TypeUnattributed]-2.5) > 0.001 {
		t.Errorf("unattributed = %v, want 2.5", c.Measured[observability.TypeUnattributed])
	}
	if c.Measured[observability.TypeCacheWrite] <= c.Measured[observability.TypeCacheRead] {
		t.Errorf("for this run the 1-hour cache writes cost more than the cache reads: %v", c.Measured)
	}
	admin.expect(http.MethodGet, "/api/v1/cost/cells?days=1&bucket=hour", nil, http.StatusOK)
}

// A session resumed for a handover reports its usage cumulatively in a second
// result line: only the session's LAST line counts, or the run is counted twice.
// And the meter's movement per period becomes the third figure in the cells.
func TestCumulativeSessionAndMeterSteps(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("kumuliert")
	task := uuid.New()
	first := `{"type":"result","session_id":"same","modelUsage":{"claude-opus-5-5":{"costUSD":5.0,"outputTokens":250000}}}`
	again := `{"type":"result","session_id":"same","modelUsage":{"claude-opus-5-5":{"costUSD":5.4,"outputTokens":270000}}}`
	for _, l := range []string{first, again} {
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, json.RawMessage(l)); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.obs.ModelUsage(ctx, agent.OrgID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 || math.Abs(rep.Rows[0].EngineUSD-5.4) > 1e-9 || rep.Rows[0].Output != 270000 {
		t.Fatalf("want only the session's last line (5.4 USD, 270000 out), got %+v", rep.Rows)
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
