package integration

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/observability"
)

// A run killed after its result line: the daemon's cost message never
// arrives (the control plane stops reading the moment it kills), but the line
// was filed. Its spend has to be booked once the task's loop ends — from the
// line, as a booking of its own kind — and not again by anything after.
func TestKilledRunBooksItsLines(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("abgebrochen")
	sid := uuid.NewString()
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Lange Aufgabe",
		"[mock:line "+session[0].json(sid, 0, true)+"][mock:sleep 30s]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run's line is filed", 15*time.Second, func() bool {
		var n int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1`, task.ID).Scan(&n)
		return n == 2
	})
	if err := s.orch.Kill(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	var (
		entries  int
		usd      float64
		basis    string
		unlinked int
	)
	waitFor(t, "the killed run's lines are booked", 10*time.Second, func() bool {
		_ = s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(usd),0)::float8, COALESCE(MAX(basis),'')
			FROM cost_entries WHERE task_id=$1`, task.ID).Scan(&entries, &usd, &basis)
		return entries > 0
	})
	// Give a late second booking the chance to show up.
	time.Sleep(500 * time.Millisecond)
	_ = s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(usd),0)::float8, COALESCE(MAX(basis),'')
		FROM cost_entries WHERE task_id=$1`, task.ID).Scan(&entries, &usd, &basis)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1 AND cost_entry_id IS NULL`, task.ID).Scan(&unlinked)
	if entries != 1 || basis != observability.BasisUnreported || math.Abs(usd-(1.802961+0.014744)) > 1e-6 || unlinked != 0 {
		t.Fatalf("%d entries of basis %q, %v booked, %d lines unlinked — want one unreported entry of the line's 1.817705",
			entries, basis, usd, unlinked)
	}
}
