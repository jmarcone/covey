package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/benjaminLedel/covey-plugin-sdk/target"
)

// killedAfterResult is a run the kill reaches after its result: the kill
// cancels the run's context, and the run returns what it spent with the
// context's error — as ClaudeCode.Run does.
type killedAfterResult struct{ c *Client }

func (killedAfterResult) Name() string { return "killed-after-result" }

func (r killedAfterResult) Run(ctx context.Context, _ RunSpec, _ func(string, json.RawMessage)) (RunResult, error) {
	r.c.mu.Lock()
	cancel := r.c.cancelRun
	r.c.mu.Unlock()
	cancel()
	<-ctx.Done()
	return RunResult{Status: "incomplete", SessionID: "sess-k", CostUSD: 0.42, OutputTokens: 900,
		Model: "claude-opus-5-5", Basis: CostBasisInvocation}, ctx.Err()
}

// standInControlPlane answers every request the daemon makes with a refusal
// and hands on the cost messages and runtime events that arrive.
func standInControlPlane(t *testing.T, c *Client) (costs <-chan Cost, events <-chan Event) {
	t.Helper()
	sent := captureSends(t, c)
	costCh, eventCh := make(chan Cost, 16), make(chan Event, 256)
	go func() {
		for m := range sent {
			switch m.Type {
			case TypeCost:
				if cost, err := DecodePayload[Cost](m); err == nil {
					costCh <- cost
				}
				continue
			case TypeEvent:
				if ev, err := DecodePayload[Event](m); err == nil {
					eventCh <- ev
				}
				continue
			}
			var req struct {
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(m.Payload, &req) != nil || req.RequestID == "" {
				continue
			}
			c.mu.Lock()
			ch := c.pending[req.RequestID]
			c.mu.Unlock()
			if ch != nil {
				ch <- Message{Type: "answer", Payload: json.RawMessage(`{"request_id":"` + req.RequestID + `","ok":false}`)}
			}
		}
	}()
	return costCh, eventCh
}

func expectCost(t *testing.T, costs <-chan Cost, what string) Cost {
	t.Helper()
	select {
	case c := <-costs:
		return c
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no cost message was sent", what)
		return Cost{}
	}
}

// A run cancelled after its main result — the kill switch, a budget pause —
// has spent that money. The daemon used to return on the cancelled context
// before it reported the cost, so the run booked nothing.
func TestKilledRunStillReportsItsCost(t *testing.T) {
	c := &Client{homeDir: t.TempDir(), creds: map[string]InjectCredentials{},
		credsFetched: map[string]time.Time{}, pending: map[string]chan Message{},
		subAgentDirs: map[string]bool{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: InjectConfig{Runtime: "killed-after-result"}}
	c.runtimes = map[string]Runtime{"killed-after-result": killedAfterResult{c}}
	costs, _ := standInControlPlane(t, c)

	c.runTask(context.Background(), AssignTask{TaskID: "task-k", Title: "Long job"})

	cost := expectCost(t, costs, "a run killed after its result")
	if cost.TaskID != "task-k" || !near(cost.USD, 0.42) || cost.OutputTokens != 900 || cost.SessionID != "sess-k" {
		t.Fatalf("the killed run's spend must be reported as it stood: %+v", cost)
	}
}

// The same for a sub-run whose request is cancelled after its result line:
// the process is killed, the line's share is still the sub-run's spend.
func TestCancelledSubAgentStillReportsItsCost(t *testing.T) {
	bin, home := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"sub-s","result":"half","total_cost_usd":0.5,"model_usage_since_last_result":{"claude-opus-5-5":{"costUSD":0.5,"outputTokens":800}}}
EOF
exec sleep 30`)
	c := &Client{homeDir: home, runtimes: map[string]Runtime{"claude-code": &ClaudeCode{Binary: bin}},
		creds: map[string]InjectCredentials{}, subAgentDirs: map[string]bool{}, cfg: InjectConfig{Runtime: "claude-code"}}
	costs, events := standInControlPlane(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ev := range events {
			if strings.Contains(string(ev.Payload), `"type":"result"`) {
				cancel()
				return
			}
		}
	}()
	if _, err := c.runSubAgent(ctx, "task-s", target.SubAgentRequest{Dir: home, Task: "Fix it"}); err == nil {
		t.Fatal("a cancelled sub-run returns the cancellation")
	}
	cost := expectCost(t, costs, "a cancelled sub-run")
	if cost.TaskID != "task-s" || !near(cost.USD, 0.5) || cost.OutputTokens != 800 || cost.SessionID != "sub-s" {
		t.Fatalf("the cancelled sub-run's spend must be reported: %+v", cost)
	}
}
