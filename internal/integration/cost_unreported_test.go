package integration

import (
	"context"
	"encoding/json"
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

// A killed run whose second line failed to file: the run's end files it once
// more (FileFailedLines, called by bookUnreported), against the run's
// credential, and books it with the first line as one unreported entry (#113).
// The credential is what the per-credential limit and the capacity steering
// read, so the refiled rows carry the one the run went out on.
func TestKilledRunFilesItsFailedLines(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent, rt := s.meterSeatAgent(t, "abgebrochen-nachgereicht")
	sid := uuid.NewString()
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Lange Aufgabe",
		"[mock:line "+session[0].json(sid, 0, true)+"][mock:sleep 30s]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run's first line is filed", 15*time.Second, func() bool {
		var n int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1`, task.ID).Scan(&n)
		return n == 2
	})
	// What RecordRuntime leaves where filing the line's rows failed: the line
	// in the recording, no rows.
	if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task.ID, observability.KindRuntime,
		json.RawMessage(session[1].json(sid, 1, true))); err != nil {
		t.Fatal(err)
	}
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
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM cost_entries WHERE task_id=$1`, task.ID).Scan(&entries)
		return entries > 0
	})
	// Give a late second booking the chance to show up.
	time.Sleep(500 * time.Millisecond)
	_ = s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(usd),0)::float8, COALESCE(MAX(basis),'')
		FROM cost_entries WHERE task_id=$1`, task.ID).Scan(&entries, &usd, &basis)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE task_id=$1 AND cost_entry_id IS NULL`, task.ID).Scan(&unlinked)
	want := 1.802961 + 0.014744 + 0.732072
	if entries != 1 || basis != observability.BasisUnreported || math.Abs(usd-want) > 1e-6 || unlinked != 0 {
		t.Fatalf("%d entries of basis %q, %v booked, %d rows unlinked — want one unreported entry of both lines' %v",
			entries, basis, usd, unlinked, want)
	}
	var rows, seats, onSeat int
	if err := s.pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT (runtime_id, credential_ord)),
			count(*) FILTER (WHERE runtime_id = $2 AND credential_ord IS NOT NULL)
		FROM model_usage WHERE task_id=$1`, task.ID, rt).Scan(&rows, &seats, &onSeat); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || seats != 1 || onSeat != 3 {
		t.Errorf("%d rows on %d credentials, %d of them on the run's seat — want the refiled row on the first line's seat too",
			rows, seats, onSeat)
	}
}
