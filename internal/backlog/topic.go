package backlog

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The values of Task.SessionDecision (#130).
const (
	SessionStickyResumed = "sticky_resumed"
	SessionFresh         = "fresh"
)

// The values of Task.SessionReason beside SessionFresh: why the previous
// task's session was not resumed.
const (
	SessionReasonWindow     = "window"      // it ended longer ago than the agent's window
	SessionReasonCap        = "cap"         // its last turn's context reached the agent's cap
	SessionReasonOff        = "off"         // the agent's stickiness is off
	SessionReasonNoSession  = "no_session"  // no session to resume: none stored, or not on this daemon
	SessionReasonNoPrevious = "no_previous" // no earlier done task of this agent on this topic
)

// TopicRun is the previous task of an agent on a topic, as the next one
// decides on it.
type TopicRun struct {
	TaskID        uuid.UUID
	SessionID     string
	Result        string
	FinishedAt    time.Time
	ContextTokens int64
	// Now is the database's clock at the lookup, so that the age of
	// FinishedAt is measured on the clock that wrote it.
	Now time.Time
}

// LastOnTopic returns the agent's last done task on topic other than
// exclude, ok=false when there is none. Only a done task counts: a failed
// one's session ended on an error, and a cancelled one never finished.
func (s *Store) LastOnTopic(ctx context.Context, agentID uuid.UUID, topic string, exclude uuid.UUID) (TopicRun, bool, error) {
	var r TopicRun
	var session, result *string
	var tokens *int64
	err := s.pool.QueryRow(ctx, `
		SELECT id, runtime_session_id, result, finished_at, last_turn_context_tokens, now()
		  FROM backlog_tasks
		 WHERE agent_id=$1 AND topic_key=$2 AND id<>$3
		   AND state='done' AND finished_at IS NOT NULL
		 ORDER BY finished_at DESC
		 LIMIT 1`, agentID, topic, exclude).Scan(&r.TaskID, &session, &result, &r.FinishedAt, &tokens, &r.Now)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicRun{}, false, nil
	}
	if err != nil {
		return TopicRun{}, false, err
	}
	if session != nil {
		r.SessionID = *session
	}
	if result != nil {
		r.Result = *result
	}
	if tokens != nil {
		r.ContextTokens = *tokens
	}
	return r, true, nil
}

// SetSessionDecision records how the task's run started (#130). Written at
// dispatch, and again when the daemon could not resume the session it was
// told to.
func (s *Store) SetSessionDecision(ctx context.Context, id uuid.UUID, decision, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET session_decision=$2, session_reason=NULLIF($3,'')
		WHERE id=$1`, id, decision, reason)
	return err
}
