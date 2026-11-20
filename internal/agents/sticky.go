package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Sticky sessions (#130): whether an agent's task on a topic (a pull request,
// an issue) resumes the runtime session of its last finished task on that
// topic, instead of starting cold and reading everything again.
//
// The policies:
//   - fresh: never. The reviewer above all: a review round is a fresh
//     adversarial review, and a resumed reviewer is anchored on its own
//     earlier reasoning by construction.
//   - conditional: inside the window (the prompt cache's lifetime) and under
//     the context cap; otherwise fresh with a handover.
//   - always: whatever the gap and the size, as a long interactive session
//     runs. Still not across credentials, and not without the transcript.
//   - ab: an A/B test on one developer's own fix rounds — odd pull requests
//     always, even ones (and issues) conditional. The arm follows from the
//     number, so a pull request keeps its arm for its whole life.
const (
	StickyFresh       = "fresh"
	StickyConditional = "conditional"
	StickyAlways      = "always"
	StickyAB          = "ab"
)

// The defaults of the window and the cap. 55 minutes stays inside the one-hour
// prompt cache; 200,000 tokens is where a resumed context costs more per turn
// than a fresh start with a handover.
const (
	DefaultStickyWindowMinutes = 55
	DefaultStickyContextCap    = 200000
	maxStickyWindowMinutes     = 7 * 24 * 60
)

// ErrInvalidSticky refuses a sticky setting outside its range.
var ErrInvalidSticky = errors.New("invalid sticky session setting")

// ValidStickyPolicy says whether p is one of the four policies.
func ValidStickyPolicy(p string) bool {
	switch p {
	case StickyFresh, StickyConditional, StickyAlways, StickyAB:
		return true
	}
	return false
}

// DefaultStickyPolicy is the policy a new agent starts with: the developers
// keep their session, every other agent starts fresh.
func DefaultStickyPolicy(slug string) string {
	switch slug {
	case "covey-dev", "covey-frontend":
		return StickyConditional
	}
	return StickyFresh
}

// SetSticky sets the agent's sticky policy, window (minutes) and context cap
// (tokens). Takes effect at the next task dispatch.
func (r *Registry) SetSticky(ctx context.Context, id uuid.UUID, policy string, windowMinutes int, contextCap int64) error {
	if !ValidStickyPolicy(policy) {
		return fmt.Errorf("%w: policy must be fresh, conditional, always or ab", ErrInvalidSticky)
	}
	if windowMinutes < 1 || windowMinutes > maxStickyWindowMinutes {
		return fmt.Errorf("%w: window_minutes must be between 1 and %d", ErrInvalidSticky, maxStickyWindowMinutes)
	}
	if contextCap < 1 {
		return fmt.Errorf("%w: context_cap must be positive", ErrInvalidSticky)
	}
	tag, err := r.pool.Exec(ctx, `UPDATE agents SET sticky_policy=$2, sticky_window_minutes=$3,
		sticky_context_cap=$4, updated_at=now() WHERE id=$1`, id, policy, windowMinutes, contextCap)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
