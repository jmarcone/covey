package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidTopicAndTopicDir(t *testing.T) {
	for _, s := range []string{"pr:1", "issue:42"} {
		if !ValidTopic(s) {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range []string{"../x", "pr:0", "pr:", "issue:4a", "pr:" + strings.Repeat("1", 64)} {
		if ValidTopic(s) {
			t.Errorf("%q must be refused", s)
		}
	}
	home := t.TempDir()
	for _, bad := range []string{"../topic-x", "topic-a/b", "topic-..", "topic-A", "topic-", "x-topic-a", "topic-a.b",
		"topic-covey-dev-pr-42", "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-0", "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-mr-4", "topic-6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B-pr-4"} {
		if dir := mustTopicDir(home, bad); dir != "" {
			t.Errorf("%q must not become a directory, got %q", bad, dir)
		}
	}
	dir := mustTopicDir(home, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42")
	if dir != filepath.Join(home, scratchRoot, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42") {
		t.Fatalf("topic dir: %q", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("topic dir not created: %v", err)
	}
}

// A topic's directory ages like a task's: removed a week after the last run
// that entered it, kept otherwise.
func TestSweepScratchAgesTopicDirs(t *testing.T) {
	home := t.TempDir()
	old := mustTopicDir(home, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-1")
	fresh := mustTopicDir(home, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-2")
	foreign := filepath.Join(home, scratchRoot, "topic-Not.Ours")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-scratchKeep - time.Hour)
	for _, d := range []string{old, foreign} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := sweepScratch(home, time.Now(), ""); err != nil || n != 1 {
		t.Fatalf("swept %d (%v), want 1", n, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the stale topic dir must go")
	}
	for _, d := range []string{fresh, foreign} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s must stay: %v", d, err)
		}
	}
}

// The text naming a topic dir is the same for every task of the topic: it is
// part of the system prompt a resume must repeat byte for byte.
func TestScratchDocTopicIsStable(t *testing.T) {
	a := withScratch("P", "/h/scratch/topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42")
	b := withScratch("P", "/other/home/scratch/topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42")
	if a != b || !strings.Contains(a, "~/scratch/topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42/") {
		t.Fatalf("topic scratch text differs or misses the dir:\n%s\n%s", a, b)
	}
	if strings.Contains(a, "belongs to this task") {
		t.Fatal("a topic dir is not one task's")
	}
}

// applySticky resumes only when the runtime holds the session in the run's
// working directory, and otherwise falls back to the task's own prompt.
func TestApplySticky(t *testing.T) {
	home := t.TempDir()
	topicDir := mustTopicDir(home, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42")
	otherDir := filepath.Join(home, scratchRoot, "11111111-1111-1111-1111-111111111111")
	task := AssignTask{TaskID: "t", Title: "B", Body: "handover + body", StickySessionID: "sess-a",
		StickyInput: "B's message", FallbackSystemPrompt: "B's own prompt", FallbackMemoryContext: "B's memory"}
	base := RunSpec{HomeDir: home, WorkDir: topicDir, SystemPrompt: "A's prompt", MemoryContext: "A's memory"}

	// No transcript: fresh, under the fallback prompt.
	spec := base
	out := applySticky(Mock{}, &spec, task, topicDir, true)
	if out.Decision != "fresh" || out.Reason != "no_session" || spec.ResumeSessionID != "" {
		t.Fatalf("without the session: %+v, resume %q", out, spec.ResumeSessionID)
	}
	if spec.MemoryContext != "B's memory" || !strings.HasPrefix(spec.SystemPrompt, "B's own prompt") ||
		!strings.Contains(spec.SystemPrompt, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-42") {
		t.Fatalf("fallback prompt not used: %q / %q", spec.SystemPrompt, spec.MemoryContext)
	}

	// A run in ANOTHER directory left the session: still not found here.
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "a", HomeDir: home, WorkDir: otherDir,
		ResumeSessionID: "sess-a"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	spec = base
	if out := applySticky(Mock{}, &spec, task, topicDir, true); out.Decision != "fresh" {
		t.Fatalf("a session from another directory must not be resumed: %+v", out)
	}

	// A run in the topic dir left it: resumed, under the pinned prompt.
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "a", HomeDir: home, WorkDir: topicDir,
		ResumeSessionID: "sess-a"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	spec = base
	out = applySticky(Mock{}, &spec, task, topicDir, true)
	if out.Decision != "sticky_resumed" || spec.ResumeSessionID != "sess-a" || spec.ResumeInput != "B's message" {
		t.Fatalf("with the session: %+v %+v", out, spec)
	}
	if spec.SystemPrompt != "A's prompt" || spec.MemoryContext != "A's memory" {
		t.Fatal("a resume keeps the session's prompt")
	}

	// A runtime that keeps no sessions it can show: fresh.
	spec = base
	if out := applySticky(noSessions{}, &spec, task, topicDir, true); out.Decision != "fresh" {
		t.Fatalf("runtime without SessionHolder: %+v", out)
	}
	// No sticky request: nothing happens.
	spec = base
	if out := applySticky(Mock{}, &spec, AssignTask{}, topicDir, true); out.Decision != "" || spec.SystemPrompt != "A's prompt" {
		t.Fatalf("no request: %+v", out)
	}
}

type noSessions struct{ Mock }

func (noSessions) HasSession() {} // shadows nothing: noSessions is not a SessionHolder

var _ Runtime = noSessions{}

// A topic task resuming its own session that lies in the task's own
// directory (started by an older daemon) resumes there, not in the topic's.
func TestResumeDirFallsBackToTheTaskDir(t *testing.T) {
	home := t.TempDir()
	topicDir := mustTopicDir(home, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-1")
	taskDir := scratchDir(home, "22222222-2222-2222-2222-222222222222")
	if got := runDirFor(Mock{}, home, "", topicDir, taskDir); got != topicDir {
		t.Fatalf("no resume: topic dir, got %q", got)
	}
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "t", HomeDir: home, WorkDir: taskDir,
		ResumeSessionID: "old"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	if got := runDirFor(Mock{}, home, "old", topicDir, taskDir); got != taskDir {
		t.Fatalf("the session lies in the task dir: got %q", got)
	}
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "t", HomeDir: home, WorkDir: topicDir,
		ResumeSessionID: "old"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	if got := runDirFor(Mock{}, home, "old", topicDir, taskDir); got != topicDir {
		t.Fatalf("in both: the topic dir wins, got %q", got)
	}
}

func mustTopicDir(home, key string) string {
	dir, _ := topicScratchDir(home, key)
	return dir
}

// A topic directory the sweep removed and the run made again holds none of
// the files the session worked on: never resume into it, even though the
// transcript under ~/.claude survived.
func TestApplyStickyNotIntoASweptDir(t *testing.T) {
	home := t.TempDir()
	key := "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-9"
	dir, existed := topicScratchDir(home, key)
	if dir == "" || existed {
		t.Fatalf("first time: made, not existing (%q, %v)", dir, existed)
	}
	if _, existed := topicScratchDir(home, key); !existed {
		t.Fatal("second time: it exists")
	}
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "a", HomeDir: home, WorkDir: dir,
		ResumeSessionID: "sess-a"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	task := AssignTask{TaskID: "t", StickySessionID: "sess-a", StickyInput: "in", FallbackSystemPrompt: "own"}
	spec := RunSpec{HomeDir: home, WorkDir: dir, SystemPrompt: "pinned"}
	if out := applySticky(Mock{}, &spec, task, dir, false); out.Decision != "fresh" || out.Reason != "no_session" || spec.ResumeSessionID != "" {
		t.Fatalf("a recreated dir must not be resumed into: %+v", out)
	}
	spec = RunSpec{HomeDir: home, WorkDir: dir, SystemPrompt: "pinned"}
	if out := applySticky(Mock{}, &spec, task, dir, true); out.Decision != "sticky_resumed" {
		t.Fatalf("an existing dir with the session resumes: %+v", out)
	}
}

// A continuation's session lies in its chain's first task's directory: the
// run goes there, unless the topic's directory holds it.
func TestRunDirForTheChainRoot(t *testing.T) {
	home := t.TempDir()
	topicDir := mustTopicDir(home, "topic-6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b-pr-3")
	rootDir := scratchDir(home, "33333333-3333-3333-3333-333333333333")
	own := taskScratchPath(home, "44444444-4444-4444-4444-444444444444")
	if got := runDirFor(Mock{}, home, "", "", rootDir, own); got != rootDir {
		t.Fatalf("no resume, no topic: the root's dir first, got %q", got)
	}
	if got := runDirFor(Mock{}, home, "s", topicDir, rootDir, own); got != topicDir {
		t.Fatalf("no candidate holds it: the topic dir, got %q", got)
	}
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "r", HomeDir: home, WorkDir: rootDir,
		ResumeSessionID: "s"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	if got := runDirFor(Mock{}, home, "s", topicDir, rootDir, own); got != rootDir {
		t.Fatalf("the session lies in the root's dir: got %q", got)
	}
}
