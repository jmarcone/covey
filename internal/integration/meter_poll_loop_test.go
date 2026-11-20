package integration

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// meterPollLoopToken is the seat's value: a subscription OAuth token by its
// prefix, so the poll reads it.
const meterPollLoopToken = "sk-ant-oat01-meter-poll-loop"

// meterPollLoopRunner stands in for the claude CLI: one call's stream-json,
// a rate_limit_event and a result line over two models among it.
type meterPollLoopRunner struct {
	mu     sync.Mutex
	tokens []string
}

func (r *meterPollLoopRunner) Run(_ context.Context, token string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, token)
	return []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"poll-s1","model":"claude-haiku-4-5-20251001"}`,
		`{"type":"assistant","session_id":"poll-s1","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"rate_limit_event","session_id":"poll-s1","rate_limit_info":{"status":"allowed_warning","unifiedWindows":{` +
			`"five_hour":{"status":"allowed_warning","resetsAt":1795100000,"utilization":0.41},` +
			`"seven_day":{"status":"allowed","resetsAt":1795190400,"utilization":0.135}}}}`,
		`not json`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"poll-s1","modelUsage":{` +
			`"claude-haiku-4-5-20251001":{"inputTokens":3,"outputTokens":5,"cacheReadInputTokens":0,"cacheCreationInputTokens":9800,"costUSD":0.0123},` +
			`"claude-haiku-5-5":{"inputTokens":40,"outputTokens":2,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.0008}}}`,
	}, "\n") + "\n"), nil
}

func (r *meterPollLoopRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}

// #153 end to end: a subscription seat no run reads, an outside source linked
// to it that pushed a moment ago. One round of PollMeters reads the seat
// through the runner, books the call under "meter poll: <secret>#<slot>"
// linked to the seat, and files the event's windows as in-band readings. A
// second round right after reads nothing: the seat has a fresh reading, and
// this process tried it a moment ago.
func TestMeterPollLoopReadsAnIdleSeat(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	const key = "claude_code_oauth_token"
	ord := s.seat(t, rt.ID, daemon.CredSubscription, key, meterPollLoopToken, "A")
	var slot int
	if err := s.pool.QueryRow(ctx, `SELECT secret_slot FROM runtime_credentials WHERE runtime_id=$1 AND ord=$2`,
		rt.ID, ord).Scan(&slot); err != nil {
		t.Fatal(err)
	}

	// The outside source: one call a few minutes ago, linked to the seat.
	up := extUpload("operator: home", extRow("o1", time.Now().Add(-5*time.Minute), "claude-opus-5-5", 1000))
	up["meter_credential"] = map[string]any{"secret_key": key, "secret_slot": slot}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	runner := &meterPollLoopRunner{}
	s.orch.MeterPoll = runner
	s.orch.PollMeters(ctx)

	if runner.calls() != 1 || runner.tokens[0] != meterPollLoopToken {
		t.Fatalf("the runner was called %d times (%q)", runner.calls(), runner.tokens)
	}

	readings := func() int {
		t.Helper()
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_readings
			WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND source = 'inband'`,
			s.orgID, key, slot).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := readings(); n == 0 {
		t.Fatal("no in-band reading was filed")
	}
	var week float64
	var supported bool
	if err := s.pool.QueryRow(ctx, `SELECT week_percent, supported FROM usage_readings
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND source = 'inband' AND window_kind = 'seven_day'`,
		s.orgID, key, slot).Scan(&week, &supported); err != nil {
		t.Fatalf("the weekly window was not filed: %v", err)
	}
	if !supported || math.Abs(week-13.5) > 1e-9 {
		t.Errorf("seven_day = %v %% (supported %v), want 13.5", week, supported)
	}

	source := observability.MeterPollSourceFor(key, slot)
	if !strings.HasPrefix(source, "meter poll: ") {
		t.Fatalf("source %q", source)
	}
	booked := func() int {
		t.Helper()
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM external_usage
			WHERE org_id = $1 AND source = $2 AND host = $3`, s.orgID, source, observability.MeterPollHost).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := booked(); n != 2 {
		t.Errorf("%d rows booked under %q, want one per model", n, source)
	}
	var linkKey string
	var linkSlot int
	if err := s.pool.QueryRow(ctx, `SELECT secret_key, secret_slot FROM external_usage_meters
		WHERE org_id = $1 AND source = $2`, s.orgID, source).Scan(&linkKey, &linkSlot); err != nil {
		t.Fatalf("the poll source is not linked: %v", err)
	}
	if linkKey != key || linkSlot != slot {
		t.Errorf("linked to %s#%d", linkKey, linkSlot)
	}

	before := readings()
	s.orch.PollMeters(ctx)
	if runner.calls() != 1 {
		t.Errorf("the second round called the runner again")
	}
	if n := readings(); n != before {
		t.Errorf("the second round filed %d readings more", n-before)
	}
	if n := booked(); n != 2 {
		t.Errorf("%d rows booked after the second round", n)
	}
}
