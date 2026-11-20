package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// AC 6: a topic is pr:<n> or issue:<n>, at most 64 characters; anything else
// is refused with the format in the error.
func TestParseTopic(t *testing.T) {
	for in, want := range map[string]string{"": "", "pr:42": "pr:42", " issue:7 ": "issue:7"} {
		got, err := ParseTopic(in)
		if err != nil || got != want {
			t.Errorf("ParseTopic(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"../x", "pr:" + strings.Repeat("1", 62), "pull:4", "pr:4x", "issue:", "PR:4", "pr:4/..", "pr: 4"} {
		if _, err := ParseTopic(in); !errors.Is(err, ErrTopic) {
			t.Errorf("ParseTopic(%q) must be refused, got %v", in, err)
		}
	}
	if !strings.Contains(ErrTopic.Error(), `"pr:<number>" or "issue:<number>"`) {
		t.Fatalf("the refusal says the format: %s", ErrTopic)
	}
	// 64 characters is the limit, 65 is over it.
	if _, err := ParseTopic("pr:" + strings.Repeat("1", 61)); err != nil {
		t.Fatalf("64 characters are allowed: %v", err)
	}
}

// The directory's name carries no path separator, whatever the slug holds, and
// what TopicDir makes is what the sweep and workDir accept.
func TestTopicDir(t *testing.T) {
	if got := TopicDir("covey-dev", "pr:42"); got != "topic-covey-dev-pr-42" {
		t.Fatalf("TopicDir = %q", got)
	}
	if got := TopicDir("../Ada", "issue:7"); strings.ContainsAny(got, `/\.`) || !isTopicDir(got) {
		t.Fatalf("TopicDir of a hostile slug = %q", got)
	}
	for _, bad := range []string{"topic-", "topic-../x", "topic-a/b", "x-topic-a", "topic-" + strings.Repeat("a", 130)} {
		if isTopicDir(bad) {
			t.Errorf("isTopicDir(%q) must be false", bad)
		}
	}
	home := t.TempDir()
	if dir := workDir(home, taskA, "topic-../../etc"); filepath.Base(dir) != taskA {
		t.Fatalf("an invalid topic directory falls back to the task's own: %s", dir)
	}
	if dir := workDir(home, taskA, "topic-ada-pr-1"); dir != filepath.Join(home, "scratch", "topic-ada-pr-1") {
		t.Fatalf("workDir = %s", dir)
	}
}

// A topic's directory is aged like a task's: by the last run that entered it.
func TestSweepAgesTopicDirectories(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	oldDir := workDir(home, taskA, "topic-ada-pr-1")
	newDir := workDir(home, taskA, "topic-ada-pr-2")
	old := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldDir, old, old); err != nil {
		t.Fatal(err)
	}
	if n, err := sweepScratch(home, now, ""); n != 1 || err != nil {
		t.Fatalf("removed %d, %v", n, err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("the stale topic directory stays")
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatal("the recent topic directory is gone")
	}
}

// keeper is a runtime that holds the sessions it is told of.
type keeper struct {
	Mock
	sessions []string
}

func (k keeper) HasSession(home, dir, id string) bool { return slices.Contains(k.sessions, id) }

// noKeeper is a runtime without sessions across tasks.
type noKeeper struct{ Runtime }

func topicSpec(t *testing.T) RunSpec {
	home := t.TempDir()
	dir := workDir(home, taskA, "topic-ada-pr-1")
	return RunSpec{TaskID: taskA, Title: "T", Body: "B", SystemPrompt: "first prompt",
		MemoryContext: "first wiki", HomeDir: home, WorkDir: dir, Topic: "pr:1"}
}

// The first run on a topic leaves its system prompt on record; a sticky
// resume sends that one and puts the new task with its wiki hits into the
// resume message.
func TestStickyResumeSendsTheRecordedPrompt(t *testing.T) {
	first := topicSpec(t)
	start, onTopic := startOnTopic(&first, keeper{}, AssignTask{Topic: "pr:1", Handover: ""})
	if start.Decision != "" || !onTopic {
		t.Fatalf("a fresh start the control plane decided reports nothing: %+v", start)
	}
	endOnTopic(first.WorkDir, "sess-1")

	next := first
	next.SystemPrompt, next.MemoryContext = "second prompt", "second wiki"
	task := AssignTask{Title: "Fix round", Body: "Finding 1", MemoryContext: "second wiki",
		Topic: "pr:1", StickySessionID: "sess-1", Handover: "## Handover"}
	start, _ = startOnTopic(&next, keeper{sessions: []string{"sess-1"}}, task)
	if start.Decision != "sticky_resumed" {
		t.Fatalf("decision %+v", start)
	}
	if next.SystemPrompt != "first prompt" || next.MemoryContext != "first wiki" {
		t.Fatalf("the resumed run sends the session's prompt: %q %q", next.SystemPrompt, next.MemoryContext)
	}
	if next.ResumeSessionID != "sess-1" || next.Handover != "" {
		t.Fatalf("resume %q, handover %q", next.ResumeSessionID, next.Handover)
	}
	if next.ResumeInput != "second wiki\n\n---\n\nFix round\n\nFinding 1" {
		t.Fatalf("resume message %q", next.ResumeInput)
	}

	// The prompt sent is the session's, so claude-code's arguments are the
	// first run's but for --resume and the message.
	c := &ClaudeCode{}
	argsFirst, _ := c.buildArgs(first)
	argsNext, prompt := c.buildArgs(next)
	if prompt != next.ResumeInput || !slices.Contains(argsNext, "--resume") {
		t.Fatalf("args %v", argsNext)
	}
	sys := func(args []string) string { return args[slices.Index(args, "--append-system-prompt")+1] }
	if sys(argsFirst) != sys(argsNext) {
		t.Fatal("the appended system prompt differs between the first run and the sticky resume")
	}
}

// AC 4, the daemon's half: without the transcript, without the record, with a
// record of another session or on a runtime without sessions, the run starts
// fresh with the handover and says no_session.
func TestStickyResumeFallsBackToFresh(t *testing.T) {
	cases := map[string]func(spec RunSpec) Runtime{
		"no transcript": func(spec RunSpec) Runtime {
			startOnTopic(&spec, keeper{}, AssignTask{Topic: "pr:1"})
			endOnTopic(spec.WorkDir, "sess-1")
			return keeper{}
		},
		"no record": func(spec RunSpec) Runtime { return keeper{sessions: []string{"sess-1"}} },
		"record of another session": func(spec RunSpec) Runtime {
			startOnTopic(&spec, keeper{}, AssignTask{Topic: "pr:1"})
			endOnTopic(spec.WorkDir, "sess-0")
			return keeper{sessions: []string{"sess-1"}}
		},
		"runtime without sessions": func(spec RunSpec) Runtime {
			startOnTopic(&spec, keeper{}, AssignTask{Topic: "pr:1"})
			endOnTopic(spec.WorkDir, "sess-1")
			return noKeeper{Mock{}}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			spec := topicSpec(t)
			rt := setup(spec)
			spec.SystemPrompt = "own prompt"
			start, _ := startOnTopic(&spec, rt, AssignTask{Topic: "pr:1", StickySessionID: "sess-1", Handover: "## Handover x"})
			if start.Decision != "fresh" || start.Reason != "no_session" {
				t.Fatalf("decision %+v", start)
			}
			if spec.ResumeSessionID != "" || spec.Handover != "## Handover x" || spec.SystemPrompt != "own prompt" {
				t.Fatalf("fresh with the handover and its own prompt: %+v", spec)
			}
			if !strings.HasPrefix(taskText(spec), "## Handover x\n\n---\n\nT\n\nB") {
				t.Fatalf("the prompt opens with the handover: %q", taskText(spec))
			}
			// And its own prompt goes on record for the next task.
			if rec, ok := loadSessionRecord(spec.WorkDir); !ok || rec.SystemPrompt != "own prompt" || rec.SessionID != "" {
				t.Fatalf("record %+v", rec)
			}
		})
	}
}

// A resume of the task's own session (blocked → working, the turn-limit
// continuation) is not a decision of this kind and goes on as before.
func TestOwnResumeIsLeftAlone(t *testing.T) {
	spec := topicSpec(t)
	spec.ResumeSessionID, spec.ResumeInput = "own", "the answer"
	start, onTopic := startOnTopic(&spec, keeper{sessions: []string{"x"}},
		AssignTask{Topic: "pr:1", ResumeSessionID: "own", StickySessionID: "x", Handover: "h"})
	if start.Decision != "" || onTopic || spec.ResumeSessionID != "own" || spec.Handover != "" {
		t.Fatalf("own resume touched: %+v %+v", start, spec)
	}
}

// The context of the last turn is the main session's last assistant line:
// input + cache read + cache write. A sub-agent's turn is not the session's.
func TestClaudeCodeReportsTheLastTurnsContext(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"s"}
{"type":"assistant","message":{"model":"m","usage":{"input_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200}}}
{"type":"assistant","message":{"model":"m","usage":{"input_tokens":5,"cache_read_input_tokens":1200,"cache_creation_input_tokens":300}}}
{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"model":"m","usage":{"input_tokens":1,"cache_read_input_tokens":90000,"cache_creation_input_tokens":0}}}
{"type":"result","subtype":"success","session_id":"s","result":"COVEY_STATUS: {\"status\":\"done\",\"result\":\"ok\"}","total_cost_usd":0.1,"usage":{"input_tokens":15}}
EOF`)
	res, err := (&ClaudeCode{Binary: bin}).Run(context.Background(),
		RunSpec{TaskID: "t", Title: "x", HomeDir: dir}, func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContextTokens != 1505 {
		t.Fatalf("last turn's context %d, want 1505", res.ContextTokens)
	}
}
