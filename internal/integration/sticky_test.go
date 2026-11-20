package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/db"
	"covey/migrations"
)

// Sticky sessions per topic (#130): a developer's fix round resumes the
// session of its last finished task on the same pull request; the reviewer
// never does; anything that makes a resume wrong or worthless starts fresh,
// with a handover, and the decision is recorded on the task.

// sessionView is what the mock's [mock:session] reports of how a run started.
type sessionView struct {
	SystemPrompt string `json:"system_prompt"`
	WorkDir      string `json:"work_dir"`
	Resume       string `json:"resume"`
	Prompt       string `json:"prompt"`
}

func (s *stack) stickyAgent(slug, policy string) agents.Agent {
	s.t.Helper()
	a := s.newSupportAgent(slug)
	if err := s.registry.SetSticky(context.Background(), a.ID, policy, 55, 200000); err != nil {
		s.t.Fatal(err)
	}
	a.StickyPolicy, a.StickyWindowMinutes, a.StickyContextCap = policy, 55, 200000
	return a
}

// runTopic creates a task on the topic and waits until it is done. Its body is
// [mock:session] plus extra; it returns the task and what the run reported.
func (s *stack) runTopic(agent agents.Agent, title, topic, extra string) (backlog.Task, sessionView) {
	s.t.Helper()
	ctx := context.Background()
	task, err := s.backlog.CreateWithTopic(ctx, s.orgID, agent.ID, title, extra+"\n[mock:session]", "manual", 3, topic)
	if err != nil {
		s.t.Fatal(err)
	}
	waitFor(s.t, title+" done", 30*time.Second, func() bool {
		st := s.taskState(task.ID)
		return st == backlog.StateDone || st == backlog.StateFailed
	})
	got, err := s.backlog.Get(ctx, task.ID)
	if err != nil {
		s.t.Fatal(err)
	}
	if got.State != backlog.StateDone || got.Result == nil {
		s.t.Fatalf("%s: state %s, error %v", title, got.State, got.Error)
	}
	var v sessionView
	if err := json.Unmarshal([]byte(*got.Result), &v); err != nil {
		s.t.Fatalf("%s: result is not the session view: %q", title, *got.Result)
	}
	return got, v
}

// finishedAgo moves a task's end into the past, as if the next round came
// that much later.
func (s *stack) finishedAgo(id uuid.UUID, d time.Duration) {
	s.t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`UPDATE backlog_tasks SET session_finished_at = now() - $2::interval WHERE id=$1`,
		id, d.String()); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stack) decision(id uuid.UUID) (string, string) {
	s.t.Helper()
	var d, r *string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT sticky_decision, sticky_reason FROM backlog_tasks WHERE id=$1`, id).Scan(&d, &r); err != nil {
		s.t.Fatal(err)
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return str(d), str(r)
}

func mockSessionID(id uuid.UUID) string { return "mock-session-" + id.String() }

// AC 1: task B on pr:42, ten minutes after A on pr:42, runs with --resume <A's
// session>, in A's directory, under a system prompt byte-identical to A's.
func TestStickyFixRoundResumesSession(t *testing.T) {
	s := newStack(t)
	dev := s.stickyAgent("sticky-dev", agents.StickyConditional)

	a, va := s.runTopic(dev, "Fix round 1", "pr:42", "")
	if va.Resume != "" {
		t.Fatalf("the first task on a topic starts fresh, resumed %q", va.Resume)
	}
	if !strings.HasSuffix(va.WorkDir, "/scratch/topic-sticky-dev-pr-42") {
		t.Fatalf("a topic task works in the topic's directory, is %q", va.WorkDir)
	}
	if d, r := s.decision(a.ID); d != backlog.DecisionFresh || r != backlog.ReasonNoPrevious {
		t.Fatalf("first round: %s/%s", d, r)
	}
	s.finishedAgo(a.ID, 10*time.Minute)

	b, vb := s.runTopic(dev, "Fix round 2", "pr:42", "")
	if vb.Resume != mockSessionID(a.ID) {
		t.Fatalf("round 2 resumes A's session %q, resumed %q", mockSessionID(a.ID), vb.Resume)
	}
	if vb.WorkDir != va.WorkDir {
		t.Fatalf("round 2 works in A's directory %q, is %q", va.WorkDir, vb.WorkDir)
	}
	if vb.SystemPrompt != va.SystemPrompt {
		t.Fatalf("round 2's system prompt differs from A's:\nA: %d bytes\nB: %d bytes", len(va.SystemPrompt), len(vb.SystemPrompt))
	}
	if !strings.Contains(vb.Prompt, "Fix round 2") || strings.Contains(vb.Prompt, "The event you were waiting for") {
		t.Fatalf("the resume message is the new task: %q", vb.Prompt)
	}
	if d, _ := s.decision(b.ID); d != backlog.DecisionResumed {
		t.Fatalf("round 2 decision %q", d)
	}
	// And round 3 continues the same session through B.
	c, vc := s.runTopic(dev, "Fix round 3", "pr:42", "")
	if vc.Resume != mockSessionID(a.ID) || vc.SystemPrompt != va.SystemPrompt {
		t.Fatalf("round 3 resumes the session B continued: %q", vc.Resume)
	}
	var from uuid.UUID
	if err := s.pool.QueryRow(context.Background(), `SELECT sticky_from FROM backlog_tasks WHERE id=$1`, c.ID).Scan(&from); err != nil || from != b.ID {
		t.Fatalf("round 3 comes from B: %v %v", from, err)
	}
}

// AC 2 and the review in the handover: 56 minutes after A → fresh, reason
// window, the prompt starts with A's report and the latest review on the PR.
func TestStickyOutsideWindowHandsOver(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("window-dev", agents.StickyConditional)
	reviewer := s.newSupportAgent("covey-reviewer")

	a, _ := s.runTopic(dev, "Fix round 1", "pr:42", "")
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET result='REPORT-OF-A: fixed the race' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	rv, err := s.backlog.CreateWithTopic(ctx, s.orgID, reviewer.ID, "Review pr 42", "[mock:result notes\n## Review round 1\nM1: the lock is taken twice]", "manual", 3, "pr:42")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "review done", 30*time.Second, func() bool { return s.taskState(rv.ID) == backlog.StateDone })
	s.finishedAgo(a.ID, 56*time.Minute)

	b, vb := s.runTopic(dev, "Fix round 2", "pr:42", "")
	if vb.Resume != "" {
		t.Fatalf("outside the window no resume, resumed %q", vb.Resume)
	}
	if !strings.HasPrefix(vb.Prompt, "Fix round 2\n\n## Handover (pr:42)") {
		t.Fatalf("the prompt starts with the handover: %q", vb.Prompt[:min(len(vb.Prompt), 200)])
	}
	if !strings.Contains(vb.Prompt, "REPORT-OF-A") || !strings.Contains(vb.Prompt, "## Review round 1\nM1: the lock is taken twice") {
		t.Fatalf("the handover carries A's report and the review: %q", vb.Prompt)
	}
	if strings.Contains(vb.Prompt, "notes\n## Review") {
		t.Fatal("the review is taken from its heading on")
	}
	if d, r := s.decision(b.ID); d != backlog.DecisionFresh || r != backlog.ReasonWindow {
		t.Fatalf("decision %s/%s, want fresh/window", d, r)
	}
}

// AC 3: the context cap, from the run's own lines (200,001) and at 199,999.
func TestStickyContextCap(t *testing.T) {
	s := newStack(t)
	dev := s.stickyAgent("cap-dev", agents.StickyConditional)
	turn := `[mock:line {"type":"assistant","parent_tool_use_id":null,"message":{"usage":{"input_tokens":1,` +
		`"cache_read_input_tokens":199998,"cache_creation_input_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2}}}}]`
	a, _ := s.runTopic(dev, "Round 1", "pr:7", turn)
	var ctxTokens, read, w1h int64
	if err := s.pool.QueryRow(context.Background(), `SELECT session_context_tokens, first_turn_cache_read, first_turn_cache_write_1h
		FROM backlog_tasks WHERE id=$1`, a.ID).Scan(&ctxTokens, &read, &w1h); err != nil {
		t.Fatal(err)
	}
	if ctxTokens != 200001 || read != 199998 || w1h != 2 {
		t.Fatalf("recorded context %d, first-turn read %d, 1h write %d", ctxTokens, read, w1h)
	}
	b, vb := s.runTopic(dev, "Round 2", "pr:7", "")
	if vb.Resume != "" || !strings.Contains(vb.Prompt, "## Handover (pr:7)") {
		t.Fatalf("over the cap: fresh with handover, resumed %q", vb.Resume)
	}
	if d, r := s.decision(b.ID); d != backlog.DecisionFresh || r != backlog.ReasonCap {
		t.Fatalf("decision %s/%s, want fresh/cap", d, r)
	}

	if _, err := s.pool.Exec(context.Background(), `UPDATE backlog_tasks SET session_context_tokens=199999 WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	c, vc := s.runTopic(dev, "Round 3", "pr:7", "")
	if vc.Resume != mockSessionID(b.ID) {
		t.Fatalf("under the cap: resumes B's session, resumed %q", vc.Resume)
	}
	if d, _ := s.decision(c.ID); d != backlog.DecisionResumed {
		t.Fatalf("decision %s", d)
	}
}

// AC 4: what is not the same agent and topic starts fresh without a handover;
// stickiness off and a missing transcript start fresh with one.
func TestStickyFreshCases(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("fresh-dev", agents.StickyConditional)
	other := s.stickyAgent("fresh-other", agents.StickyConditional)

	a, va := s.runTopic(dev, "Round 1", "pr:42", "")

	// Another topic.
	b, vb := s.runTopic(dev, "Other topic", "pr:43", "")
	if vb.Resume != "" || strings.Contains(vb.Prompt, "Handover") || vb.WorkDir == va.WorkDir {
		t.Fatalf("another topic: %+v", vb)
	}
	if d, r := s.decision(b.ID); d != backlog.DecisionFresh || r != backlog.ReasonNoPrevious {
		t.Fatalf("another topic: %s/%s", d, r)
	}
	// Another agent, same topic: never another agent's session.
	c, vc := s.runTopic(other, "Same topic, other agent", "pr:42", "")
	if vc.Resume != "" || strings.Contains(vc.Prompt, "Handover") || vc.WorkDir == va.WorkDir {
		t.Fatalf("another agent: %+v", vc)
	}
	if d, r := s.decision(c.ID); d != backlog.DecisionFresh || r != backlog.ReasonNoPrevious {
		t.Fatalf("another agent: %s/%s", d, r)
	}
	// No topic: nothing sticky at all, the task's own directory.
	plain, err := s.backlog.Create(ctx, s.orgID, dev.ID, "No topic", "[mock:session]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "plain done", 30*time.Second, func() bool { return s.taskState(plain.ID) == backlog.StateDone })
	pt, _ := s.backlog.Get(ctx, plain.ID)
	var vp sessionView
	_ = json.Unmarshal([]byte(*pt.Result), &vp)
	if vp.Resume != "" || !strings.HasSuffix(vp.WorkDir, plain.ID.String()) {
		t.Fatalf("no topic: %+v", vp)
	}
	if d, _ := s.decision(plain.ID); d != "" {
		t.Fatalf("no topic records no decision, got %q", d)
	}

	// The transcript is gone (another runner, a home without it): fresh with
	// the handover, reason no_session — decided by the daemon.
	if err := os.RemoveAll(filepath.Join(s.homeBase, dev.ID.String(), ".mock")); err != nil {
		t.Fatal(err)
	}
	d2, vd := s.runTopic(dev, "Round 2", "pr:42", "")
	if vd.Resume != "" || !strings.Contains(vd.Prompt, "## Handover (pr:42)") {
		t.Fatalf("no transcript: fresh with handover, got %+v", vd)
	}
	if vd.WorkDir != va.WorkDir {
		t.Fatalf("the fresh session starts in the topic dir, so the next round finds it: %q", vd.WorkDir)
	}
	if d, r := s.decision(d2.ID); d != backlog.DecisionFresh || r != backlog.ReasonNoSession {
		t.Fatalf("no transcript: %s/%s", d, r)
	}
	// ... and the next round resumes THAT session, under the prompt it started with.
	_, ve := s.runTopic(dev, "Round 3", "pr:42", "")
	if ve.Resume != mockSessionID(d2.ID) || ve.SystemPrompt != vd.SystemPrompt {
		t.Fatalf("after a fallback the fallback's session is the one to resume: %q", ve.Resume)
	}

	// Stickiness off.
	if err := s.registry.SetSticky(ctx, dev.ID, agents.StickyFresh, 55, 200000); err != nil {
		t.Fatal(err)
	}
	f, vf := s.runTopic(dev, "Round 4", "pr:42", "")
	if vf.Resume != "" || !strings.Contains(vf.Prompt, "## Handover (pr:42)") || vf.WorkDir == va.WorkDir {
		t.Fatalf("off: fresh with handover in its own dir, got %+v", vf)
	}
	if d, r := s.decision(f.ID); d != backlog.DecisionFresh || r != backlog.ReasonOff {
		t.Fatalf("off: %s/%s", d, r)
	}
	_ = a
}

// The reviewer never resumes, however close its rounds are: its default is
// fresh, each round runs in its own directory without the earlier session.
func TestStickyReviewerNeverResumes(t *testing.T) {
	s := newStack(t)
	reviewer := s.newSupportAgent("covey-reviewer")
	if reviewer.StickyPolicy != agents.StickyFresh {
		t.Fatalf("covey-reviewer defaults to fresh, is %q", reviewer.StickyPolicy)
	}
	a, va := s.runTopic(reviewer, "Review round 1", "pr:9", "")
	b, vb := s.runTopic(reviewer, "Review round 2", "pr:9", "")
	if vb.Resume != "" || vb.WorkDir == va.WorkDir || !strings.HasSuffix(vb.WorkDir, b.ID.String()) {
		t.Fatalf("the reviewer resumed or shares a dir: %+v", vb)
	}
	if d, r := s.decision(b.ID); d != backlog.DecisionFresh || r != backlog.ReasonOff {
		t.Fatalf("reviewer: %s/%s", d, r)
	}
	_ = a
}

// Isolation and concurrency: a session from another credential is not
// resumed, and a session another round already continues is not resumed a
// second time.
func TestStickyCredentialAndBusy(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("busy-dev", agents.StickyAlways)

	a, _ := s.runTopic(dev, "Round 1", "issue:5", "")
	// Another round holds A's session (it is blocked, say, waiting on an answer).
	holder, err := s.backlog.CreateWithTopic(ctx, s.orgID, dev.ID, "Holder", "x", "manual", 3, "issue:5")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "holder done", 30*time.Second, func() bool { return s.taskState(holder.ID) == backlog.StateDone })
	// The holder resumed A and holds its claim. Make A the latest finished
	// task again: the next round finds A — whose session the holder took.
	var successor *uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT session_successor FROM backlog_tasks WHERE id=$1`, a.ID).Scan(&successor); err != nil ||
		successor == nil || *successor != holder.ID {
		t.Fatalf("the holder claimed A's session: %v %v", successor, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET session_finished_at = now() - interval '1 hour' WHERE id=$1`, holder.ID); err != nil {
		t.Fatal(err)
	}
	b, vb := s.runTopic(dev, "Round 2", "issue:5", "")
	if vb.Resume != "" {
		t.Fatalf("a session another round continues is not resumed again, resumed %q", vb.Resume)
	}
	if d, r := s.decision(b.ID); d != backlog.DecisionFresh || r != backlog.ReasonBusy {
		t.Fatalf("busy: %s/%s", d, r)
	}

	// A session that ran on another credential.
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET session_credential='secret:someone-else:0' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	pred, ok, err := s.backlog.LastOnTopic(ctx, s.orgID, dev.ID, "issue:5", uuid.New())
	if err != nil || !ok || pred.TaskID != b.ID || pred.Credential != "secret:someone-else:0" {
		t.Fatalf("LastOnTopic: %+v %v %v", pred, ok, err)
	}
	// LastOnTopic never crosses organisations, whatever the caller passes.
	if _, ok, _ := s.backlog.LastOnTopic(ctx, uuid.New(), dev.ID, "issue:5", uuid.New()); ok {
		t.Fatal("LastOnTopic found a task under another organisation")
	}
	// Two claims on one session: the first wins, the second is refused, the
	// first may claim again (a requeue).
	x, y := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{x, y} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO backlog_tasks (id, org_id, agent_id, title, body, origin, priority, state)
			VALUES ($1,$2,$3,'claim','', 'manual', 9, 'blocked')`, id, s.orgID, dev.ID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.backlog.ClaimSession(ctx, b.ID, x); !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := s.backlog.ClaimSession(ctx, b.ID, y); ok {
		t.Fatal("a second round claimed a session already claimed")
	}
	if ok, _ := s.backlog.ClaimSession(ctx, b.ID, x); !ok {
		t.Fatal("the holder may claim again")
	}
	// A holder that ended without carrying the session on frees it.
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state='cancelled' WHERE id=$1`, x); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.backlog.ClaimSession(ctx, b.ID, y); !ok {
		t.Fatal("a cancelled holder must free the session")
	}
}

// AC 5: a topic task that blocks and is answered keeps its topic and resumes
// its own session in the topic's directory.
func TestStickyBlockKeepsTopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("block-dev", agents.StickyConditional)
	task, err := s.backlog.CreateWithTopic(ctx, s.orgID, dev.ID, "Needs an answer",
		"[mock:block key=ext:q-11 question=which?]\n[mock:session]", "manual", 3, "pr:11")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "blocked", 30*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateBlocked })
	got, _ := s.backlog.Get(ctx, task.ID)
	if got.TopicKey == nil || *got.TopicKey != "pr:11" {
		t.Fatalf("Block keeps the topic: %v", got.TopicKey)
	}
	if _, err := s.backlog.Answer(ctx, task.ID, "ada", "the left one"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "done", 30*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
	got, _ = s.backlog.Get(ctx, task.ID)
	if got.TopicKey == nil || *got.TopicKey != "pr:11" {
		t.Fatalf("Complete keeps the topic: %v", got.TopicKey)
	}
	var v sessionView
	if err := json.Unmarshal([]byte(*got.Result), &v); err != nil {
		t.Fatalf("result %q", *got.Result)
	}
	if v.Resume != mockSessionID(task.ID) || !strings.HasSuffix(v.WorkDir, "/scratch/topic-block-dev-pr-11") {
		t.Fatalf("blocked→working resumes its own session in the topic dir: %+v", v)
	}
	if v.Prompt != "the left one" && !strings.Contains(v.Prompt, "the left one") {
		t.Fatalf("resumed with the answer: %q", v.Prompt)
	}
}

// The turn-limit continuation of a topic task carries the topic, works in the
// topic's directory under the session's prompt, and the next round resumes it.
func TestStickyContinuation(t *testing.T) {
	s := newStack(t)
	dev := s.stickyAgent("cont-dev", agents.StickyConditional)
	ctx := context.Background()
	// (The mock passes a turn limit on every resume, so the round that runs
	// into it is the topic's first, fresh one.)
	task, err := s.backlog.CreateWithTopic(ctx, s.orgID, dev.ID, "Big round",
		"[mock:maxturns ## Next step\nfinish]\n[mock:session]", "manual", 3, "pr:21")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cut off", 30*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateFailed })
	child := childOf(t, s, dev.ID, task.ID)
	if child.TopicKey == nil || *child.TopicKey != "pr:21" {
		t.Fatalf("the continuation inherits the topic: %v", child.TopicKey)
	}
	waitFor(t, "continuation done", 30*time.Second, func() bool { return s.taskState(child.ID) == backlog.StateDone })
	child, _ = s.backlog.Get(ctx, child.ID)
	var vc sessionView
	if err := json.Unmarshal([]byte(*child.Result), &vc); err != nil {
		t.Fatalf("continuation result %q", *child.Result)
	}
	// The continuation resumes the cut-off session in the topic's directory,
	// under the prompt that session was started with.
	if vc.Resume != mockSessionID(task.ID) || !strings.HasSuffix(vc.WorkDir, "/scratch/topic-cont-dev-pr-21") {
		t.Fatalf("continuation: resume %q dir %q", vc.Resume, vc.WorkDir)
	}
	var parentPrompt, childPrompt string
	if err := s.pool.QueryRow(ctx, `SELECT p.session_prompt, c.session_prompt FROM backlog_tasks p, backlog_tasks c
		WHERE p.id=$1 AND c.id=$2`, task.ID, child.ID).Scan(&parentPrompt, &childPrompt); err != nil || parentPrompt != childPrompt {
		t.Fatalf("the continuation carries its session's prompt (%v)", err)
	}
	// The next round resumes that same session, under the same prompt bytes.
	n, vn := s.runTopic(dev, "Next round", "pr:21", "")
	if vn.Resume != mockSessionID(task.ID) || vn.SystemPrompt != vc.SystemPrompt || vn.WorkDir != vc.WorkDir {
		t.Fatalf("the round after a continuation resumes its session: %q, prompt same %v", vn.Resume, vn.SystemPrompt == vc.SystemPrompt)
	}
	if d, _ := s.decision(n.ID); d != backlog.DecisionResumed {
		t.Fatalf("decision %s", d)
	}
}

// AC 6: covey/create_task with a topic stores it; a malformed one is refused
// with the format; the protocol text names the parameter.
func TestCreateTaskTopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("topic-dev", agents.StickyConditional)
	parent, err := s.backlog.Create(ctx, s.orgID, dev.ID, "Parent", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	s.ohneLaeufe(dev.ID)
	resp := s.orch.CreateAgentTask(ctx, dev, parent.ID, daemon.RequestCreateTask{Title: "Fix review findings", Body: "b", Topic: "pr:42"})
	if !resp.OK {
		t.Fatalf("create_task with a topic: %s", resp.Error)
	}
	created, err := s.backlog.Get(ctx, uuid.MustParse(resp.TaskID))
	if err != nil || created.TopicKey == nil || *created.TopicKey != "pr:42" {
		t.Fatalf("topic not stored: %v %v", created.TopicKey, err)
	}
	for _, bad := range []string{"../x", "pr:" + strings.Repeat("1", 70), "pr:abc", "issue", "#42"} {
		resp := s.orch.CreateAgentTask(ctx, dev, parent.ID, daemon.RequestCreateTask{Title: "T " + bad, Topic: bad})
		if resp.OK || !strings.Contains(resp.Error, `"pr:<n>" or "issue:<n>"`) {
			t.Errorf("topic %q: ok=%v error %q", bad, resp.OK, resp.Error)
		}
	}
	if !strings.Contains(agents.CoveyActionsDoc, `"topic":"pr:<n>"`) {
		t.Error("the protocol text names the topic parameter")
	}
	// The API takes it too.
	if _, err := s.backlog.CreateWithTopic(ctx, s.orgID, dev.ID, "x", "", "manual", 3, "pr:../1"); err == nil {
		t.Error("the store refuses a malformed topic")
	}
}

// AC 7: defaults for existing agents after the migration, and for new agents.
func TestStickyDefaults(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	// Existing agents: back to before 0154, agents as they were, then up.
	version := func() int {
		var v int
		if err := s.pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for version() >= 154 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"covey-dev": "conditional", "covey-frontend": "conditional",
		"covey-reviewer": "fresh", "covey-lead": "fresh"}
	var ids []uuid.UUID
	for slug := range want {
		id := uuid.New()
		ids = append(ids, id)
		if _, err := s.pool.Exec(ctx, `INSERT INTO agents (id, org_id, slug, display_name, runtime) VALUES ($1,$2,$3,$3,'mock')`,
			id, s.orgID, slug); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for slug, w := range want {
		var p string
		var win int
		var capTokens int64
		if err := s.pool.QueryRow(ctx, `SELECT sticky_policy, sticky_window_minutes, sticky_context_cap FROM agents
			WHERE org_id=$1 AND slug=$2`, s.orgID, slug).Scan(&p, &win, &capTokens); err != nil {
			t.Fatal(err)
		}
		if p != w || win != 55 || capTokens != 200000 {
			t.Errorf("existing %s after the migration: %s/%d/%d, want %s/55/200000", slug, p, win, capTokens, w)
		}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE id = ANY($1)`, ids); err != nil {
		t.Fatal(err)
	}

	// New agents.
	for slug, w := range want {
		a, err := s.registry.Create(ctx, s.orgID, slug, slug, "mock", &s.adminID)
		if err != nil {
			t.Fatal(err)
		}
		if a.StickyPolicy != w || a.StickyWindowMinutes != 55 || a.StickyContextCap != 200000 {
			t.Errorf("new %s: %s/%d/%d", slug, a.StickyPolicy, a.StickyWindowMinutes, a.StickyContextCap)
		}
	}
	// The setting refuses what it cannot mean.
	a, _ := s.registry.GetBySlug(ctx, s.orgID, "covey-lead")
	for _, bad := range []struct {
		p string
		w int
		c int64
	}{{"on", 55, 1}, {"conditional", 0, 1}, {"conditional", 55, 0}, {"always", 20000, 1}} {
		if err := s.registry.SetSticky(ctx, a.ID, bad.p, bad.w, bad.c); err == nil {
			t.Errorf("SetSticky(%+v) accepted", bad)
		}
	}
}

// agentBelowSchema creates a test agent with plain SQL, for a migration test
// that has rolled the schema back below the registry's column list (0154 added
// the sticky settings to it). Same agent as newSupportAgent, without its config.
func (s *stack) agentBelowSchema(slug string) agents.Agent {
	s.t.Helper()
	a := agents.Agent{ID: uuid.New(), OrgID: s.orgID, Slug: slug, DisplayName: "Support-Agent", Runtime: "mock"}
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO agents (id, org_id, slug, display_name, runtime, owner_id, hired_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())`, a.ID, a.OrgID, a.Slug, a.DisplayName, a.Runtime, s.adminID); err != nil {
		s.t.Fatal(err)
	}
	return a
}

// taskBelowSchema creates an open task with plain SQL, likewise.
func (s *stack) taskBelowSchema(agentID uuid.UUID, title string, priority int) uuid.UUID {
	s.t.Helper()
	id := uuid.New()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO backlog_tasks (id, org_id, agent_id, title, body, origin, priority)
		VALUES ($1,$2,$3,$4,'','manual',$5)`, id, s.orgID, agentID, title, priority); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// A changed configuration ends the session line: the next round starts fresh
// (prompt_changed) instead of carrying the old instructions on.
func TestStickyPromptChanged(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("prompt-dev", agents.StickyAlways)
	s.runTopic(dev, "Round 1", "pr:3", "")
	if _, err := s.registry.SaveConfig(ctx, dev.ID, map[string]string{
		"SOUL.md":   "# Support-Agent\n\n## Role\nSupport, and now also reviews its own diff first.",
		"ACCESS.md": "- system: zammad scope: read,write",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}
	b, vb := s.runTopic(dev, "Round 2", "pr:3", "")
	if vb.Resume != "" || !strings.Contains(vb.SystemPrompt, "reviews its own diff first") {
		t.Fatalf("after a config change: fresh under the new prompt, resumed %q", vb.Resume)
	}
	if d, r := s.decision(b.ID); d != backlog.DecisionFresh || r != backlog.ReasonPromptChanged {
		t.Fatalf("decision %s/%s", d, r)
	}
	// And the round after that resumes the new session.
	_, vc := s.runTopic(dev, "Round 3", "pr:3", "")
	if vc.Resume != mockSessionID(b.ID) || vc.SystemPrompt != vb.SystemPrompt {
		t.Fatalf("round 3 resumes round 2's session: %q", vc.Resume)
	}
}

// A round blocked on its own session holds the newer work: a new round beside
// it does not resume the older finished session (busy).
func TestStickyBusyBesideBlockedRound(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("beside-dev", agents.StickyAlways)
	s.runTopic(dev, "Round 1", "pr:4", "")
	// Round 2 runs fresh (the mock never blocks on a resume) and blocks on a
	// question, holding its own session.
	if err := s.registry.SetSticky(ctx, dev.ID, agents.StickyFresh, 55, 200000); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.backlog.CreateWithTopic(ctx, s.orgID, dev.ID, "Round 2",
		"[mock:block key=ext:pr-4 question=which?]", "manual", 3, "pr:4")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "round 2 blocked", 30*time.Second, func() bool { return s.taskState(blocked.ID) == backlog.StateBlocked })
	if err := s.registry.SetSticky(ctx, dev.ID, agents.StickyAlways, 55, 200000); err != nil {
		t.Fatal(err)
	}
	// Round 1's session is unclaimed: only the open round can stop a resume.
	c, vc := s.runTopic(dev, "Round 3", "pr:4", "")
	if vc.Resume != "" {
		t.Fatalf("a round beside a blocked one must not resume the older session, resumed %q", vc.Resume)
	}
	if d, r := s.decision(c.ID); d != backlog.DecisionFresh || r != backlog.ReasonBusy {
		t.Fatalf("decision %s/%s", d, r)
	}
}

// The handover prefers the review comment as it was posted on the pull request
// (the recording of the reviewer's github action) over the reviewer's report,
// and only one newer than the previous round.
func TestStickyHandoverTakesThePostedReview(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("posted-dev", agents.StickyConditional)
	reviewer := s.newSupportAgent("posted-reviewer")
	a, _ := s.runTopic(dev, "Round 1", "pr:77", "")
	record := func(at time.Duration, pr int, body string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"action": "github:comment_pr", "ok": true,
			"params": map[string]any{"repo": "example/app", "pr_number": pr, "body": body}})
		if _, err := s.pool.Exec(ctx, `INSERT INTO recording_events (org_id, agent_id, kind, payload, created_at)
			VALUES ($1,$2,'action',$3, now() - $4::interval)`, s.orgID, reviewer.ID, payload, at.String()); err != nil {
			t.Fatal(err)
		}
	}
	s.finishedAgo(a.ID, 60*time.Minute)
	record(2*time.Hour, 77, "## Review round 0\nOLD: before round 1")
	record(30*time.Minute, 77, "Thanks.\n## Review round 1\nM1: posted on the PR")
	record(10*time.Minute, 78, "## Review round 1\nOTHER PR")
	_, vb := s.runTopic(dev, "Round 2", "pr:77", "")
	if !strings.Contains(vb.Prompt, "## Review round 1\nM1: posted on the PR") {
		t.Fatalf("the posted review is in the handover: %q", vb.Prompt)
	}
	for _, not := range []string{"OLD: before round 1", "OTHER PR", "Thanks."} {
		if strings.Contains(vb.Prompt, not) {
			t.Fatalf("%q must not be in the handover", not)
		}
	}
}
