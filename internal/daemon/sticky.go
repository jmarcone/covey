package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// topicDirPattern is the only form a topic directory name takes,
// topic-<agent id>-<pr|issue>-<n>: no separator, no dot, nothing a path could
// be built from but one plain name — and nothing an agent would name a
// directory of its own, which the sweep would otherwise take for one. The control
// plane composes it (orchestrator.topicWorkDir); the daemon accepts nothing else.
var topicDirPattern = regexp.MustCompile(`^topic-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-(pr|issue)-[1-9][0-9]{0,62}$`)

func isTopicDir(name string) bool { return topicDirPattern.MatchString(name) }

// topicScratchDir creates ~/scratch/<key> for a topic and stamps it as entered,
// like scratchDir does for a task. "" for a key of any other form or when the
// directory cannot be made — the run then uses its task's own directory.
// existed says whether the directory was there already: one that had to be
// made again was swept, and a session that worked in it would come back to
// none of its files.
func topicScratchDir(home, key string) (dir string, existed bool) {
	if home == "" || !isTopicDir(key) {
		return "", false
	}
	dir = filepath.Join(home, scratchRoot, key)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		existed = true
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	return dir, existed
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
func applySticky(rt Runtime, spec *RunSpec, task AssignTask, scratch string, dirExisted bool) StickyOutcome {
	if task.StickySessionID == "" {
		return StickyOutcome{}
	}
	// Never into a directory that was swept and made again: the transcript
	// outlives it under ~/.claude, the files the session worked on do not.
	if holder, ok := rt.(SessionHolder); ok && dirExisted && holder.HasSession(*spec, task.StickySessionID) {
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

// runDirFor picks a run's working directory among the candidates, in order:
// the topic's directory, the continuation chain's first task's, the task's
// own ("" = not a candidate). A run that resumes a session goes to the first
// candidate that holds it — --resume finds a transcript only under the
// directory it was written in, and a resume that finds nothing fails the
// task. Otherwise, and when none holds it, the first candidate there is.
func runDirFor(rt Runtime, home, resumeID string, dirs ...string) string {
	if holder, ok := rt.(SessionHolder); ok && resumeID != "" {
		for _, d := range dirs {
			if d != "" && holder.HasSession(RunSpec{HomeDir: home, WorkDir: d}, resumeID) {
				return d
			}
		}
	}
	for _, d := range dirs {
		if d != "" {
			return d
		}
	}
	return ""
}

// taskScratchPath is where scratchDir would put the task's own directory,
// without making it; "" for an id scratchDir refuses.
func taskScratchPath(home, taskID string) string {
	if home == "" || !isTaskID(taskID) {
		return ""
	}
	return filepath.Join(home, scratchRoot, strings.ToLower(taskID))
}
