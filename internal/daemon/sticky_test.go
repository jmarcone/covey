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
	for _, bad := range []string{"../topic-x", "topic-a/b", "topic-..", "topic-A", "topic-", "x-topic-a", "topic-a.b"} {
		if dir := topicScratchDir(home, bad); dir != "" {
			t.Errorf("%q must not become a directory, got %q", bad, dir)
		}
	}
	dir := topicScratchDir(home, "topic-covey-dev-pr-42")
	if dir != filepath.Join(home, scratchRoot, "topic-covey-dev-pr-42") {
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
	old := topicScratchDir(home, "topic-dev-pr-1")
	fresh := topicScratchDir(home, "topic-dev-pr-2")
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
	a := withScratch("P", "/h/scratch/topic-dev-pr-42")
	b := withScratch("P", "/other/home/scratch/topic-dev-pr-42")
	if a != b || !strings.Contains(a, "~/scratch/topic-dev-pr-42/") {
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
	topicDir := topicScratchDir(home, "topic-dev-pr-42")
	otherDir := filepath.Join(home, scratchRoot, "11111111-1111-1111-1111-111111111111")
	task := AssignTask{TaskID: "t", Title: "B", Body: "handover + body", StickySessionID: "sess-a",
		StickyInput: "B's message", FallbackSystemPrompt: "B's own prompt", FallbackMemoryContext: "B's memory"}
	base := RunSpec{HomeDir: home, WorkDir: topicDir, SystemPrompt: "A's prompt", MemoryContext: "A's memory"}

	// No transcript: fresh, under the fallback prompt.
	spec := base
	out := applySticky(Mock{}, &spec, task, topicDir)
	if out.Decision != "fresh" || out.Reason != "no_session" || spec.ResumeSessionID != "" {
		t.Fatalf("without the session: %+v, resume %q", out, spec.ResumeSessionID)
	}
	if spec.MemoryContext != "B's memory" || !strings.HasPrefix(spec.SystemPrompt, "B's own prompt") ||
		!strings.Contains(spec.SystemPrompt, "topic-dev-pr-42") {
		t.Fatalf("fallback prompt not used: %q / %q", spec.SystemPrompt, spec.MemoryContext)
	}

	// A run in ANOTHER directory left the session: still not found here.
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "a", HomeDir: home, WorkDir: otherDir,
		ResumeSessionID: "sess-a"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	spec = base
	if out := applySticky(Mock{}, &spec, task, topicDir); out.Decision != "fresh" {
		t.Fatalf("a session from another directory must not be resumed: %+v", out)
	}

	// A run in the topic dir left it: resumed, under the pinned prompt.
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "a", HomeDir: home, WorkDir: topicDir,
		ResumeSessionID: "sess-a"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	spec = base
	out = applySticky(Mock{}, &spec, task, topicDir)
	if out.Decision != "sticky_resumed" || spec.ResumeSessionID != "sess-a" || spec.ResumeInput != "B's message" {
		t.Fatalf("with the session: %+v %+v", out, spec)
	}
	if spec.SystemPrompt != "A's prompt" || spec.MemoryContext != "A's memory" {
		t.Fatal("a resume keeps the session's prompt")
	}

	// A runtime that keeps no sessions it can show: fresh.
	spec = base
	if out := applySticky(noSessions{}, &spec, task, topicDir); out.Decision != "fresh" {
		t.Fatalf("runtime without SessionHolder: %+v", out)
	}
	// No sticky request: nothing happens.
	spec = base
	if out := applySticky(Mock{}, &spec, AssignTask{}, topicDir); out.Decision != "" || spec.SystemPrompt != "A's prompt" {
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
	topicDir := topicScratchDir(home, "topic-dev-pr-1")
	taskDir := scratchDir(home, "22222222-2222-2222-2222-222222222222")
	if got := resumeDir(Mock{}, home, topicDir, taskDir, ""); got != topicDir {
		t.Fatalf("no resume: topic dir, got %q", got)
	}
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "t", HomeDir: home, WorkDir: taskDir,
		ResumeSessionID: "old"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	if got := resumeDir(Mock{}, home, topicDir, taskDir, "old"); got != taskDir {
		t.Fatalf("the session lies in the task dir: got %q", got)
	}
	if _, err := (Mock{}).Run(context.Background(), RunSpec{TaskID: "t", HomeDir: home, WorkDir: topicDir,
		ResumeSessionID: "old"}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	if got := resumeDir(Mock{}, home, topicDir, taskDir, "old"); got != topicDir {
		t.Fatalf("in both: the topic dir wins, got %q", got)
	}
}
