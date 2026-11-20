package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Sticky sessions (#130). The control plane decides whether a task on a topic
// may resume the session of its agent's previous task on that topic — window,
// context cap, policy, credential. One condition only the daemon can check:
// whether the runtime holds that session's transcript here, in the topic's
// directory. If it does not (another runner, a home restored without it, an
// engine that keeps no sessions), the run starts fresh with the handover the
// control plane put into the task, and the daemon says so.

// TopicFormat is the form of a task topic, as an error names it. The control
// plane's backlog.TopicFormat says the same; the daemon does not import the
// backlog (it would drag the database driver into the sandbox).
const TopicFormat = `"pr:<n>" or "issue:<n>" (n a positive whole number, at most 64 characters)`

var topicPattern = regexp.MustCompile(`^(pr|issue):[1-9][0-9]*$`)

// ValidTopic says whether s is a topic in TopicFormat.
func ValidTopic(s string) bool { return len(s) <= 64 && topicPattern.MatchString(s) }

// topicDirPattern is the only form a topic directory name takes: no separator,
// no dot, nothing a path could be built from but one plain name. The control
// plane composes it (orchestrator.topicWorkDir); the daemon accepts nothing else.
var topicDirPattern = regexp.MustCompile(`^topic-[a-z0-9][a-z0-9-]{0,150}$`)

func isTopicDir(name string) bool { return topicDirPattern.MatchString(name) }

// topicScratchDir creates ~/scratch/<key> for a topic and stamps it as entered,
// like scratchDir does for a task. "" for a key of any other form or when the
// directory cannot be made — the run then uses its task's own directory.
func topicScratchDir(home, key string) string {
	if home == "" || !isTopicDir(key) {
		return ""
	}
	dir := filepath.Join(home, scratchRoot, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	return dir
}

// SessionHolder is a runtime that can say whether it holds a session's
// transcript for a run started with spec (its home and working directory).
// A runtime without it holds none as far as a sticky resume is concerned.
type SessionHolder interface {
	HasSession(spec RunSpec, id string) bool
}

// HasSession: Claude Code keeps a transcript per working directory, and
// `--resume` finds it only under the directory the run starts in.
func (c *ClaudeCode) HasSession(spec RunSpec, id string) bool {
	return hasSession(spec.HomeDir, spec.WorkDir, id)
}

// applySticky turns a sticky assignment into the run's spec: the resume when
// the runtime holds the session, otherwise the fallback (fresh, with the
// handover in Title/Body, under the task's own prompt). It reports which, for
// the "sticky" event.
func applySticky(rt Runtime, spec *RunSpec, task AssignTask, scratch string) StickyOutcome {
	if task.StickySessionID == "" {
		return StickyOutcome{}
	}
	if holder, ok := rt.(SessionHolder); ok && holder.HasSession(*spec, task.StickySessionID) {
		spec.ResumeSessionID = task.StickySessionID
		spec.ResumeInput = task.StickyInput
		return StickyOutcome{Decision: "sticky_resumed"}
	}
	if task.FallbackSystemPrompt != "" {
		spec.SystemPrompt = withScratch(withWorkplace(task.FallbackSystemPrompt), scratch)
		spec.MemoryContext = task.FallbackMemoryContext
	}
	return StickyOutcome{Decision: "fresh", Reason: "no_session"}
}

func stickyEvent(o StickyOutcome) json.RawMessage {
	b, _ := json.Marshal(o)
	return b
}

// appendedSystemPrompt is the system prompt as a run hands it to the engine:
// the compiled prompt, then the memory context. A resumed session is read from
// the prompt cache only when this is byte-identical to the run that started it.
func appendedSystemPrompt(spec RunSpec) string {
	p := spec.SystemPrompt
	if spec.MemoryContext != "" {
		p += "\n\n" + spec.MemoryContext
	}
	return p
}

// HasSession: seven-code keeps its sessions under the home, whatever the cwd.
func (s *SevenCode) HasSession(spec RunSpec, id string) bool { return sevencodeHasSession(spec, id) }

// resumeDir picks the working directory of a topic task: the topic's, unless
// the task resumes its own session and that session lies in the task's own
// directory instead — it was started there by a daemon that predates topic
// directories, or when the topic's could not be made. --resume finds a
// transcript only under the directory it was written in, and a resume that
// finds nothing fails the task.
func resumeDir(rt Runtime, home, topicDir, taskDir, resumeID string) string {
	if resumeID == "" || taskDir == "" {
		return topicDir
	}
	holder, ok := rt.(SessionHolder)
	if !ok {
		return topicDir
	}
	if !holder.HasSession(RunSpec{HomeDir: home, WorkDir: topicDir}, resumeID) &&
		holder.HasSession(RunSpec{HomeDir: home, WorkDir: taskDir}, resumeID) {
		return taskDir
	}
	return topicDir
}
