package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// A task on a topic resumes the session the agent's previous task on it left,
// when the control plane says so (AssignTask.StickySessionID, #130). The
// control plane decides on what it knows: the agent's setting, how long ago
// the previous task ended, how large its context was. Whether the session is
// actually here only the daemon knows, and so it has the last word.
//
// A resumed session reads its whole earlier context from the prompt cache only
// if the request begins with the same bytes as before, and the system prompt
// comes first. It is not the same from task to task: the wiki's hits are the
// task's, and parts of the compiled prompt follow the task's text. So the run
// that starts a session on a topic leaves its system prompt on record beside
// the transcript, and a sticky resume sends that one, not its own. The new
// task's wiki hits go into the resume message instead.

// sessionRecordName is the record in the topic's directory. Hidden, and it
// goes with the directory when the sweep removes that.
const sessionRecordName = ".covey-session.json"

// sessionRecord is what a sticky resume needs to begin with the same bytes as
// the session's first run.
type sessionRecord struct {
	// SessionID is the session the run that wrote the record ended in. Empty
	// until that run has ended: a resume needs the session the control plane
	// names to be this one.
	SessionID     string `json:"session_id"`
	SystemPrompt  string `json:"system_prompt"`
	MemoryContext string `json:"memory_context"`
}

func loadSessionRecord(dir string) (sessionRecord, bool) {
	var rec sessionRecord
	if dir == "" {
		return rec, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, sessionRecordName))
	if err != nil || json.Unmarshal(raw, &rec) != nil {
		return sessionRecord{}, false
	}
	return rec, true
}

func saveSessionRecord(dir string, rec sessionRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sessionRecordName), raw, 0o600)
}

// startOnTopic decides how a run on a topic starts and prepares spec for it.
// It returns the decision to report (zero for a run that is not one to
// decide: no topic, or a resume of the task's own session, which goes on as
// before) and whether the run is one whose session the record follows.
//
// Resumed: spec carries the recorded system prompt and wiki context, the
// session and the task as the resume message. Fresh: spec carries the
// handover, and the run's prompt goes on record for the next task.
func startOnTopic(spec *RunSpec, rt Runtime, task AssignTask) (SessionStart, bool) {
	if spec.Topic == "" || spec.WorkDir == "" || task.ResumeSessionID != "" {
		return SessionStart{}, false
	}
	var decision SessionStart
	if id := task.StickySessionID; id != "" {
		keeper, ok := rt.(SessionKeeper)
		rec, recorded := loadSessionRecord(spec.WorkDir)
		if ok && recorded && rec.SessionID == id && keeper.HasSession(spec.HomeDir, spec.WorkDir, id) {
			spec.SystemPrompt = rec.SystemPrompt
			spec.MemoryContext = rec.MemoryContext
			spec.ResumeSessionID = id
			spec.ResumeInput = stickyInput(task)
			return SessionStart{Decision: "sticky_resumed"}, true
		}
		decision = SessionStart{Decision: "fresh", Reason: "no_session"}
	}
	spec.Handover = task.Handover
	_ = saveSessionRecord(spec.WorkDir, sessionRecord{
		SystemPrompt: spec.SystemPrompt, MemoryContext: spec.MemoryContext})
	return decision, true
}

// endOnTopic writes the session the run ended in into the record, so that the
// next task on the topic can resume it.
func endOnTopic(dir, sessionID string) {
	if dir == "" || sessionID == "" {
		return
	}
	rec, ok := loadSessionRecord(dir)
	if !ok {
		return
	}
	rec.SessionID = sessionID
	_ = saveSessionRecord(dir, rec)
}

// stickyInput is the resume message of a sticky resume: the new task, and the
// wiki's hits for it ahead of it, as a fresh run would have them in its system
// prompt. Not "the event you were waiting for has occurred": the session ended
// with a task done, and this is the next one.
func stickyInput(task AssignTask) string {
	text := task.Title + "\n\n" + task.Body
	if mem := strings.TrimSpace(task.MemoryContext); mem != "" {
		return mem + "\n\n---\n\n" + text
	}
	return text
}
