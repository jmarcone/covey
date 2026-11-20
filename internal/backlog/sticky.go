package backlog

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A task can name the topic it belongs to (#130): the pull request or issue a
// developer's fix round is about. Every fix round used to start a cold runtime
// session, even the second round on the same pull request by the same
// developer, which then read the code and the review again and paid the cache
// write again. With a topic the orchestrator can resume the session of the
// agent's last finished task on it instead (orchestrator/sticky.go).

// TopicFormat is the form a topic takes, in the words an error names it.
const TopicFormat = `"pr:<n>" or "issue:<n>" (n a positive whole number, at most 64 characters)`

// ErrInvalidTopic refuses a topic of any other form. The topic becomes part of a
// directory name in the sandbox, so the form is closed, not cleaned up.
var ErrInvalidTopic = errors.New("topic must be " + TopicFormat)

const maxTopicLen = 64

var topicPattern = regexp.MustCompile(`^(pr|issue):[1-9][0-9]*$`)

// NormalizeTopic checks an explicit topic. Surrounding blanks are dropped; an
// empty topic is no topic ("", nil); anything else must be TopicFormat exactly.
func NormalizeTopic(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !IsStickyTopic(s) {
		return "", ErrInvalidTopic
	}
	return s, nil
}

// IsStickyTopic says whether a stored topic_key is one a session may follow.
// The column is shared with topic routing (#45), whose keys are derived from
// titles ("#45", "cost page headline"); those never make a task sticky.
func IsStickyTopic(key string) bool {
	return len(key) <= maxTopicLen && topicPattern.MatchString(key)
}

// TopicParts splits a sticky topic into its kind ("pr", "issue") and number.
func TopicParts(key string) (kind, number string) {
	kind, number, _ = strings.Cut(key, ":")
	return kind, number
}

// Predecessor is an agent's last finished task on a topic: what its run left
// for the next one.
type Predecessor struct {
	TaskID     uuid.UUID
	SessionID  string
	FinishedAt time.Time
	// ContextTokens is the size of the context in the run's last turn (input +
	// cache read + cache write); 0 when it is not known.
	ContextTokens int64
	// Credential names the pool value the run was billed to (orchestrator's
	// credentialKey); "" when not known.
	Credential string
	// Prompt and Memory are the system prompt and memory context the session
	// was started with. Prompt is nil when the task never recorded them (it
	// ran before its agent kept sessions).
	Prompt *string
	Memory string
	Result string
	// Successor is the task that already continues this session, if any.
	Successor *uuid.UUID
}

// LastOnTopic returns the agent's most recently finished (done) task on the
// topic, other than exclude. The organisation is part of the condition so that
// a lookup can never cross into another tenant, whatever the caller passes.
func (s *Store) LastOnTopic(ctx context.Context, orgID, agentID uuid.UUID, topic string, exclude uuid.UUID) (Predecessor, bool, error) {
	var p Predecessor
	err := s.pool.QueryRow(ctx, `SELECT id, COALESCE(runtime_session_id,''), session_finished_at,
			COALESCE(session_context_tokens,0), COALESCE(session_credential,''), session_prompt,
			COALESCE(session_memory,''), COALESCE(result,''), session_successor
		FROM backlog_tasks
		WHERE org_id=$1 AND agent_id=$2 AND topic_key=$3 AND state='done'
		  AND session_finished_at IS NOT NULL AND id<>$4
		ORDER BY session_finished_at DESC, id DESC LIMIT 1`, orgID, agentID, topic, exclude).Scan(
		&p.TaskID, &p.SessionID, &p.FinishedAt, &p.ContextTokens, &p.Credential, &p.Prompt,
		&p.Memory, &p.Result, &p.Successor)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// ClaimSession makes successor the one task that continues predecessor's
// session. It fails (false) when another task holds the claim: two rounds on
// one topic must never resume the same session at once, and the second would
// branch the conversation the first is still writing. A task that claims again
// (a requeue after a lost sandbox) keeps its claim.
func (s *Store) ClaimSession(ctx context.Context, predecessor, successor uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET session_successor=$2
		WHERE id=$1 AND (session_successor IS NULL OR session_successor=$2)`, predecessor, successor)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SessionPrompt is the system prompt and memory context a task's session was
// started with, recorded so that every resume of the session sends the same
// bytes. ok is false when the task recorded none.
func (s *Store) SessionPrompt(ctx context.Context, id uuid.UUID) (prompt, memory string, ok bool, err error) {
	var p, m *string
	err = s.pool.QueryRow(ctx, "SELECT session_prompt, session_memory FROM backlog_tasks WHERE id=$1", id).Scan(&p, &m)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, ErrNotFound
	}
	if err != nil || p == nil {
		return "", "", false, err
	}
	if m != nil {
		memory = *m
	}
	return *p, memory, true, nil
}

// SetSessionPrompt records the prompt the task's session runs under.
func (s *Store) SetSessionPrompt(ctx context.Context, id uuid.UUID, prompt, memory string) error {
	_, err := s.pool.Exec(ctx, "UPDATE backlog_tasks SET session_prompt=$2, session_memory=$3 WHERE id=$1",
		id, prompt, memory)
	return err
}

// StickyDecision is how a task's run was started, recorded on the task.
type StickyDecision struct {
	Decision string // DecisionResumed | DecisionFresh | DecisionPending
	Reason   string // for fresh: the Reason* below
	Policy   string // the policy that applied (fresh, conditional, always)
	From     *uuid.UUID
	GapS     *int64
	Context  *int64
}

// The decisions and the reasons for a fresh start.
const (
	DecisionResumed = "sticky_resumed"
	DecisionFresh   = "fresh"
	// DecisionPending: the daemon was asked to resume and has not said
	// whether it could (an older daemon never says).
	DecisionPending = "sticky_pending"

	ReasonWindow     = "window"
	ReasonCap        = "cap"
	ReasonOff        = "off"
	ReasonNoSession  = "no_session"
	ReasonNoPrevious = "no_previous"
	ReasonCredential = "credential"
	ReasonBusy       = "busy"
)

// RecordSticky writes the dispatch decision onto the task.
func (s *Store) RecordSticky(ctx context.Context, id uuid.UUID, d StickyDecision) error {
	_, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET sticky_decision=$2, sticky_reason=NULLIF($3,''),
		sticky_policy=NULLIF($4,''), sticky_from=$5, sticky_gap_s=$6, sticky_context=$7 WHERE id=$1`,
		id, d.Decision, d.Reason, d.Policy, d.From, d.GapS, d.Context)
	return err
}

// SettleSticky records the daemon's answer to a resume it was asked for: only
// a pending decision changes, so a late or repeated answer cannot rewrite one
// that is settled.
func (s *Store) SettleSticky(ctx context.Context, id uuid.UUID, decision, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET sticky_decision=$2, sticky_reason=NULLIF($3,'')
		WHERE id=$1 AND sticky_decision=$4`, id, decision, reason, DecisionPending)
	return err
}

// RecordSessionEnd stores what a finished run leaves for the next one on its
// topic: the session, the time, the context size of its last turn (0 = not
// known) and the credential it ran on.
func (s *Store) RecordSessionEnd(ctx context.Context, id uuid.UUID, sessionID string, contextTokens int64, credential string) error {
	_, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET
		runtime_session_id=COALESCE(NULLIF($2,''), runtime_session_id), session_finished_at=now(),
		session_context_tokens=NULLIF($3::bigint,0), session_credential=NULLIF($4,'') WHERE id=$1`,
		id, sessionID, contextTokens, credential)
	return err
}

// FirstTurn is the usage of a run's first assistant turn.
type FirstTurn struct {
	Input, CacheRead, CacheWrite5m, CacheWrite1h int64
}

// RecordFirstTurn stores the first turn of the task's FIRST run; later runs
// (a resume after a block, say) leave it alone.
func (s *Store) RecordFirstTurn(ctx context.Context, id uuid.UUID, f FirstTurn) error {
	_, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET first_turn_input=$2, first_turn_cache_read=$3,
		first_turn_cache_write_5m=$4, first_turn_cache_write_1h=$5 WHERE id=$1 AND first_turn_input IS NULL`,
		id, f.Input, f.CacheRead, f.CacheWrite5m, f.CacheWrite1h)
	return err
}

// reviewHeading marks the review round in a reviewer's report.
const reviewHeading = "## Review round"

// LatestReview returns the latest review round on the topic: the most recent
// finished report of another agent of the organisation that carries a
// "## Review round" heading, from that heading on. "" when there is none.
//
// The source is covey's own record of the review, not the pull request: the
// dispatch has no read path into the code host, and the reviewer's report is
// what its review comment was written from.
func (s *Store) LatestReview(ctx context.Context, orgID, agentID uuid.UUID, topic string) (string, error) {
	var result string
	err := s.pool.QueryRow(ctx, `SELECT result FROM backlog_tasks
		WHERE org_id=$1 AND agent_id<>$2 AND topic_key=$3 AND state='done' AND strpos(result, $4) > 0
		ORDER BY COALESCE(session_finished_at, updated_at) DESC LIMIT 1`, orgID, agentID, topic, reviewHeading).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if i := strings.LastIndex(result, reviewHeading); i >= 0 {
		result = result[i:]
	}
	return strings.TrimSpace(result), nil
}

// StickyState is what a task recorded about its session so far: the policy
// it was dispatched under ("" = never) and the prompt its session runs with.
type StickyState struct {
	Policy string
	Prompt *string
	Memory string
}

// StickyState reads it.
func (s *Store) StickyState(ctx context.Context, id uuid.UUID) (StickyState, error) {
	var st StickyState
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sticky_policy,''), session_prompt, COALESCE(session_memory,'')
		FROM backlog_tasks WHERE id=$1`, id).Scan(&st.Policy, &st.Prompt, &st.Memory)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}
