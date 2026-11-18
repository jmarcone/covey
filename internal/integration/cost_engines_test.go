package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/observability"
)

// mockPlainEngine stands in for an engine whose result lines look like Claude
// Code's but whose usage covey does not price — educa-ai drives the same
// binary against its own gateway. It declares no ResultUsage.
const mockPlainEngine = "mock-plain"

type mockPlain struct{ daemon.Mock }

func (mockPlain) Name() string { return mockPlainEngine }

func init() {
	daemon.RegisterRuntime(daemon.RuntimeDescriptor{
		Name: mockPlainEngine, Label: "Mock (no per-model usage)",
		Description:  "The mock engine without per-model result usage, for the cost tests.",
		Capabilities: daemon.RuntimeCapabilities{Resume: true, SkillsDir: ".claude/skills"},
		New:          func() daemon.Runtime { return mockPlain{} },
	})
}

// A result line of an engine that does not declare per-model usage is filed
// in the recording, but leaves no model_usage row: its models are not on the
// price list, and the rows would land in the measured figures as unpriced
// spend beside a booking that never links them. The run is booked as sent.
func TestModelUsageOnlyForEnginesThatReportIt(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent, err := s.registry.Create(ctx, s.orgID, "educa-artig", "Gateway-Agent", mockPlainEngine, &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.registry.SaveConfig(ctx, agent.ID, map[string]string{
		"SOUL.md":   "# Gateway-Agent\n\n## Role\nSupport.",
		"ACCESS.md": "- system: zammad scope: read",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}
	line := session[0].json(uuid.NewString(), 0, true)
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Lauf",
		"[mock:line "+line+`][mock:cost {"usd":1.81,"basis":"invocation"}]`, "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })

	var rows, lines int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1`, task.ID).Scan(&rows)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM recording_events WHERE task_id=$1 AND kind='runtime' AND payload->>'type'='result'`,
		task.ID).Scan(&lines)
	if rows != 0 || lines != 1 {
		t.Fatalf("%d model_usage rows, %d result lines in the recording — want none and the one line", rows, lines)
	}
	var (
		usd   float64
		basis string
	)
	if err := s.pool.QueryRow(ctx, `SELECT usd::float8, basis FROM cost_entries WHERE task_id=$1`, task.ID).Scan(&usd, &basis); err != nil {
		t.Fatal(err)
	}
	if usd != 1.81 || basis != observability.BasisInvocation {
		t.Errorf("booked %v (%s), want the 1.81 as sent", usd, basis)
	}
}
