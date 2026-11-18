package integration

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"

	"covey/internal/observability"
)

// One task books more than one run: the agent's own and a sub-run it started,
// each a session of its own, each with its own cost message. Each booking has
// to take its own run's lines — in either order, from a daemon that sends the
// run's spend and from one whose figure has to be recomputed from the lines.
func TestBookRunTwiceForOneTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	none := observability.CredentialRef{}
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }

	for _, mode := range []struct {
		name      string
		fromLines bool
		basis     string
	}{
		{"daemon sends the run's spend", false, observability.BasisInvocation},
		{"daemon from before the cost basis", true, observability.BasisFromDeltas},
	} {
		t.Run(mode.name, func(t *testing.T) {
			agent := s.newSupportAgent("zwei-laeufe-" + map[bool]string{false: "neu", true: "alt"}[mode.fromLines])
			task := uuid.New()
			parent, sub := uuid.NewString(), uuid.NewString()
			parentLine := recordedLine{0, 1000, 1.0, map[string]ccUsage{opus: at(1.0, 900)}, map[string]ccUsage{opus: at(1.0, 900)}}
			subLine := recordedLine{0, 1000, 0.25, map[string]ccUsage{opus: at(0.25, 300)}, map[string]ccUsage{opus: at(0.25, 300)}}
			for _, l := range []struct {
				sid  string
				line recordedLine
			}{{parent, parentLine}, {sub, subLine}} {
				if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, json.RawMessage(l.line.json(l.sid, 0, true))); err != nil {
					t.Fatal(err)
				}
			}
			book := func(sid string, usd float64) observability.Booked {
				t.Helper()
				basis := observability.BasisInvocation
				if mode.fromLines {
					// An old daemon: no basis, and a figure that is not the run's own.
					basis, usd = "", usd*10
				}
				b, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: usd,
					Basis: basis, SessionID: sid}, mode.fromLines)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			// The sub-run reports first (it ends while the agent's run goes on).
			bs := book(sub, 0.25)
			bp := book(parent, 1.0)
			if bs.Lines != 1 || math.Abs(bs.LinesUSD-0.25) > 1e-9 || bp.Lines != 1 || math.Abs(bp.LinesUSD-1.0) > 1e-9 {
				t.Errorf("each booking links its own run's line: sub %+v, parent %+v", bs, bp)
			}
			if math.Abs(bs.USD-0.25) > 1e-9 || math.Abs(bp.USD-1.0) > 1e-9 || bs.Basis != mode.basis || bp.Basis != mode.basis {
				t.Errorf("booked sub %v (%s), parent %v (%s) — want 0.25 and 1.0 (%s)", bs.USD, bs.Basis, bp.USD, bp.Basis, mode.basis)
			}
			var booked float64
			_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(usd),0)::float8 FROM cost_entries WHERE task_id=$1`, task).Scan(&booked)
			if math.Abs(booked-1.25) > 1e-9 {
				t.Errorf("the task booked %v, want 1.25", booked)
			}
		})
	}
}
