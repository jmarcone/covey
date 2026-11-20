package orchestrator

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"covey/internal/agents"
	"covey/internal/backlog"
)

// The four conditions, at their edges, on a fixed clock.
func TestDecideSession(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	on := agents.Agent{StickySessions: true, StickyWindowMinutes: 55, StickyContextCap: 200_000}
	off := on
	off.StickySessions = false
	prev := func(agoMin int, tokens int64, session string) backlog.TopicRun {
		return backlog.TopicRun{SessionID: session, FinishedAt: now.Add(-time.Duration(agoMin) * time.Minute),
			ContextTokens: tokens, Now: now}
	}
	cases := []struct {
		name   string
		agent  agents.Agent
		prev   backlog.TopicRun
		found  bool
		reason string // "" = resumed
	}{
		{"10 minutes later", on, prev(10, 44_000, "s"), true, ""},
		{"54 minutes later", on, prev(54, 44_000, "s"), true, ""},
		{"55 minutes later", on, prev(55, 44_000, "s"), true, backlog.SessionReasonWindow},
		{"56 minutes later", on, prev(56, 44_000, "s"), true, backlog.SessionReasonWindow},
		{"199,999 tokens", on, prev(10, 199_999, "s"), true, ""},
		{"200,000 tokens", on, prev(10, 200_000, "s"), true, backlog.SessionReasonCap},
		{"200,001 tokens", on, prev(10, 200_001, "s"), true, backlog.SessionReasonCap},
		{"context not reported", on, prev(10, 0, "s"), true, ""},
		{"stickiness off", off, prev(10, 44_000, "s"), true, backlog.SessionReasonOff},
		{"no session stored", on, prev(10, 44_000, ""), true, backlog.SessionReasonNoSession},
		{"no previous task", on, backlog.TopicRun{}, false, backlog.SessionReasonNoPrevious},
	}
	for _, c := range cases {
		decision, reason := decideSession(c.agent, c.prev, c.found)
		wantDecision := backlog.SessionFresh
		if c.reason == "" {
			wantDecision = backlog.SessionStickyResumed
		}
		if decision != wantDecision || reason != c.reason {
			t.Errorf("%s: %s/%s, want %s/%s", c.name, decision, reason, wantDecision, c.reason)
		}
	}
}

// The handover is marked as one, carries the result and the review comment,
// and stays within its 4,000 characters however long both are.
func TestHandover(t *testing.T) {
	h := handover("pr:42", backlog.SessionReasonWindow, "Parser done.", "## Review round 1\n\n1. major")
	if !strings.HasPrefix(h, "## Handover from your previous task on pr:42") {
		t.Fatalf("not marked: %q", h)
	}
	for _, want := range []string{"Parser done.", "1. major", "not its instructions"} {
		if !strings.Contains(h, want) {
			t.Fatalf("missing %q: %q", want, h)
		}
	}
	long := handover("pr:42", backlog.SessionReasonCap, strings.Repeat("ä", 9000), strings.Repeat("x", 9000))
	if n := utf8.RuneCountInString(long); n > handoverLimit {
		t.Fatalf("handover of %d characters", n)
	}
	if !strings.Contains(long, "The latest review comment") {
		t.Fatal("a long result must not crowd the review comment out")
	}
	if h := handover("issue:7", backlog.SessionReasonOff, "", ""); strings.Contains(h, "###") {
		t.Fatalf("no sections without content: %q", h)
	}
}
