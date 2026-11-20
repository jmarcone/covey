package orchestrator

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
)

func TestStickyVerdict(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	prompt := "the session's prompt"
	pred := func(ago time.Duration, ctxTokens int64) backlog.Predecessor {
		return backlog.Predecessor{TaskID: uuid.New(), SessionID: "s-1", FinishedAt: now.Add(-ago),
			ContextTokens: ctxTokens, Prompt: &prompt, Credential: "secret:a:0"}
	}
	cases := []struct {
		name   string
		policy string
		found  bool
		p      backlog.Predecessor
		cred   string
		want   string
	}{
		{"10 min, small: resumed", agents.StickyConditional, true, pred(10*time.Minute, 50000), "secret:a:0", ""},
		{"54 min: resumed", agents.StickyConditional, true, pred(54*time.Minute, 1), "", ""},
		{"55 min: window", agents.StickyConditional, true, pred(55*time.Minute, 1), "", backlog.ReasonWindow},
		{"56 min: window", agents.StickyConditional, true, pred(56*time.Minute, 1), "", backlog.ReasonWindow},
		{"199,999: resumed", agents.StickyConditional, true, pred(time.Minute, 199999), "", ""},
		{"200,000: cap", agents.StickyConditional, true, pred(time.Minute, 200000), "", backlog.ReasonCap},
		{"200,001: cap", agents.StickyConditional, true, pred(time.Minute, 200001), "", backlog.ReasonCap},
		{"unknown size: resumed", agents.StickyConditional, true, pred(time.Minute, 0), "", ""},
		{"off", agents.StickyFresh, true, pred(time.Minute, 1), "", backlog.ReasonOff},
		{"no policy = off", "", true, pred(time.Minute, 1), "", backlog.ReasonOff},
		{"nothing before", agents.StickyConditional, false, backlog.Predecessor{}, "", backlog.ReasonNoPrevious},
		{"other credential", agents.StickyConditional, true, pred(time.Minute, 1), "secret:b:0", backlog.ReasonCredential},
		{"always: three hours, 500k", agents.StickyAlways, true, pred(3*time.Hour, 500000), "secret:a:0", ""},
		{"always: still not across credentials", agents.StickyAlways, true, pred(time.Minute, 1), "runtime:x:1", backlog.ReasonCredential},
		{"no session id", agents.StickyConditional, true, backlog.Predecessor{FinishedAt: now, Prompt: &prompt}, "", backlog.ReasonNoSession},
		{"no recorded prompt", agents.StickyConditional, true, backlog.Predecessor{FinishedAt: now, SessionID: "s"}, "", backlog.ReasonNoSession},
	}
	pending := pred(time.Minute, 1)
	pending.Decision = backlog.DecisionPending
	cases = append(cases, struct {
		name   string
		policy string
		found  bool
		p      backlog.Predecessor
		cred   string
		want   string
	}{"predecessor's resume never answered (old daemon)", agents.StickyConditional, true, pending, "", backlog.ReasonNoSession})
	for _, c := range cases {
		if got := stickyVerdict(c.policy, c.found, c.p, now, 55, 200000, c.cred, ""); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStickyVerdictPromptChanged(t *testing.T) {
	now := time.Now()
	prompt := "p"
	p := backlog.Predecessor{SessionID: "s", FinishedAt: now, Prompt: &prompt, Basis: "aaa"}
	if got := stickyVerdict(agents.StickyAlways, true, p, now, 55, 200000, "", "bbb"); got != backlog.ReasonPromptChanged {
		t.Fatalf("a changed basis must start fresh, got %q", got)
	}
	if got := stickyVerdict(agents.StickyAlways, true, p, now, 55, 200000, "", "aaa"); got != "" {
		t.Fatalf("the same basis resumes, got %q", got)
	}
	p.Basis = ""
	if got := stickyVerdict(agents.StickyAlways, true, p, now, 55, 200000, "", "bbb"); got != "" {
		t.Fatalf("an unrecorded basis is not held against the session, got %q", got)
	}
}

func TestEffectiveStickyPolicy(t *testing.T) {
	cases := map[[2]string]string{
		{agents.StickyAB, "pr:41"}:          agents.StickyAlways,
		{agents.StickyAB, "pr:42"}:          agents.StickyConditional,
		{agents.StickyAB, "pr:1009"}:        agents.StickyAlways,
		{agents.StickyAB, "issue:41"}:       agents.StickyConditional,
		{agents.StickyConditional, "pr:41"}: agents.StickyConditional,
		{agents.StickyFresh, "pr:41"}:       agents.StickyFresh,
		{"", "pr:41"}:                       agents.StickyFresh,
		{agents.StickyAlways, "issue:2"}:    agents.StickyAlways,
	}
	for in, want := range cases {
		if got := effectiveStickyPolicy(in[0], in[1]); got != want {
			t.Errorf("effectiveStickyPolicy(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// The daemon accepts only this form of directory name (daemon/sticky.go).
var daemonTopicDir = regexp.MustCompile(`^topic-[a-z0-9][a-z0-9-]{0,150}$`)

func TestTopicWorkDir(t *testing.T) {
	cases := map[[2]string]string{
		{"covey-dev", "pr:42"}:         "topic-covey-dev-pr-42",
		{"Covey_Front.end", "issue:7"}: "topic-covey-front-end-issue-7",
		{"../../etc", "pr:1"}:          "topic-etc-pr-1",
		{"---", "pr:1"}:                "topic-agent-pr-1",
	}
	for in, want := range cases {
		got := topicWorkDir(in[0], in[1])
		if got != want {
			t.Errorf("topicWorkDir(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
		if !daemonTopicDir.MatchString(got) {
			t.Errorf("%q is not a name the daemon accepts", got)
		}
	}
	long := topicWorkDir(strings.Repeat("a", 300), "pr:"+strings.Repeat("9", 60))
	if !daemonTopicDir.MatchString(long) {
		t.Errorf("a long slug and topic still make a name the daemon accepts: %q (%d)", long, len(long))
	}
	// Two agents never share a directory.
	if topicWorkDir("covey-dev", "pr:42") == topicWorkDir("covey-frontend", "pr:42") {
		t.Error("two agents share a topic directory")
	}
}

func TestAssistantUsage(t *testing.T) {
	main := `{"type":"assistant","parent_tool_use_id":null,"message":{"usage":{"input_tokens":3,` +
		`"cache_read_input_tokens":150000,"cache_creation_input_tokens":2000,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":500,"ephemeral_1h_input_tokens":1500}}}}`
	u, ok := assistantUsage([]byte(main))
	if !ok || u.Input != 3 || u.CacheRead != 150000 || u.CacheWrite5m != 500 || u.CacheWrite1h != 1500 {
		t.Fatalf("main-thread turn: %+v ok=%v", u, ok)
	}
	// Without the split, the whole write counts as 5-minute (the API default).
	noSplit := `{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":7}}}`
	if u, ok := assistantUsage([]byte(noSplit)); !ok || u.CacheWrite5m != 7 || u.CacheWrite1h != 0 {
		t.Fatalf("turn without split: %+v ok=%v", u, ok)
	}
	// A sub-agent's turn is the sub-agent's context, not the session's.
	sub := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"usage":{"input_tokens":1,"cache_read_input_tokens":900000}}}`
	if _, ok := assistantUsage([]byte(sub)); ok {
		t.Fatal("a sub-agent's turn must not count")
	}
	// Nor is the line of a sub-run the daemon started itself.
	covey := `{"type":"assistant","parent_tool_use_id":null,"covey_sub_agent":{"name":"x"},"message":{"usage":{"input_tokens":1,"cache_read_input_tokens":900000}}}`
	if _, ok := assistantUsage([]byte(covey)); ok {
		t.Fatal("a covey sub-run's turn must not count")
	}
	for _, other := range []string{`{"type":"result","usage":{"input_tokens":1}}`, `{"type":"assistant","message":{}}`, `not json`} {
		if _, ok := assistantUsage([]byte(other)); ok {
			t.Errorf("not a turn: %s", other)
		}
	}
}

func TestStickyHandoverCapped(t *testing.T) {
	o := &Orchestrator{}
	long := strings.Repeat("ö", 10000)
	h := o.stickyHandover(context.Background(), agents.Agent{}, "issue:7", backlog.Predecessor{Result: long})
	if n := utf8.RuneCountInString(h); n > stickyHandoverCap {
		t.Fatalf("handover has %d characters, cap is %d", n, stickyHandoverCap)
	}
	if !strings.HasPrefix(h, "## Handover (issue:7)") || !strings.Contains(h, "not your own memory") ||
		!strings.HasSuffix(h, "## The new task\n\n") {
		t.Fatalf("handover is not marked as one: %q", h[:200])
	}
	if got := o.stickyHandover(context.Background(), agents.Agent{}, "issue:7", backlog.Predecessor{}); got != "" {
		t.Fatalf("nothing to hand over makes no handover: %q", got)
	}
}
