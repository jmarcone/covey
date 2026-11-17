package integration

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/orchestrator"
)

/* A wake that arrived while an agent was `securing` was lost. The session
   stays registered until its sandbox is down and its home is in the store,
   EnsureRunning found it and returned, and the agent went to sleep with work
   in its backlog — POST /agents/{id}/wake had answered ok all the same. Only
   the next tick picked the work up again.

   The tests below run with a tick far beyond their own runtime, so that a
   wake has to arrive by itself or not at all. */

// holdingProvider is the in-process sandbox, except that stopping it waits
// until the test lets go. That is the window in question: the status says
// `securing`, the session is still registered, and the test can do whatever
// a person or a colleague would do in it.
type holdingProvider struct {
	inner    *inprocProvider
	starts   atomic.Int32
	stopping chan struct{}
	release  chan struct{}
}

func newHoldingProvider() *holdingProvider {
	return &holdingProvider{stopping: make(chan struct{}, 4), release: make(chan struct{})}
}

func (p *holdingProvider) wrap(homeBase string, log *slog.Logger) orchestrator.SandboxProvider {
	p.inner = &inprocProvider{homeBase: homeBase, log: log}
	return p
}

func (p *holdingProvider) Start(ctx context.Context, spec orchestrator.SandboxSpec) (orchestrator.Sandbox, error) {
	p.starts.Add(1)
	sb, err := p.inner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &holdingSandbox{inner: sb, p: p}, nil
}

type holdingSandbox struct {
	inner orchestrator.Sandbox
	p     *holdingProvider
}

func (s *holdingSandbox) Stop(ctx context.Context) error {
	s.p.stopping <- struct{}{}
	select {
	case <-s.p.release:
	case <-ctx.Done():
	}
	return s.inner.Stop(context.Background())
}

// securingWindow brings an agent through one task into `securing` and holds
// it there.
func securingWindow(t *testing.T, s *stack, hold *holdingProvider, slug string) agents.Agent {
	t.Helper()
	ctx := context.Background()
	agent := s.newSupportAgent(slug)
	first, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Erste Aufgabe",
		"[mock:result erledigt]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-hold.stopping:
	case <-time.After(40 * time.Second):
		t.Fatalf("the sandbox never went down; task %s", s.taskState(first.ID))
	}
	if st := s.agentStatus(agent.ID); st != "securing" {
		t.Fatalf("status while the sandbox goes down: %q, expected securing", st)
	}
	if st := s.taskState(first.ID); st != backlog.StateDone {
		t.Fatalf("first task: %q, expected done", st)
	}
	return agent
}

func TestAWakeDuringSecuringStartsTheNextSession(t *testing.T) {
	ctx := context.Background()
	hold := newHoldingProvider()
	s := newStackWith(t, stackOpts{provider: hold.wrap, tickInterval: time.Hour})
	agent := securingWindow(t, s, hold, "wake-securing")

	// A colleague hands over work, and a person presses "wake" — both while
	// the sandbox is still going down.
	second, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Zweite Aufgabe",
		"[mock:result auch erledigt]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	s.orch.WakeNow(agent.ID)
	close(hold.release)

	waitForOr(t, "the task handed over during securing is not picked up", 30*time.Second, func() bool {
		return s.taskState(second.ID) == backlog.StateDone
	}, func() string { return s.recordingSummary(agent.ID) })
	if n := hold.starts.Load(); n != 2 {
		t.Errorf("%d sandboxes started, expected 2 — one per waking phase", n)
	}
	waitFor(t, "the agent does not come to rest", 20*time.Second, func() bool {
		return s.agentStatus(agent.ID) == "sleeping"
	})
}

// A killed agent is not woken by what was asked for before the kill.
func TestAKilledAgentIsNotWokenAfterSecuring(t *testing.T) {
	ctx := context.Background()
	hold := newHoldingProvider()
	s := newStackWith(t, stackOpts{provider: hold.wrap, tickInterval: time.Hour})
	agent := securingWindow(t, s, hold, "kill-securing")

	second, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Zweite Aufgabe",
		"[mock:result erledigt]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	s.orch.WakeNow(agent.ID)
	if err := s.orch.Kill(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	close(hold.release)

	settled(t, s, agent)
	if n := hold.starts.Load(); n != 1 {
		t.Errorf("%d sandboxes started after a kill, expected 1", n)
	}
	if st := s.taskState(second.ID); st != backlog.StateOpen {
		t.Errorf("the task of a killed agent is %q, expected it to wait open", st)
	}
}

// A wake that arrives while the control plane shuts down starts nothing: the
// process is waiting for its own work to stop, not for more of it.
func TestNoWakeAfterSecuringDuringShutdown(t *testing.T) {
	ctx := context.Background()
	hold := newHoldingProvider()
	s := newStackWith(t, stackOpts{provider: hold.wrap, tickInterval: time.Hour})
	agent := securingWindow(t, s, hold, "shutdown-securing")

	if _, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Zweite Aufgabe",
		"[mock:result erledigt]", "manual", 3); err != nil {
		t.Fatal(err)
	}
	s.orch.WakeNow(agent.ID)
	s.cancel()
	close(hold.release)

	settled(t, s, agent)
	if n := hold.starts.Load(); n != 1 {
		t.Errorf("%d sandboxes started during the shutdown, expected 1", n)
	}
}

// settled waits until the held session has ended and gives a wrongly started
// next one the time to show itself.
func settled(t *testing.T, s *stack, agent agents.Agent) {
	t.Helper()
	waitFor(t, "the session does not end", 20*time.Second, func() bool {
		return s.agentStatus(agent.ID) != "securing"
	})
	time.Sleep(time.Second)
}
