package integration

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/orchestrator"

	"github.com/google/uuid"
)

/* A wake that arrived while an agent was `securing` was lost. The session
   stays registered until its sandbox is down and its home is in the store,
   EnsureRunning found it and returned, and the agent went to sleep with work
   in its backlog — POST /agents/{id}/wake had answered ok all the same. Only
   the next tick picked the work up again.

   The tests below run with a tick far beyond their own runtime, so that a
   wake has to arrive by itself or not at all. Where they prove that nothing
   started, they wait for the orchestrator's "session ended" line, which is
   written after the end of a session has started whatever it starts — not
   for a pause. */

// holdingProvider is the in-process sandbox with two handles for the test:
// stopping the sandbox of a held agent waits until the test lets go (the
// `securing` window), and a start can be held and then fail (a wake that is
// in flight when a person presses Wake).
type holdingProvider struct {
	inner *inprocProvider

	mu      sync.Mutex
	held    map[string]bool // agent ids whose Stop waits for release
	started map[string]int
	last    map[string]*holdingSandbox
	// failStart: the next start of any agent signals startHeld, waits for
	// release and fails.
	failStart bool

	stopping  chan struct{}
	startHeld chan struct{}
	release   chan struct{}
}

func newHoldingProvider() *holdingProvider {
	return &holdingProvider{
		held: map[string]bool{}, started: map[string]int{}, last: map[string]*holdingSandbox{},
		stopping: make(chan struct{}, 4), startHeld: make(chan struct{}, 1), release: make(chan struct{}),
	}
}

func (p *holdingProvider) wrap(homeBase string, log *slog.Logger) orchestrator.SandboxProvider {
	p.inner = &inprocProvider{homeBase: homeBase, log: log}
	return p
}

// hold makes stopping this agent's sandbox wait for release.
func (p *holdingProvider) hold(agentID uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held[agentID.String()] = true
}

func (p *holdingProvider) starts(agentID uuid.UUID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started[agentID.String()]
}

// cut ends the daemon of the agent's running sandbox the way a container
// that dies does: the link drops in the middle of the task.
func (p *holdingProvider) cut(agentID uuid.UUID) {
	p.mu.Lock()
	sb := p.last[agentID.String()]
	p.mu.Unlock()
	_ = sb.inner.Stop(context.Background())
}

func (p *holdingProvider) Start(ctx context.Context, spec orchestrator.SandboxSpec) (orchestrator.Sandbox, error) {
	p.mu.Lock()
	p.started[spec.AgentID.String()]++
	fail := p.failStart
	p.failStart = false
	p.mu.Unlock()
	if fail {
		p.startHeld <- struct{}{}
		<-p.release
		return nil, errors.New("image not found")
	}
	sb, err := p.inner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	hs := &holdingSandbox{inner: sb, p: p, agentID: spec.AgentID.String()}
	p.mu.Lock()
	p.last[hs.agentID] = hs
	p.mu.Unlock()
	return hs, nil
}

type holdingSandbox struct {
	inner   orchestrator.Sandbox
	p       *holdingProvider
	agentID string
}

func (s *holdingSandbox) Stop(ctx context.Context) error {
	s.p.mu.Lock()
	held := s.p.held[s.agentID]
	s.p.mu.Unlock()
	if held {
		s.p.stopping <- struct{}{}
		select {
		case <-s.p.release:
		case <-ctx.Done():
		}
	}
	return s.inner.Stop(context.Background())
}

// sessionEnds collects the orchestrator's "session ended" lines: what each
// end of a session started after it, per agent.
type sessionEnds struct {
	mu   sync.Mutex
	ends map[string]chan string
}

func newSessionEnds() *sessionEnds { return &sessionEnds{ends: map[string]chan string{}} }

func (e *sessionEnds) of(agentID string) chan string {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := e.ends[agentID]
	if ch == nil {
		ch = make(chan string, 16)
		e.ends[agentID] = ch
	}
	return ch
}

// next waits for the next end of one of the agent's sessions and returns its
// successor: "none", "open work" or "wake pressed".
func (e *sessionEnds) next(t *testing.T, agentID uuid.UUID) string {
	t.Helper()
	select {
	case successor := <-e.of(agentID.String()):
		return successor
	case <-time.After(30 * time.Second):
		t.Fatalf("no session of agent %s ended", agentID)
		return ""
	}
}

func (e *sessionEnds) Enabled(context.Context, slog.Level) bool { return true }
func (e *sessionEnds) WithAttrs([]slog.Attr) slog.Handler       { return e }
func (e *sessionEnds) WithGroup(string) slog.Handler            { return e }

func (e *sessionEnds) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "session ended" {
		return nil
	}
	var agent, successor string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "agent":
			agent = a.Value.String()
		case "successor":
			successor = a.Value.String()
		}
		return true
	})
	e.of(agent) <- successor
	return nil
}

// wakeStack is the stack these tests share: the holding sandbox, the session
// ends, and a tick that never comes.
func wakeStack(t *testing.T) (*stack, *holdingProvider, *sessionEnds) {
	t.Helper()
	hold := newHoldingProvider()
	ends := newSessionEnds()
	s := newStackWith(t, stackOpts{provider: hold.wrap, tickInterval: time.Hour, logTap: ends})
	return s, hold, ends
}

// securingWindow brings an agent through one task into `securing` and holds
// it there.
func securingWindow(t *testing.T, s *stack, hold *holdingProvider, slug string) agents.Agent {
	t.Helper()
	return securingAfter(t, s, hold, slug, "[mock:result done]", backlog.StateDone)
}

// securingAfter is securingWindow with a first task of the test's own, which
// ends in the state given.
func securingAfter(t *testing.T, s *stack, hold *holdingProvider, slug, body, ends string) agents.Agent {
	t.Helper()
	agent := s.newSupportAgent(slug)
	hold.hold(agent.ID)
	first := s.task(agent.ID, "First task", body)
	select {
	case <-hold.stopping:
	case <-time.After(40 * time.Second):
		t.Fatalf("the sandbox never went down; task %s", s.taskState(first.ID))
	}
	if st := s.agentStatus(agent.ID); st != "securing" {
		t.Fatalf("status while the sandbox goes down: %q, expected securing", st)
	}
	if st := s.taskState(first.ID); st != ends {
		t.Fatalf("first task: %q, expected %s", st, ends)
	}
	return agent
}

func (s *stack) task(agentID uuid.UUID, title, body string) backlog.Task {
	s.t.Helper()
	task, err := s.backlog.Create(context.Background(), s.orgID, agentID, title, body, "manual", 3)
	if err != nil {
		s.t.Fatal(err)
	}
	return task
}

// pickedUp waits until the task handed over during securing is done, by a
// second waking phase.
func pickedUp(t *testing.T, s *stack, hold *holdingProvider, agentID, taskID uuid.UUID) {
	t.Helper()
	waitForOr(t, "the task handed over during securing is not picked up", 20*time.Second, func() bool {
		return s.taskState(taskID) == backlog.StateDone
	}, func() string { return s.recordingSummary(agentID) })
	if n := hold.starts(agentID); n != 2 {
		t.Errorf("%d sandboxes started, expected 2 — one per waking phase", n)
	}
}

// A task created during securing: its NOTIFY finds the session still
// registered. The end of the session looks at the backlog and finds it.
func TestATaskCreatedDuringSecuringIsPickedUp(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := securingWindow(t, s, hold, "create-securing")

	second := s.task(agent.ID, "Second task", "[mock:result also done]")
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "open work" {
		t.Errorf("the end of the session started %q, expected the next one for open work", got)
	}
	pickedUp(t, s, hold, agent.ID, second.ID)
}

// A colleague delegates during securing: CreateAgentTask wakes the agent
// itself, besides the NOTIFY, and finds the session as well.
func TestADelegationDuringSecuringIsPickedUp(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := securingWindow(t, s, hold, "delegate-securing")
	sender := s.newSupportAgent("delegate-sender")

	sent := s.task(sender.ID, "Not my field",
		`[mock:action covey/create_task {"title":"Please take over","body":"[mock:result taken over]","agent":"delegate-securing"}]
[mock:result delegated]`)
	waitFor(t, "the colleague does not delegate", 20*time.Second, func() bool {
		return s.taskState(sent.ID) == backlog.StateDone
	})
	delegated := childOf(t, s, agent.ID, sent.ID)
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "open work" {
		t.Errorf("the end of the session started %q, expected the next one for open work", got)
	}
	pickedUp(t, s, hold, agent.ID, delegated.ID)
}

// cancelledMidRun starts the task, waits until it runs and cancels it: the
// run's result is then refused by the store, and the session stops on that
// error instead of going on to its last look at the backlog.
func cancelledMidRun(t *testing.T, s *stack, taskID uuid.UUID) {
	t.Helper()
	waitFor(t, "the task does not start running", 20*time.Second, func() bool {
		return s.taskState(taskID) == backlog.StateInProgress
	})
	if _, err := s.backlog.Cancel(context.Background(), taskID, "not needed after all"); err != nil {
		t.Fatal(err)
	}
}

// A delegation during securing after the session stopped on its task's error
// — the task was cancelled while it ran. The session never looked again, and
// its end looks instead.
func TestADelegationDuringSecuringAfterATaskErrorIsPickedUp(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := s.newSupportAgent("error-securing")
	hold.hold(agent.ID)
	first := s.task(agent.ID, "First task", "[mock:sleep 2s][mock:result done]")
	cancelledMidRun(t, s, first.ID)
	select {
	case <-hold.stopping:
	case <-time.After(20 * time.Second):
		t.Fatalf("the sandbox never went down; task %s", s.taskState(first.ID))
	}

	second := s.task(agent.ID, "Second task", "[mock:result also done]")
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "open work" {
		t.Errorf("the end of the session after a task error started %q, expected the next one for open work", got)
	}
	pickedUp(t, s, hold, agent.ID, second.ID)
	if st := s.taskState(first.ID); st != backlog.StateCancelled {
		t.Errorf("the first task is %q, expected cancelled", st)
	}
}

// A warm agent parks its sandbox instead of stopping it, and the end of its
// session is the same: after a task error it looks at the backlog, and the
// next session takes the parked sandbox over rather than starting one.
func TestAWarmAgentAfterATaskErrorPicksUpTheRest(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := s.newSupportAgent("warm-error")
	if err := s.registry.SetWarmSandbox(context.Background(), agent.ID, true); err != nil {
		t.Fatal(err)
	}
	// Both tasks open before any session, without a wake: the cancelled one
	// first by priority, the second one left for after it.
	var first, rest uuid.UUID
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO backlog_tasks
		(id, org_id, agent_id, title, body, origin, priority)
		VALUES (gen_random_uuid(), $1, $2, 'First task', '[mock:sleep 2s][mock:result done]', 'manual', 3) RETURNING id`,
		s.orgID, agent.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO backlog_tasks
		(id, org_id, agent_id, title, body, origin, priority)
		VALUES (gen_random_uuid(), $1, $2, 'Second task', '[mock:result done]', 'manual', 5) RETURNING id`,
		s.orgID, agent.ID).Scan(&rest); err != nil {
		t.Fatal(err)
	}
	s.orch.EnsureRunning(agent.ID)
	cancelledMidRun(t, s, first)

	if got := ends.next(t, agent.ID); got != "open work" {
		t.Errorf("the end of the warm session after a task error started %q, expected the next one for open work", got)
	}
	if got := ends.next(t, agent.ID); got != "none" {
		t.Errorf("the end of the second warm session started %q, expected none", got)
	}
	if st := s.taskState(rest); st != backlog.StateDone {
		t.Errorf("the second task is %q, expected done", st)
	}
	if n := hold.starts(agent.ID); n != 1 {
		t.Errorf("%d sandboxes started, expected 1 — the parked one taken over", n)
	}
	if st := s.agentStatus(agent.ID); st != "sleeping" {
		t.Errorf("the parked agent shows %q, expected sleeping", st)
	}
}

// Work that becomes open with no wake at all — a row written by hand, a
// reopen that does not notify — is found by the same look.
func TestWorkOpenedWithoutAWakeDuringSecuringIsPickedUp(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := securingWindow(t, s, hold, "silent-securing")

	var id uuid.UUID
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO backlog_tasks
		(id, org_id, agent_id, title, body, origin, priority)
		VALUES (gen_random_uuid(), $1, $2, 'Second task', '[mock:result also done]', 'manual', 3) RETURNING id`,
		s.orgID, agent.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "open work" {
		t.Errorf("the end of the session started %q, expected the next one for open work", got)
	}
	pickedUp(t, s, hold, agent.ID, id)
}

// A person presses Wake while the wake in flight is about to fail. The press
// is answered after that failure, without the backoff the failure set — the
// person may have fixed exactly what made it fail.
func TestAWakePressedDuringAFailingWakeIsAnswered(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := s.newSupportAgent("press-failing")
	hold.mu.Lock()
	hold.failStart = true
	hold.mu.Unlock()

	task := s.task(agent.ID, "First task", "[mock:result done]")
	select {
	case <-hold.startHeld:
	case <-time.After(20 * time.Second):
		t.Fatalf("no sandbox start; task %s", s.taskState(task.ID))
	}
	s.orch.WakeNow(agent.ID)
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "wake pressed" {
		t.Errorf("the end of the failed wake started %q, expected the press to be answered", got)
	}
	waitForOr(t, "the press during the failing wake is lost", 20*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateDone
	}, func() string { return s.recordingSummary(agent.ID) })
	if n := hold.starts(agent.ID); n != 2 {
		t.Errorf("%d sandbox starts, expected 2 — the failed one and the answer to the press", n)
	}
}

// A sandbox that loses its daemon mid-task puts the task back, and the end of
// the session starts nothing — even though a wake (the tick's, here) reached
// the session while it ran. Under Docker pressure several agents lose their
// link in the same second; the tick brings them back with its spacing.
func TestNoSuccessorAfterADaemonLoss(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := s.newSupportAgent("daemon-loss")

	task := s.task(agent.ID, "Long task", "[mock:sleep 30s]")
	waitFor(t, "the task does not start running", 20*time.Second, func() bool {
		var n int
		_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM recording_events
			WHERE agent_id=$1 AND task_id=$2 AND kind='runtime'`, agent.ID, task.ID).Scan(&n)
		return n > 0
	})
	s.orch.EnsureRunning(agent.ID) // what every tick does for an agent with open work
	hold.cut(agent.ID)

	if got := ends.next(t, agent.ID); got != "none" {
		t.Errorf("the end of a session that lost its daemon started %q, expected none", got)
	}
	if st := s.taskState(task.ID); st != backlog.StateOpen {
		t.Errorf("the task after the daemon loss is %q, expected it back open", st)
	}
	if n := hold.starts(agent.ID); n != 1 {
		t.Errorf("%d sandboxes started, expected 1", n)
	}
}

// A killed agent is not woken by what was asked for before the kill. The end
// of its session answers the press — and the next session ends at runAgent's
// own kill check, before any sandbox.
func TestAKilledAgentIsNotWokenAfterSecuring(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := securingWindow(t, s, hold, "kill-securing")

	second := s.task(agent.ID, "Second task", "[mock:result done]")
	s.orch.WakeNow(agent.ID)
	if err := s.orch.Kill(context.Background(), agent.ID); err != nil {
		t.Fatal(err)
	}
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "wake pressed" {
		t.Errorf("the end of the killed session started %q, expected the press to be answered", got)
	}
	if got := ends.next(t, agent.ID); got != "none" {
		t.Errorf("the session after the kill started %q, expected none", got)
	}
	if n := hold.starts(agent.ID); n != 1 {
		t.Errorf("%d sandboxes started after a kill, expected 1", n)
	}
	if st := s.taskState(second.ID); st != backlog.StateOpen {
		t.Errorf("the task of a killed agent is %q, expected it to wait open", st)
	}
	// The status was decided before the sandbox went down, when the agent
	// was not killed yet, and sleeping went over the kill.
	if st := s.agentStatus(agent.ID); st != "killed" {
		t.Errorf("the agent killed during securing shows %q, expected killed", st)
	}
}

// A person resumes the agent while the sandbox of the killed session is still
// going down: the look at the end finds the work, and the agent is no longer
// killed when the next session asks.
func TestAResumeDuringSecuringIsAnswered(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := securingWindow(t, s, hold, "resume-securing")

	second := s.task(agent.ID, "Second task", "[mock:result also done]")
	if err := s.orch.Kill(context.Background(), agent.ID); err != nil {
		t.Fatal(err)
	}
	resume(t, s, agent.ID)
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "wake pressed" {
		t.Errorf("the end of the session started %q, expected the resume to be answered", got)
	}
	pickedUp(t, s, hold, agent.ID, second.ID)
}

// resume presses Resume on the agent's page.
func resume(t *testing.T, s *stack, agentID uuid.UUID) {
	t.Helper()
	login(t, s, "admin@test.local", "admin-passwort").
		expect(http.MethodPost, "/api/v1/agents/"+agentID.String()+"/resume", nil, http.StatusOK)
}

// killedMidTask kills an agent in the middle of a long task and holds its
// sandbox going down: the session ends with the task put back, not after a
// last look that found nothing.
func killedMidTask(t *testing.T, s *stack, hold *holdingProvider, slug string) (agents.Agent, backlog.Task) {
	t.Helper()
	agent := s.newSupportAgent(slug)
	hold.hold(agent.ID)
	task := s.task(agent.ID, "Long task", "[mock:sleep 30s]")
	waitFor(t, "the task does not start running", 20*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateInProgress
	})
	if err := s.orch.Kill(context.Background(), agent.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hold.stopping:
	case <-time.After(20 * time.Second):
		t.Fatalf("the sandbox of the killed session never went down; task %s", s.taskState(task.ID))
	}
	if st := s.taskState(task.ID); st != backlog.StateOpen {
		t.Fatalf("the task of the killed session is %q, expected it back open", st)
	}
	return agent, task
}

// A person kills an agent in the middle of a task and resumes it while its
// sandbox is still going down — "wrong agent, put it back". The resume finds
// the session and is dropped; the end of the killed session looks at the
// backlog, and the agent is no longer killed when the next session asks.
func TestAResumeAfterAKillMidTaskIsAnswered(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent, task := killedMidTask(t, s, hold, "resume-mid-task")

	resume(t, s, agent.ID)
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "wake pressed" {
		t.Errorf("the end of the killed session started %q, expected the resume to be answered", got)
	}
	waitForOr(t, "the task put back by the kill is not picked up after the resume", 20*time.Second, func() bool {
		return hold.starts(agent.ID) == 2 && s.taskState(task.ID) == backlog.StateInProgress
	}, func() string { return s.recordingSummary(agent.ID) })
	if st := s.agentStatus(agent.ID); st == "killed" {
		t.Errorf("the resumed agent shows %q", st)
	}
}

// The same resume, and the task is called off before the sandbox is down:
// the next session finds nothing and never writes a status. The agent shows
// what its row says — not the kill the session knew of before it went down.
func TestAResumeAfterAKillMidTaskShowsTheAgentAsleep(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent, task := killedMidTask(t, s, hold, "resume-cancel")

	resume(t, s, agent.ID)
	if _, err := s.backlog.Cancel(context.Background(), task.ID, "not needed after all"); err != nil {
		t.Fatal(err)
	}
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "wake pressed" {
		t.Errorf("the end of the killed session started %q, expected the resume to be answered", got)
	}
	if got := ends.next(t, agent.ID); got != "none" {
		t.Errorf("the session after the resume started %q, expected none", got)
	}
	if st := s.agentStatus(agent.ID); st != "sleeping" {
		t.Errorf("the resumed agent shows %q, expected sleeping", st)
	}
	if n := hold.starts(agent.ID); n != 1 {
		t.Errorf("%d sandboxes started, expected 1", n)
	}
}

// A resume that finds a session about to end quietly — the successor of a
// killed session that read the agent as killed just before the resume, here a
// wake that is about to fail — is held on it like a press and answered at its
// end. The same for the fleet's resume.
func TestAResumeFindingASessionIsAnswered(t *testing.T) {
	for name, path := range map[string]func(uuid.UUID) string{
		"agent": func(id uuid.UUID) string { return "/api/v1/agents/" + id.String() + "/resume" },
		"fleet": func(uuid.UUID) string { return "/api/v1/fleet/resume" },
	} {
		t.Run(name, func(t *testing.T) {
			s, hold, ends := wakeStack(t)
			agent := s.newSupportAgent("resume-finds-" + name)
			hold.mu.Lock()
			hold.failStart = true
			hold.mu.Unlock()

			task := s.task(agent.ID, "First task", "[mock:result done]")
			select {
			case <-hold.startHeld:
			case <-time.After(20 * time.Second):
				t.Fatalf("no sandbox start; task %s", s.taskState(task.ID))
			}
			login(t, s, "admin@test.local", "admin-passwort").
				expect(http.MethodPost, path(agent.ID), nil, http.StatusOK)
			close(hold.release)

			if got := ends.next(t, agent.ID); got != "wake pressed" {
				t.Errorf("the end of the session the resume found started %q, expected the resume to be answered", got)
			}
			waitForOr(t, "the resume is lost on the session it found", 20*time.Second, func() bool {
				return s.taskState(task.ID) == backlog.StateDone
			}, func() string { return s.recordingSummary(agent.ID) })
		})
	}
}

// The same kill without a resume: the look finds the task, and the successor
// ends at runAgent's kill check, before any sandbox.
func TestAKillMidTaskStaysKilled(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent, task := killedMidTask(t, s, hold, "kill-mid-task")
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "open work" {
		t.Errorf("the end of the killed session started %q, expected a look at the open work", got)
	}
	if got := ends.next(t, agent.ID); got != "none" {
		t.Errorf("the session after the kill started %q, expected none", got)
	}
	if n := hold.starts(agent.ID); n != 1 {
		t.Errorf("%d sandboxes started after a kill, expected 1", n)
	}
	if st := s.taskState(task.ID); st != backlog.StateOpen {
		t.Errorf("the task of a killed agent is %q, expected it to wait open", st)
	}
	if st := s.agentStatus(agent.ID); st != "killed" {
		t.Errorf("the killed agent shows %q, expected killed", st)
	}
}

// A wake that arrives while the control plane shuts down starts nothing: the
// process is waiting for its own work to stop, not for more of it.
func TestNoSuccessorAfterSecuringDuringShutdown(t *testing.T) {
	s, hold, ends := wakeStack(t)
	agent := securingWindow(t, s, hold, "shutdown-securing")

	s.task(agent.ID, "Second task", "[mock:result done]")
	s.orch.WakeNow(agent.ID)
	s.cancel()
	close(hold.release)

	if got := ends.next(t, agent.ID); got != "none" {
		t.Errorf("the end of the session during the shutdown started %q, expected none", got)
	}
	if n := hold.starts(agent.ID); n != 1 {
		t.Errorf("%d sandboxes started during the shutdown, expected 1", n)
	}
}
