package integration

import (
	"context"
	"encoding/json"
	"net/http"
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
	"covey/internal/memory"
	"covey/migrations"
)

// Sticky sessions (#130): the next task of an agent on the same topic resumes
// the previous task's session while it is warm, in the same directory and with
// the same system prompt; otherwise it starts fresh with a handover. The mock
// answers [mock:spec] with what its run was started with.

// runSpec is the mock's [mock:spec] answer.
type runSpec struct {
	SystemPrompt    string `json:"system_prompt"`
	MemoryContext   string `json:"memory_context"`
	WorkDir         string `json:"work_dir"`
	Topic           string `json:"topic"`
	Handover        string `json:"handover"`
	ResumeSessionID string `json:"resume_session_id"`
	ResumeInput     string `json:"resume_input"`
	Prompt          string `json:"prompt"`
}

func (s *stack) stickyAgent(slug string, on bool) agents.Agent {
	s.t.Helper()
	a := s.newSupportAgent(slug)
	if err := s.registry.SetSticky(context.Background(), a.ID, on, 55, 200_000); err != nil {
		s.t.Fatal(err)
	}
	return a
}

// onTopic creates a task on topic ("" = none) and waits for it to be done.
func (s *stack) onTopic(t *testing.T, agentID uuid.UUID, title, body, topic string) backlog.Task {
	t.Helper()
	ctx := context.Background()
	task, err := s.backlog.CreateOnTopic(ctx, s.orgID, agentID, title, body, "manual", 3, topic)
	if err != nil {
		t.Fatal(err)
	}
	waitForOr(t, title+" done", 20*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateDone
	}, func() string { return s.taskState(task.ID) })
	done, err := s.backlog.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func specOf(t *testing.T, task backlog.Task) runSpec {
	t.Helper()
	var sp runSpec
	if task.Result == nil || json.Unmarshal([]byte(*task.Result), &sp) != nil {
		t.Fatalf("no [mock:spec] answer in the result: %v", task.Result)
	}
	return sp
}

// endedAgo moves a done task's end into the past, on the database's clock.
func (s *stack) endedAgo(t *testing.T, taskID uuid.UUID, minutes int) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`UPDATE backlog_tasks SET finished_at = now() - make_interval(mins => $2) WHERE id=$1`,
		taskID, minutes); err != nil {
		t.Fatal(err)
	}
}

func decisionOf(t *testing.T, task backlog.Task) string {
	t.Helper()
	if task.SessionDecision == nil {
		return ""
	}
	d := *task.SessionDecision
	if task.SessionReason != nil {
		d += "/" + *task.SessionReason
	}
	return d
}

const handoverHead = "## Handover from your previous task on "

// AC 1: task B on the topic of task A, ten minutes after A ended, resumes A's
// session, in A's directory, with A's system prompt to the byte — although a
// fresh start of B would have had another one: the wiki has a page more since,
// and B names a stopped colleague, which adds a section to the compiled
// prompt. The new page goes into the resume message, beside B's title and
// body.
func TestStickyResumeOnTopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("sticky-dev", true)

	a := s.onTopic(t, dev.ID, "#42 implement parser", "[mock:spec]", "pr:42")
	if a.TopicKey == nil || *a.TopicKey != "pr:42" {
		t.Fatalf("topic not stored: %v", a.TopicKey)
	}
	if a.RuntimeSessionID == nil || *a.RuntimeSessionID != "mock-session-"+a.ID.String() {
		t.Fatalf("the session is stored at done: %v", a.RuntimeSessionID)
	}
	if a.FinishedAt == nil || a.LastTurnContextTokens == nil || *a.LastTurnContextTokens != 44_000 {
		t.Fatalf("finish time and last turn's context are stored at done: %v %v", a.FinishedAt, a.LastTurnContextTokens)
	}
	if d := decisionOf(t, a); d != "fresh/no_previous" {
		t.Fatalf("the first task on a topic starts fresh, recorded %q", d)
	}
	specA := specOf(t, a)
	if filepath.Base(specA.WorkDir) != "topic-sticky-dev-pr-42" {
		t.Fatalf("the run works in the topic's directory, not %s", specA.WorkDir)
	}
	if specA.ResumeSessionID != "" || specA.Handover != "" {
		t.Fatalf("a first task resumes nothing and hands nothing over: %+v", specA)
	}

	paused := s.newSupportAgent("paused-colleague")
	if err := s.registry.SetKilled(ctx, paused.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mem.Write(ctx, dev.ID, memory.PageInput{
		Slug: "parser-edge-cases", Title: "Parser edge cases", Body: "Empty input is valid.", Source: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	s.endedAgo(t, a.ID, 10)

	b := s.onTopic(t, dev.ID, "#42 fix review round 1",
		"Finding 1: off by one. Ask paused-colleague.\n[mock:spec]", "pr:42")
	specB := specOf(t, b)
	if specB.ResumeSessionID != *a.RuntimeSessionID {
		t.Fatalf("B resumes A's session %s, got %q", *a.RuntimeSessionID, specB.ResumeSessionID)
	}
	if specB.WorkDir != specA.WorkDir {
		t.Fatalf("B works in A's directory %s, got %s", specA.WorkDir, specB.WorkDir)
	}
	if specB.SystemPrompt != specA.SystemPrompt || specB.MemoryContext != specA.MemoryContext {
		t.Fatal("B's system prompt is not A's byte for byte")
	}
	if strings.Contains(specB.MemoryContext, "parser-edge-cases") {
		t.Fatal("the new wiki page must not enter the resumed system prompt")
	}
	for _, want := range []string{"#42 fix review round 1", "Finding 1: off by one.", "parser-edge-cases"} {
		if !strings.Contains(specB.ResumeInput, want) {
			t.Fatalf("the resume message carries %q: %q", want, specB.ResumeInput)
		}
	}
	if strings.Contains(specB.ResumeInput, "The event you were waiting for") {
		t.Fatal("a sticky resume is the next task, not a woken wait")
	}
	if d := decisionOf(t, b); d != "sticky_resumed" {
		t.Fatalf("recorded %q, want sticky_resumed", d)
	}
}

// reviewComment files the reviewer's round comment on PR n into the
// recording, as the github plugin's comment_pr leaves it.
func (s *stack) reviewComment(t *testing.T, reviewer uuid.UUID, n int, body string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"action": "github:comment_pr", "ok": true,
		"params": map[string]any{"repo": "example/app", "pr_number": n, "body": body}})
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO recording_events (org_id, agent_id, kind, payload)
		VALUES ($1,$2,'action',$3)`, s.orgID, reviewer, payload); err != nil {
		t.Fatal(err)
	}
}

// AC 2: 56 minutes after A, past the 55-minute window, B starts fresh and
// opens with the handover: A's result and the PR's latest review comment.
func TestStickyFreshAfterWindow(t *testing.T) {
	s := newStack(t)
	dev := s.stickyAgent("window-dev", true)
	reviewer := s.stickyAgent("window-reviewer", false)
	s.ohneLaeufe(reviewer.ID)

	a := s.onTopic(t, dev.ID, "#42 implement parser", "[mock:result Parser done, tests green]", "pr:42")
	s.reviewComment(t, reviewer.ID, 42, "## Review round 0\n\nAn older round.")
	s.reviewComment(t, reviewer.ID, 41, "## Review round 1\n\nAnother PR.")
	s.reviewComment(t, reviewer.ID, 42, "## Review round 1\n\n1. major: the parser drops the last token.")
	s.reviewComment(t, reviewer.ID, 42, "Thanks, will look.") // no round marker
	s.endedAgo(t, a.ID, 56)

	b := s.onTopic(t, dev.ID, "#42 fix review round 1", "[mock:spec]", "pr:42")
	sp := specOf(t, b)
	if sp.ResumeSessionID != "" {
		t.Fatalf("past the window nothing is resumed, got %q", sp.ResumeSessionID)
	}
	if !strings.HasPrefix(sp.Prompt, handoverHead+"pr:42") {
		t.Fatalf("the prompt starts with the handover: %q", sp.Prompt)
	}
	for _, want := range []string{"Parser done, tests green", "1. major: the parser drops the last token."} {
		if !strings.Contains(sp.Handover, want) {
			t.Fatalf("the handover carries %q: %q", want, sp.Handover)
		}
	}
	for _, not := range []string{"An older round", "Another PR", "will look"} {
		if strings.Contains(sp.Handover, not) {
			t.Fatalf("the handover carries only the latest round comment on this PR, found %q", not)
		}
	}
	if d := decisionOf(t, b); d != "fresh/window" {
		t.Fatalf("recorded %q, want fresh/window", d)
	}
}

// AC 3: the cap is on the previous task's last turn: 200,001 tokens start
// fresh with the handover, 199,999 resume.
func TestStickyContextCap(t *testing.T) {
	s := newStack(t)
	dev := s.stickyAgent("cap-dev", true)
	setContext := func(id uuid.UUID, n int64) {
		if _, err := s.pool.Exec(context.Background(),
			`UPDATE backlog_tasks SET last_turn_context_tokens=$2 WHERE id=$1`, id, n); err != nil {
			t.Fatal(err)
		}
	}

	over := s.onTopic(t, dev.ID, "#42 implement", "[mock:result big]", "pr:42")
	setContext(over.ID, 200_001)
	s.endedAgo(t, over.ID, 10)
	b := s.onTopic(t, dev.ID, "#42 fix", "[mock:spec]", "pr:42")
	if sp := specOf(t, b); sp.ResumeSessionID != "" || !strings.HasPrefix(sp.Prompt, handoverHead) {
		t.Fatalf("over the cap: fresh with the handover, got %+v", sp)
	}
	if d := decisionOf(t, b); d != "fresh/cap" {
		t.Fatalf("recorded %q, want fresh/cap", d)
	}

	under := s.onTopic(t, dev.ID, "#43 implement", "[mock:result small]", "pr:43")
	setContext(under.ID, 199_999)
	s.endedAgo(t, under.ID, 10)
	c := s.onTopic(t, dev.ID, "#43 fix", "[mock:spec]", "pr:43")
	if sp := specOf(t, c); sp.ResumeSessionID != *under.RuntimeSessionID {
		t.Fatalf("under the cap: resumed, got %+v", sp)
	}
	if d := decisionOf(t, c); d != "sticky_resumed" {
		t.Fatalf("recorded %q, want sticky_resumed", d)
	}
}

// AC 4: what is not the same agent on the same topic starts fresh without a
// handover; stickiness off and a missing transcript start fresh with one.
func TestStickyFreshCases(t *testing.T) {
	s := newStack(t)
	dev := s.stickyAgent("fresh-dev", true)
	other := s.stickyAgent("fresh-other", true)
	off := s.stickyAgent("fresh-off", false)

	a := s.onTopic(t, dev.ID, "#42 implement", "[mock:result A]", "pr:42")
	s.endedAgo(t, a.ID, 10)

	noHandover := func(name string, task backlog.Task, want string) {
		t.Helper()
		sp := specOf(t, task)
		if sp.ResumeSessionID != "" || sp.Handover != "" {
			t.Fatalf("%s: fresh without a handover, got %+v", name, sp)
		}
		if d := decisionOf(t, task); d != want {
			t.Fatalf("%s: recorded %q, want %q", name, d, want)
		}
	}
	noHandover("another topic", s.onTopic(t, dev.ID, "#7 implement", "[mock:spec]", "issue:7"), "fresh/no_previous")
	noHandover("another agent", s.onTopic(t, other.ID, "#42 look", "[mock:spec]", "pr:42"), "fresh/no_previous")
	plain := s.onTopic(t, dev.ID, "#42 without topic", "[mock:spec]", "")
	noHandover("no topic", plain, "")
	if sp := specOf(t, plain); strings.HasPrefix(filepath.Base(sp.WorkDir), "topic-") {
		t.Fatalf("a task without a topic works in its own directory, not %s", sp.WorkDir)
	}

	withHandover := func(name string, task backlog.Task, want string) {
		t.Helper()
		sp := specOf(t, task)
		if sp.ResumeSessionID != "" || !strings.HasPrefix(sp.Prompt, handoverHead) {
			t.Fatalf("%s: fresh with the handover, got %+v", name, sp)
		}
		if d := decisionOf(t, task); d != want {
			t.Fatalf("%s: recorded %q, want %q", name, d, want)
		}
	}
	offA := s.onTopic(t, off.ID, "#42 review", "[mock:result reviewed]", "pr:42")
	s.endedAgo(t, offA.ID, 10)
	withHandover("stickiness off", s.onTopic(t, off.ID, "#42 review round 2", "[mock:spec]", "pr:42"), "fresh/off")

	// The transcript is gone from the topic's directory: the control plane
	// asks for the resume, the daemon cannot, and says so.
	last := s.onTopic(t, dev.ID, "#42 again", "[mock:result B]", "pr:42")
	s.endedAgo(t, last.ID, 10)
	dir := filepath.Join(s.homeBase, dev.ID.String(), "scratch", daemon.TopicDir(dev.Slug, "pr:42"))
	if err := os.RemoveAll(filepath.Join(dir, ".mock-sessions")); err != nil {
		t.Fatal(err)
	}
	withHandover("no transcript", s.onTopic(t, dev.ID, "#42 once more", "[mock:spec]", "pr:42"), "fresh/no_session")
}

// AC 5: Block and Complete leave topic_key alone, and blocked → working
// resumes the task's own session as before, in the topic's directory.
func TestTopicKeptThroughBlock(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.stickyAgent("block-dev", true)
	task, err := s.backlog.CreateOnTopic(ctx, s.orgID, dev.ID, "#42 implement",
		"[mock:block key=answer:42 question=Which parser?]\n[mock:spec]", "manual", 3, "pr:42")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "blocked", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateBlocked })
	blocked, _ := s.backlog.Get(ctx, task.ID)
	if blocked.TopicKey == nil || *blocked.TopicKey != "pr:42" {
		t.Fatalf("Block cleared the topic: %v", blocked.TopicKey)
	}
	if blocked.CorrelationKey == nil || *blocked.CorrelationKey != "answer:42" {
		t.Fatalf("the correlation key is the wait's: %v", blocked.CorrelationKey)
	}
	if _, err := s.backlog.Answer(ctx, task.ID, "ada", "The new one."); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
	done, _ := s.backlog.Get(ctx, task.ID)
	if done.TopicKey == nil || *done.TopicKey != "pr:42" {
		t.Fatalf("Complete cleared the topic: %v", done.TopicKey)
	}
	sp := specOf(t, done)
	if sp.ResumeSessionID != *blocked.RuntimeSessionID || !strings.Contains(sp.ResumeInput, "The new one.") {
		t.Fatalf("the blocked task resumes its own session with the answer: %+v", sp)
	}
	if filepath.Base(sp.WorkDir) != "topic-block-dev-pr-42" {
		t.Fatalf("the resume works in the topic's directory, not %s", sp.WorkDir)
	}
}

// AC 6: covey/create_task and the API store a topic of the form pr:<n> or
// issue:<n>, and refuse anything else with the format in the error.
func TestCreateTaskTopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	dev := s.newSupportAgent("topic-creator")

	parent := s.onTopic(t, dev.ID, "split work",
		`[mock:action covey/create_task {"title":"fix it","body":"the rest","topic":"pr:42"}]`, "")
	child := childOf(t, s, dev.ID, parent.ID)
	if child.TopicKey == nil || *child.TopicKey != "pr:42" {
		t.Fatalf("create_task stores the topic: %v", child.TopicKey)
	}

	long := "pr:" + strings.Repeat("1", 70)
	for _, bad := range []string{"../x", long, "pull:4", "pr:4x", "issue:"} {
		params, _ := json.Marshal(map[string]string{"title": "bad " + bad, "body": "b", "topic": bad})
		task, err := s.backlog.Create(ctx, s.orgID, dev.ID, "refused "+bad,
			"[mock:action covey/create_task "+string(params)+"]", "manual", 3)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "refused", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateFailed })
		failed, _ := s.backlog.Get(ctx, task.ID)
		if failed.Error == nil || !strings.Contains(*failed.Error, `"pr:<number>" or "issue:<number>"`) {
			t.Fatalf("%q: the refusal says the format, got %v", bad, failed.Error)
		}
	}

	admin := login(t, s, "admin@test.local", "admin-passwort")
	base := "/api/v1/agents/" + dev.ID.String()
	s.ohneLaeufe(dev.ID)
	admin.expect(http.MethodPost, base+"/tasks", map[string]any{"title": "api", "topic": "issue:7"}, http.StatusCreated)
	admin.expect(http.MethodPost, base+"/tasks", map[string]any{"title": "api bad", "topic": "../x"}, http.StatusBadRequest)
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM backlog_tasks WHERE agent_id=$1 AND topic_key='issue:7'`,
		dev.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the API stores the topic: %d, %v", n, err)
	}

	if !strings.Contains(agents.CoveyActionsDoc, `"topic"`) {
		t.Fatal("the protocol text names the topic parameter")
	}
}

// AC 7: stickiness is off for a new agent and for every agent the migration
// finds, except covey-dev and covey-frontend; covey-reviewer stays off.
func TestStickyDefaults(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	fresh := s.newSupportAgent("brand-new")
	if fresh.StickySessions || fresh.StickyWindowMinutes != 55 || fresh.StickyContextCap != 200_000 {
		t.Fatalf("a new agent: off, 55 minutes, 200,000 tokens; got %v %d %d",
			fresh.StickySessions, fresh.StickyWindowMinutes, fresh.StickyContextCap)
	}

	slugs := []string{"covey-dev", "covey-frontend", "covey-reviewer", "covey-qa", "support"}
	ids := map[string]uuid.UUID{}
	for _, slug := range slugs {
		ids[slug] = s.newSupportAgent(slug).ID
		s.ohneLaeufe(ids[slug])
	}
	version := func() int {
		var v int
		if err := s.pool.QueryRow(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for version() >= 154 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, slug := range slugs {
		a, err := s.registry.Get(ctx, ids[slug])
		if err != nil {
			t.Fatal(err)
		}
		want := slug == "covey-dev" || slug == "covey-frontend"
		if a.StickySessions != want || a.StickyWindowMinutes != 55 || a.StickyContextCap != 200_000 {
			t.Fatalf("%s after the migration: sticky %v (want %v), %d min, %d tokens",
				slug, a.StickySessions, want, a.StickyWindowMinutes, a.StickyContextCap)
		}
	}

	// The setting over the API, with its bounds.
	admin := login(t, s, "admin@test.local", "admin-passwort")
	base := "/api/v1/agents/" + fresh.ID.String() + "/sticky-sessions"
	set := func(on bool, window, limit int) map[string]any {
		return map[string]any{"sticky_sessions": on, "sticky_window_minutes": window, "sticky_context_cap": limit}
	}
	admin.expect(http.MethodPatch, base, set(true, 30, 150_000), http.StatusOK)
	admin.expect(http.MethodPatch, base, set(true, 0, 150_000), http.StatusBadRequest)
	admin.expect(http.MethodPatch, base, set(true, 30, 1_000_001), http.StatusBadRequest)
	got, _ := s.registry.Get(ctx, fresh.ID)
	if !got.StickySessions || got.StickyWindowMinutes != 30 || got.StickyContextCap != 150_000 {
		t.Fatalf("the API stores the setting: %+v", got)
	}
}
