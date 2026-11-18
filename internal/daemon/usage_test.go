package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The real answer of `claude -p "/usage"`, captured from the binary. It is
// pinned here verbatim because the parser's whole risk is that this text
// changes — and then it should fail HERE and not in a fleet that quietly stops
// measuring.
const realUsageOutput = `You are currently using your subscription to power your Claude Code usage

Current session: 6% used · resets Aug 7 at 1:20pm (Europe/Berlin)
Current week (all models): 87% used · resets Aug 9 at 6:59am (Europe/Berlin)
Current week (Fable): 0% used

What's contributing to your limits usage?
Approximate, based on local sessions on this machine — does not include other devices or claude.ai.

Last 24h · 965 requests · 9 sessions
  73% of your usage was at >150k context
`

func TestParseUsageReadsTheRealAnswer(t *testing.T) {
	u := ParseUsage(realUsageOutput)
	if u.WindowPercent != 6 {
		t.Fatalf("session share = %v, expected 6", u.WindowPercent)
	}
	if u.WeekPercent != 87 {
		t.Fatalf("week share = %v, expected 87", u.WeekPercent)
	}
	if u.WindowResets == "" || u.WeekResets == "" {
		t.Fatalf("the reset times belong in it: %+v", u)
	}
	if u.Stale {
		t.Fatal("a fresh answer must not be marked stale")
	}
	if !u.Reported() {
		t.Fatal("a parsed answer counts as reported")
	}
}

// TestParseUsageMarksStale: when the provider's endpoint is rate limited the
// engine serves bars up to an hour old. Reporting those as fresh would be worse
// than having no figure — a limit would be compared against a number nobody
// knows the age of.
func TestParseUsageMarksStale(t *testing.T) {
	u := ParseUsage("Current session: 40% used\nShowing last-known usage (fetched 12 minutes ago)")
	if !u.Stale {
		t.Fatal("the staleness note has to be read")
	}
	if u.WindowPercent != 40 {
		t.Fatalf("the figure is still usable: %v", u.WindowPercent)
	}
}

// TestParseUsageFailsOpen is the load-bearing one. The answer is prose and will
// change; what must never happen is that a text we no longer understand turns
// into "0% used", which reads as plenty of room left.
func TestParseUsageFailsOpen(t *testing.T) {
	for _, text := range []string{
		"",
		"Total cost: $0.0000\nTotal duration (API): 0s", // no credential in the sandbox
		"Your plan usage: nearly full",                  // a rewritten format
		"Current session: unknown% used",                // a number we cannot read
		"You have used up 6 of your sessions",           // reworded entirely
	} {
		u := ParseUsage(text)
		if u.Reported() {
			t.Fatalf("unparsable text must not report a figure (%q → %+v)", text, u)
		}
		if u.WindowPercent >= 0 || u.WeekPercent >= 0 {
			t.Fatalf("unknown has to stay negative, never 0 (%q → %+v)", text, u)
		}
	}
}

// The reset time is read as an instant in the zone the text names: the week
// boundary decides whether a lower reading is a new week or a lagging one, and
// "4:59pm" without its zone would be off by hours.
func TestParseUsageReadsTheResetInItsZone(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	u := ParseUsageAt("Current week (all models): 13% used · resets Nov 20, 4:59pm (Europe/Berlin)", now)
	if u.WeekPercent != 13 {
		t.Fatalf("week share = %v, want 13", u.WeekPercent)
	}
	want := time.Date(2026, 11, 20, 15, 59, 0, 0, time.UTC)
	if u.WeekResetsAt == nil || !u.WeekResetsAt.Equal(want) {
		t.Fatalf("week resets at %v, want %v", u.WeekResetsAt, want)
	}
	if u.WeekResets != "Nov 20, 4:59pm (Europe/Berlin)" {
		t.Errorf("the printed reset keeps its zone: %q", u.WeekResets)
	}

	// The real answer's shapes: "Aug 7 at 1:20pm", a session reset as a bare
	// time, and a reset just past the turn of the year.
	u = ParseUsageAt(realUsageOutput, time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC))
	if u.WindowResetsAt == nil || !u.WindowResetsAt.Equal(time.Date(2026, 8, 7, 11, 20, 0, 0, time.UTC)) {
		t.Errorf("session resets at %v", u.WindowResetsAt)
	}
	if u.WeekResetsAt == nil || !u.WeekResetsAt.Equal(time.Date(2026, 8, 9, 4, 59, 0, 0, time.UTC)) {
		t.Errorf("week resets at %v", u.WeekResetsAt)
	}
	at, ok := ParseResets("Jan 2, 4:59pm", "Europe/Berlin", time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC))
	if !ok || !at.Equal(time.Date(2027, 1, 2, 15, 59, 0, 0, time.UTC)) {
		t.Errorf("a reset after new year = %v, %v", at, ok)
	}
	at, ok = ParseResets("6:59pm", "UTC", time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC))
	if !ok || !at.Equal(time.Date(2026, 11, 19, 18, 59, 0, 0, time.UTC)) {
		t.Errorf("a bare time already past today is tomorrow's: %v, %v", at, ok)
	}

	// Without a zone, or with one nobody knows, nothing is guessed.
	for _, zone := range []string{"", "Mars/Olympus"} {
		if _, ok := ParseResets("Nov 20, 4:59pm", zone, now); ok {
			t.Errorf("a reset without a known zone (%q) was read as an instant", zone)
		}
	}
	if u := ParseUsageAt("Current week (all models): 13% used · resets Nov 20", now); u.WeekResetsAt != nil || u.WeekResets != "Nov 20" {
		t.Errorf("no zone: %+v", u)
	}
}

// A stale answer is as old as the engine says, not as old as the question.
func TestParseUsageStaleKeepsTheReportedTime(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	u := ParseUsageAt("Current week (all models): 30% used\nShowing last-known usage (fetched 12 minutes ago)", now)
	if !u.Stale || !u.Fetched.Equal(now.Add(-12*time.Minute)) {
		t.Fatalf("stale %v, fetched %v — want the time 12 minutes before the question", u.Stale, u.Fetched)
	}
	u = ParseUsageAt("Current week (all models): 30% used\nShowing last-known usage", now)
	if !u.Stale || !u.Fetched.IsZero() {
		t.Fatalf("a stale figure without an age must not be stamped with now: %v", u.Fetched)
	}
	if u := ParseUsageAt("Current week (all models): 30% used", now); !u.Fetched.Equal(now) {
		t.Fatalf("a fresh figure is from the moment it was asked: %v", u.Fetched)
	}
}

// failingReporter is an engine whose question fails.
type failingReporter struct{ Mock }

func (failingReporter) Usage(context.Context, []string) (Usage, error) {
	return Usage{WindowPercent: -1, WeekPercent: -1}, errors.New("exit status 1")
}

// unparsedReporter answers, but with nothing to read.
type unparsedReporter struct{ Mock }

func (unparsedReporter) Usage(context.Context, []string) (Usage, error) {
	return ParseUsage("Your plan usage: nearly full"), nil
}

// The question names its engine: a fresh daemon has no config yet when the
// control plane asks — that is the moment it asks — and answered "not
// supported" from the empty config. Every answer without a figure says why.
func TestReportUsageAsksTheNamedEngine(t *testing.T) {
	c := NewClient("", "", "", t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.runtimes["failing"] = failingReporter{}
	c.runtimes["unparsed"] = unparsedReporter{}
	sent := captureSends(t, c)
	report := func(req RequestUsage) UsageReport {
		t.Helper()
		c.reportUsage(context.Background(), req)
		select {
		case m := <-sent:
			rep, err := DecodePayload[UsageReport](m)
			if err != nil || m.Type != TypeUsageReport {
				t.Fatalf("%s: %v", m.Type, err)
			}
			return rep
		case <-time.After(5 * time.Second):
			t.Fatal("no usage report")
		}
		return UsageReport{}
	}

	rep := report(RequestUsage{RequestID: "r1", Runtime: "mock"})
	if !rep.Supported || rep.Usage.WeekPercent != 13 || rep.Reason != "" || rep.RequestID != "r1" {
		t.Fatalf("before any config, the named engine answers: %+v", rep)
	}
	for _, tc := range []struct {
		req    RequestUsage
		reason string
	}{
		{RequestUsage{RequestID: "r2"}, UsageNoRuntimeConfig},
		{RequestUsage{RequestID: "r3", Runtime: "codex"}, UsageNoReporter},
		{RequestUsage{RequestID: "r4", Runtime: "gibtesnicht"}, UsageNoReporter},
		{RequestUsage{RequestID: "r5", Runtime: "failing"}, UsageExecFailed},
		{RequestUsage{RequestID: "r6", Runtime: "unparsed"}, UsageUnparsed},
	} {
		rep := report(tc.req)
		if rep.Supported || rep.Reason != tc.reason {
			t.Errorf("%+v: supported %v, reason %q — want %q", tc.req, rep.Supported, rep.Reason, tc.reason)
		}
	}

	// A control plane from before the field names no engine: the config is
	// the fallback.
	c.mu.Lock()
	c.cfg.Runtime = "mock"
	c.mu.Unlock()
	if rep := report(RequestUsage{RequestID: "r7"}); !rep.Supported {
		t.Errorf("the config's engine was not asked: %+v", rep)
	}
}
