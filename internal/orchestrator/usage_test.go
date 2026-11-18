package orchestrator

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/daemon"
	"covey/internal/observability"
)

// The engine's own utilisation figure is what decides whether a seat is skipped
// — and the figure ages. What the cache holds is that distinction: a fresh
// number acts, an old one does not, and an engine that cannot answer at all is
// remembered without an expiry, because that property does not change between
// two runs.
func TestUsageCacheExpiresFiguresButNotUnsupported(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	c := newUsageCache()
	c.now = func() time.Time { return now }
	k := usageKey{uuid.New(), 0}

	if _, ok := c.get(k); ok {
		t.Error("an empty cache answered")
	}

	c.put(k, usageEntry{usage: daemon.Usage{WindowPercent: 40}, at: now})
	if e, fresh := c.get(k); !fresh || e.usage.WindowPercent != 40 {
		t.Errorf("a fresh figure = %+v, fresh=%v", e, fresh)
	}

	// Older than the TTL: remembered, but no longer something to act on.
	c.put(k, usageEntry{usage: daemon.Usage{WindowPercent: 40}, at: now.Add(-2 * usageCacheTTL)})
	if _, fresh := c.get(k); fresh {
		t.Error("a figure past its TTL still counts as fresh")
	}

	// "This engine cannot say" has no expiry.
	c.put(k, usageEntry{unsupported: true, reason: daemon.UsageNoReporter, at: now.Add(-100 * usageFailureTTL)})
	e, fresh := c.get(k)
	if !fresh || !e.unsupported {
		t.Errorf("an engine that cannot answer was forgotten: %+v, %v", e, fresh)
	}
}

// Every other answer without a figure is asked about again after the failure
// TTL: one failed question — a binary that did not start, a daemon without its
// config yet — must not silence the meter for the life of the process. That
// is what the first version did.
func TestUsageCacheForgetsAFailedQuestion(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	c := newUsageCache()
	c.now = func() time.Time { return now }
	for _, reason := range []string{daemon.UsageExecFailed, daemon.UsageUnparsed, daemon.UsageNoRuntimeConfig, ""} {
		k := usageKey{uuid.New(), 0}
		c.put(k, usageEntry{unsupported: true, reason: reason})
		if _, fresh := c.get(k); !fresh {
			t.Errorf("%q: a failure just filed is not remembered", reason)
		}
		now = now.Add(usageFailureTTL - time.Second)
		if _, fresh := c.get(k); !fresh {
			t.Errorf("%q: forgotten before the TTL", reason)
		}
		now = now.Add(2 * time.Second)
		if _, fresh := c.get(k); fresh {
			t.Errorf("%q: still remembered after the TTL — the meter would never be asked again", reason)
		}
	}
	k := usageKey{uuid.New(), 0}
	c.put(k, usageEntry{unsupported: true, reason: daemon.UsageNoReporter})
	now = now.Add(1000 * usageFailureTTL)
	if _, fresh := c.get(k); !fresh {
		t.Error("no_reporter is a property of the engine and stays remembered")
	}
}

func TestUsageOnlyAnswersWhatCanBeActedOn(t *testing.T) {
	o := &Orchestrator{usage: newUsageCache()}
	rt := uuid.New()

	// Nothing known.
	if _, ok := o.Usage(rt, 0); ok {
		t.Error("an empty cache reported a figure")
	}

	// Known, but the engine cannot say.
	o.usage.put(usageKey{rt, 0}, usageEntry{unsupported: true, at: time.Now()})
	if _, ok := o.Usage(rt, 0); ok {
		t.Error("an engine that cannot answer was reported as a figure")
	}

	// Known and reported.
	o.usage.put(usageKey{rt, 1}, usageEntry{usage: daemon.Usage{WindowPercent: 62}, at: time.Now()})
	got, ok := o.Usage(rt, 1)
	if !ok || got.WindowPercent != 62 {
		t.Errorf("Usage = %+v, %v", got, ok)
	}

	// An orchestrator without a cache answers nothing rather than panicking —
	// the tools and the tests build one without it.
	if _, ok := (&Orchestrator{}).Usage(rt, 1); ok {
		t.Error("an orchestrator without a cache reported a figure")
	}
}

// A STALE figure is withheld rather than passed on with a flag. It is up to an
// hour old, and the decision it would feed is "skip this credential" — taking a
// seat out of play on an hour-old number would cost exactly the capacity the
// mechanism exists to use up.
func TestReportedUtilisationWithholdsStaleAndUnknown(t *testing.T) {
	o := &Orchestrator{usage: newUsageCache()}
	rt := uuid.New()

	o.usage.put(usageKey{rt, 0}, usageEntry{
		usage: daemon.Usage{WindowPercent: 95, Stale: true}, at: time.Now()})
	if _, ok := o.reportedUtilisation(rt, 0); ok {
		t.Error("a stale figure was passed on")
	}

	o.usage.put(usageKey{rt, 1}, usageEntry{
		usage: daemon.Usage{WindowPercent: -1}, at: time.Now()})
	if _, ok := o.reportedUtilisation(rt, 1); ok {
		t.Error("a figure below zero was passed on")
	}

	o.usage.put(usageKey{rt, 2}, usageEntry{
		usage: daemon.Usage{WindowPercent: 80}, at: time.Now()})
	pct, ok := o.reportedUtilisation(rt, 2)
	if !ok || pct != 80 {
		t.Errorf("reportedUtilisation = %v, %v", pct, ok)
	}
}

// noteUsage is where an answer lands. What matters is that "supported=false"
// is filed as such rather than as a zero — a seat at 0 % would be the emptiest
// one and get every agent.
func TestNoteUsageFilesUnsupportedAsUnsupported(t *testing.T) {
	o := &Orchestrator{Options: Options{Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}, usage: newUsageCache()}
	rt := uuid.New()
	cred := func(ord int) observability.CredentialRef {
		return observability.CredentialRef{RuntimeID: rt, Ord: ord, SecretKey: "claude_code_oauth_token", SecretSlot: ord}
	}

	o.noteUsage(daemon.UsageReport{Supported: false, Reason: daemon.UsageExecFailed}, cred(0))
	e, fresh := o.usage.get(usageKey{rt, 0})
	if !fresh || !e.unsupported || e.reason != daemon.UsageExecFailed {
		t.Errorf("an unsupported answer was filed as %+v", e)
	}
	if _, ok := o.reportedUtilisation(rt, 0); ok {
		t.Error("an engine that cannot answer was read as 0 %")
	}

	o.noteUsage(daemon.UsageReport{Supported: true, Usage: daemon.Usage{WindowPercent: 12}}, cred(1))
	if pct, ok := o.reportedUtilisation(rt, 1); !ok || pct != 12 {
		t.Errorf("reportedUtilisation = %v, %v", pct, ok)
	}

	// A runtime nobody named, or a negative slot, is filed nowhere rather than
	// under the zero uuid.
	o.noteUsage(daemon.UsageReport{Supported: true}, observability.CredentialRef{})
	o.noteUsage(daemon.UsageReport{Supported: true}, observability.CredentialRef{RuntimeID: rt, Ord: -1})
	if _, ok := o.usage.get(usageKey{uuid.Nil, 0}); ok {
		t.Error("a figure was filed under the zero uuid")
	}
}

// An answer without a figure is said once per credential and reason, not on
// every wake: loud enough to be found, quiet enough to be read.
func TestNoteUsageWarnsOncePerCredentialAndReason(t *testing.T) {
	var buf bytes.Buffer
	o := &Orchestrator{Options: Options{Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))}, usage: newUsageCache()}
	cred := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "claude_code_oauth_token"}
	for range 3 {
		o.noteUsage(daemon.UsageReport{Reason: daemon.UsageExecFailed, Error: "exit status 1: sk-ant-oat-geheim"}, cred)
	}
	o.noteUsage(daemon.UsageReport{Reason: daemon.UsageUnparsed}, cred)
	if n := strings.Count(buf.String(), "utilisation not reported"); n != 2 {
		t.Errorf("%d warnings, want one per reason: %s", n, buf.String())
	}
	if strings.Contains(buf.String(), "geheim") {
		t.Error("the daemon's error text reached the log")
	}
}

// The reason an agent has no credential decides where the reader goes next:
// no workplace is a different fix from a workplace without a value in it.
func TestNoCredentialReasonNamesTheRightPlace(t *testing.T) {
	if got := noCredentialReason(agents.Agent{}); got == "" {
		t.Fatal("an agent without a workplace gets no reason")
	}
	if got := noCredentialReason(agents.Agent{}); !contains(got, "Runtimes") {
		t.Errorf("without a workplace the reason points at %q", got)
	}
	id := uuid.New()
	withSeat := noCredentialReason(agents.Agent{RuntimeID: &id})
	if !contains(withSeat, "Secrets") {
		t.Errorf("with a workplace the reason points at %q", withSeat)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("kurz", 10); got != "kurz" {
		t.Errorf("truncateRunes = %q", got)
	}
	// Cut by runes, not bytes: an umlaut must not be halved.
	if got := truncateRunes("ätherisch", 3); got != "äth …" {
		t.Errorf("truncateRunes = %q", got)
	}
	// The cut trims what it cut to, then marks the cut — a text that ends in
	// whitespace at the boundary must not carry it into the ellipsis.
	if got := truncateRunes("ein satz  und weiter", 10); got != "ein satz …" {
		t.Errorf("truncateRunes = %q", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// A used-up seat is parked for an hour, not for the window's nominal length:
// the window ROLLS, so it frees up gradually, and being wrong costs one run
// while the agent works on another seat. A rejected token is parked far longer,
// because it does not recover at all.
func TestTheTwoCooldownsAreDeliberatelyDifferent(t *testing.T) {
	if cooldownSeatWindow >= cooldownRejected {
		t.Error("a used-up seat is parked as long as a revoked token")
	}
	if cooldownRateLimit > cooldownRejected {
		t.Error("a rate limit is parked longer than a revoked token")
	}
	if cooldownSeatWindow <= 0 || cooldownRateLimit <= 0 || cooldownRejected <= 0 {
		t.Error("a cooldown of zero parks nothing")
	}
}

// A stale answer is filed with the age the engine states. One that states
// none has no moment its figures belong to: filed with the time it arrived it
// would read as current, so it is kept as a stale answer without figures.
func TestUsageReadingOfStaleAnswer(t *testing.T) {
	cred := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "seat", Kind: "subscription"}
	resets := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	loaded := time.Now().Add(-40 * time.Minute)
	answer := func(stale bool, fetched time.Time) daemon.UsageReport {
		return daemon.UsageReport{Supported: true, Usage: daemon.Usage{WindowPercent: 4, WeekPercent: 13,
			WeekResets: "Nov 20, 5pm (Europe/Berlin)", WeekResetsAt: &resets, Stale: stale, Fetched: fetched}}
	}

	ageless := usageReadingOf(answer(true, time.Time{}), cred)
	if !ageless.Supported || !ageless.Stale || ageless.Reason != daemon.UsageStale {
		t.Errorf("a stale answer without an age is not kept as stale: %+v", ageless)
	}
	if ageless.WeekPercent >= 0 || ageless.WindowPercent >= 0 || !ageless.ResetsAt.IsZero() || ageless.WeekResets != "" {
		t.Errorf("a stale answer without an age was filed with its figures (as read now): %+v", ageless)
	}

	aged := usageReadingOf(answer(true, loaded), cred)
	if aged.WeekPercent != 13 || aged.WindowPercent != 4 || !aged.FetchedAt.Equal(loaded) || !aged.Stale ||
		aged.Reason != daemon.UsageStale || !aged.ResetsAt.Equal(resets) {
		t.Errorf("a stale answer with its age: %+v — want its figures at the time the engine loaded them", aged)
	}

	fresh := usageReadingOf(answer(false, loaded), cred)
	if fresh.WeekPercent != 13 || fresh.Stale || fresh.Reason != "" || !fresh.FetchedAt.Equal(loaded) {
		t.Errorf("a fresh answer: %+v", fresh)
	}

	none := usageReadingOf(daemon.UsageReport{Reason: daemon.UsageExecFailed, Usage: daemon.Usage{WeekPercent: -1, WindowPercent: -1}}, cred)
	if none.Supported || none.Stale || !none.FetchedAt.IsZero() || none.WeekPercent >= 0 {
		t.Errorf("an answer without a figure: %+v", none)
	}
}
