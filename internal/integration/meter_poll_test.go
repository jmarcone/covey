package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// covey's own meter poll (#153), the store half: which seats are due
// (MeterPollDue), how a polled rate_limit_event is filed (AddPolledReadings)
// and what the split makes of it, the poll sources' watermarks
// (AdvanceMeterPollMarks), and the push endpoint keeping uploaders out of the
// poll's sources. Every time is measured from
// pollNow, never from the wall clock, with one exception:
// TestMeterPollReadingExtendsTheInbandSeries uses time.Now() with relative
// offsets, because the orchestrator clock and the cells endpoint cannot be
// injected from this package.

var pollNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const (
	pollFresh  = 10 * time.Minute
	pollActive = time.Hour
)

// pollOrg is an organisation of its own, so that each case reads only its
// seats out of MeterPollDue's answer across all organisations.
func (s *stack) pollOrg(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO organizations (id, name) VALUES ($1, 'Poll-Org')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// pollRuntime is a Claude Code runtime of org created at created (the first
// place of a seat is decided by it).
func (s *stack) pollRuntime(t *testing.T, org uuid.UUID, created time.Time) uuid.UUID {
	t.Helper()
	return s.pollEngineRuntime(t, org, daemon.EngineClaudeCode, created)
}

// pollEngineRuntime is a runtime of org on engine, created at created.
func (s *stack) pollEngineRuntime(t *testing.T, org uuid.UUID, engine string, created time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO runtimes (id, org_id, engine, display_name, created_at)
		VALUES ($1, $2, $3, 'Poll', $4)`, id, org, engine, created); err != nil {
		t.Fatal(err)
	}
	return id
}

// pollCred hangs the secret key#slot on rt at ord.
func (s *stack) pollCred(t *testing.T, rt uuid.UUID, ord int, kind, key string, slot int) observability.CredentialRef {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO runtime_credentials (runtime_id, ord, kind, secret_key, secret_slot)
		VALUES ($1, $2, $3, $4, $5)`, rt, ord, kind, key, slot); err != nil {
		t.Fatal(err)
	}
	return observability.CredentialRef{RuntimeID: rt, Ord: ord, SecretKey: key, SecretSlot: slot, Kind: kind}
}

// pollSeat is a subscription seat on a runtime of its own.
func (s *stack) pollSeat(t *testing.T, org uuid.UUID, key string) observability.CredentialRef {
	t.Helper()
	return s.pollCred(t, s.pollRuntime(t, org, pollNow.Add(-24*time.Hour)), 0, daemon.CredSubscription, key, 0)
}

// pollLink links source to the seat key#0 and records a push of host at
// pushedAt.
func (s *stack) pollLink(t *testing.T, org uuid.UUID, source, host, key string, pushedAt time.Time) {
	t.Helper()
	s.pollLinkSlot(t, org, source, host, key, 0, pushedAt)
}

// pollLinkSlot is pollLink for the seat key#slot.
func (s *stack) pollLinkSlot(t *testing.T, org uuid.UUID, source, host, key string, slot int, pushedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO external_usage_meters (org_id, source, secret_key, secret_slot)
		VALUES ($1, $2, $3, $4)`, org, source, key, slot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO external_usage_hosts (org_id, source, host, pushed_at)
		VALUES ($1, $2, $3, $4)`, org, source, host, pushedAt); err != nil {
		t.Fatal(err)
	}
}

// pollReading files a weekly reading of cred received at received.
func (s *stack) pollReading(t *testing.T, org uuid.UUID, cred observability.CredentialRef, source string,
	supported, stale bool, received time.Time) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO usage_readings (org_id, runtime_id, credential_ord,
		secret_key, secret_slot, credential_kind, source, window_kind, supported, week_percent, stale, fetched_at, received_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'seven_day', $8, 20, $9, $10, $10)`,
		org, cred.RuntimeID, cred.Ord, cred.SecretKey, cred.SecretSlot, cred.Kind, source, supported, stale, received); err != nil {
		t.Fatal(err)
	}
}

// pollDue is MeterPollDue at now, the seats of org only, by secret key.
func (s *stack) pollDue(t *testing.T, org uuid.UUID, now time.Time) map[string]observability.MeterPollCandidate {
	t.Helper()
	all, err := s.obs.MeterPollDue(context.Background(), now, pollFresh, pollActive)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]observability.MeterPollCandidate{}
	for _, c := range all {
		if c.OrgID != org {
			continue
		}
		k := c.Credential.SecretKey
		if c.Credential.SecretSlot != 0 {
			k += "#" + string(rune('0'+c.Credential.SecretSlot))
		}
		if _, dup := out[k]; dup {
			t.Errorf("seat %s returned twice", k)
		}
		out[k] = c
	}
	return out
}

// A seat is due through a linked source whose machine pushed within active,
// not through one whose last push is older, and never through a source of
// the poll itself: each poll books a call there, and would call for the next.
// A source that only bears a poll name, pushed from another host than
// covey's (an upload from before such names were refused), is an outside
// source like any other.
func TestMeterPollDueThroughLinkedSource(t *testing.T) {
	s := newStack(t)
	org := s.pollOrg(t)
	recent := s.pollSeat(t, org, "seat_recent")
	s.pollSeat(t, org, "seat_old")
	s.pollSeat(t, org, "seat_polled")
	s.pollSeat(t, org, "seat_bare")
	s.pollSeat(t, org, "seat_quiet")
	named := s.pollSeat(t, org, "seat_named")
	s.pollLink(t, org, "operator: home", "laptop", "seat_recent", pollNow.Add(-30*time.Minute))
	s.pollLink(t, org, "operator: old", "laptop", "seat_old", pollNow.Add(-2*time.Hour))
	s.pollLink(t, org, observability.MeterPollSourceFor("seat_polled", 0), observability.MeterPollHost, "seat_polled", pollNow.Add(-time.Minute))
	s.pollLink(t, org, observability.MeterPollSource, observability.MeterPollHost, "seat_bare", pollNow.Add(-time.Minute))
	// A push exactly `active` ago is no longer within it.
	s.pollLink(t, org, "operator: edge", "laptop", "seat_quiet", pollNow.Add(-pollActive))
	s.pollLink(t, org, observability.MeterPollSourceFor("seat_named", 0), "laptop", "seat_named", pollNow.Add(-30*time.Minute))

	due := s.pollDue(t, org, pollNow)
	if len(due) != 2 {
		t.Fatalf("due seats %v, want seat_recent and seat_named", due)
	}
	for k, cred := range map[string]observability.CredentialRef{"seat_recent": recent, "seat_named": named} {
		c, ok := due[k]
		if !ok {
			t.Fatalf("%s not due: %v", k, due)
		}
		if c.Reason != observability.MeterPollOutside || c.Credential != cred {
			t.Errorf("%s: candidate %+v, want reason outside through %+v", k, c, cred)
		}
	}
}

// A seat with a supported, fresh reading (in band, the poll's own among them,
// or from /usage) received within fresh is not due; an older one, an
// unsupported or stale one, or one entered by hand does not count.
func TestMeterPollDueFreshReading(t *testing.T) {
	s := newStack(t)
	org := s.pollOrg(t)
	cases := []struct {
		key       string
		source    string
		supported bool
		stale     bool
		age       time.Duration
		due       bool
	}{
		{"fresh_inband", observability.SourceInband, true, false, 5 * time.Minute, false},
		{"fresh_usage_cmd", observability.SourceUsageCmd, true, false, 5 * time.Minute, false},
		{"old_inband", observability.SourceInband, true, false, 15 * time.Minute, true},
		{"unsupported", observability.SourceInband, false, false, 5 * time.Minute, true},
		{"stale", observability.SourceInband, true, true, 5 * time.Minute, true},
		{"manual", observability.SourceManual, true, false, 5 * time.Minute, true},
	}
	for _, c := range cases {
		cred := s.pollSeat(t, org, c.key)
		s.pollLink(t, org, "operator: "+c.key, "laptop", c.key, pollNow.Add(-30*time.Minute))
		s.pollReading(t, org, cred, c.source, c.supported, c.stale, pollNow.Add(-c.age))
	}
	due := s.pollDue(t, org, pollNow)
	for _, c := range cases {
		if _, got := due[c.key]; got != c.due {
			t.Errorf("%s: due = %v, want %v", c.key, got, c.due)
		}
	}
}

// A paused fleet makes a seat due that an outside source is linked to, even
// one whose host has not pushed for a while: the switch is off, or every
// hired agent of the organisation is killed. A draft (not hired) agent runs
// nothing, killed or not, and does not keep an organisation from counting as
// paused. An organisation with one hired agent still alive, or with no hired
// agent at all, is not paused. With an active link as well,
// the reason is the link. A seat no outside source is linked to — none at
// all, or only the poll's own — is never due, paused or not: there is no
// outside work on it to split.
func TestMeterPollDueFleetPaused(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	// A hired agent; draft leaves hired_at NULL, as a draft has it.
	addAgent := func(org uuid.UUID, slug string, killed, draft bool) {
		t.Helper()
		hired := &pollNow
		if draft {
			hired = nil
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO agents (id, org_id, slug, display_name, killed, hired_at)
			VALUES ($1, $2, $3, $3, $4, $5)`, uuid.New(), org, slug, killed, hired); err != nil {
			t.Fatal(err)
		}
	}
	agent := func(org uuid.UUID, slug string, killed bool) { t.Helper(); addAgent(org, slug, killed, false) }

	switchOff := s.pollOrg(t)
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, switchOff); err != nil {
		t.Fatal(err)
	}
	agent(switchOff, "alive", false)
	// Linked to an outside source that last pushed a day ago.
	idle := func(org uuid.UUID, key string) {
		t.Helper()
		s.pollSeat(t, org, key)
		s.pollLink(t, org, "operator: "+key, "laptop", key, pollNow.Add(-24*time.Hour))
	}
	idle(switchOff, "seat_a")
	s.pollSeat(t, switchOff, "seat_linked")
	s.pollLink(t, switchOff, "operator: home", "laptop", "seat_linked", pollNow.Add(-30*time.Minute))
	read := s.pollSeat(t, switchOff, "seat_read")
	s.pollLink(t, switchOff, "operator: read", "laptop", "seat_read", pollNow.Add(-24*time.Hour))
	s.pollReading(t, switchOff, read, observability.SourceInband, true, false, pollNow.Add(-time.Minute))
	s.pollSeat(t, switchOff, "seat_unlinked")
	s.pollSeat(t, switchOff, "seat_poll_only")
	s.pollLink(t, switchOff, observability.MeterPollSourceFor("seat_poll_only", 0), observability.MeterPollHost,
		"seat_poll_only", pollNow.Add(-time.Minute))

	allKilled := s.pollOrg(t)
	agent(allKilled, "a", true)
	agent(allKilled, "b", true)
	idle(allKilled, "seat_a")

	// Every hired agent killed, and a draft that is not: still paused.
	draftAlive := s.pollOrg(t)
	agent(draftAlive, "a", true)
	addAgent(draftAlive, "draft", false, true)
	idle(draftAlive, "seat_a")

	// Only drafts: no hired agent, so nothing says the fleet is paused.
	onlyDrafts := s.pollOrg(t)
	addAgent(onlyDrafts, "draft", true, true)
	idle(onlyDrafts, "seat_a")

	oneAlive := s.pollOrg(t)
	agent(oneAlive, "a", true)
	agent(oneAlive, "b", false)
	idle(oneAlive, "seat_a")

	noAgents := s.pollOrg(t)
	idle(noAgents, "seat_a")

	// Paused, and not one seat an outside source names: nothing at all.
	noLinks := s.pollOrg(t)
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, noLinks); err != nil {
		t.Fatal(err)
	}
	s.pollSeat(t, noLinks, "seat_a")
	s.pollSeat(t, noLinks, "seat_b")
	s.pollLink(t, noLinks, observability.MeterPollSourceFor("seat_b", 0), observability.MeterPollHost,
		"seat_b", pollNow.Add(-time.Minute))

	due := s.pollDue(t, switchOff, pollNow)
	if c, ok := due["seat_a"]; !ok || c.Reason != observability.MeterPollPaused {
		t.Errorf("fleet switched off: seat_a %+v (due %v), want reason paused", c, ok)
	}
	if c, ok := due["seat_linked"]; !ok || c.Reason != observability.MeterPollOutside {
		t.Errorf("fleet switched off with an active link: %+v (due %v), want reason outside", c, ok)
	}
	if _, ok := due["seat_read"]; ok {
		t.Errorf("a seat read a minute ago is due")
	}
	if _, ok := due["seat_unlinked"]; ok {
		t.Errorf("a seat no source is linked to is due")
	}
	if _, ok := due["seat_poll_only"]; ok {
		t.Errorf("a seat linked to the poll's own source only is due")
	}
	if len(due) != 2 {
		t.Errorf("fleet switched off: due %v, want seat_a and seat_linked", due)
	}
	if c, ok := s.pollDue(t, allKilled, pollNow)["seat_a"]; !ok || c.Reason != observability.MeterPollPaused {
		t.Errorf("every agent killed: %+v (due %v), want reason paused", c, ok)
	}
	if c, ok := s.pollDue(t, draftAlive, pollNow)["seat_a"]; !ok || c.Reason != observability.MeterPollPaused {
		t.Errorf("every hired agent killed, a draft not: %+v (due %v), want reason paused", c, ok)
	}
	if due := s.pollDue(t, onlyDrafts, pollNow); len(due) != 0 {
		t.Errorf("only drafts: due %v, want none", due)
	}
	if due := s.pollDue(t, oneAlive, pollNow); len(due) != 0 {
		t.Errorf("one agent alive: due %v, want none", due)
	}
	if due := s.pollDue(t, noAgents, pollNow); len(due) != 0 {
		t.Errorf("no agents: due %v, want none", due)
	}
	if due := s.pollDue(t, noLinks, pollNow); len(due) != 0 {
		t.Errorf("paused without an outside link: due %v, want none", due)
	}
}

// Only a Claude Code seat is read: a subscription of another engine — Codex's
// ChatGPT plan, an educa contract seat — is another kind of secret, never
// handed to the claude CLI, and is not due even in a paused organisation with
// an outside source linked to it that pushed a moment ago.
func TestMeterPollDueClaudeCodeOnly(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	org := s.pollOrg(t)
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ engine, key string }{
		{"codex", "codex_auth_json"},
		{"educa-ai", "educa_seat_token"},
		{daemon.EngineClaudeCode, "claude_seat"},
	} {
		rt := s.pollEngineRuntime(t, org, c.engine, pollNow.Add(-24*time.Hour))
		s.pollCred(t, rt, 0, daemon.CredSubscription, c.key, 0)
		s.pollLink(t, org, "operator: "+c.key, "laptop", c.key, pollNow.Add(-5*time.Minute))
	}
	due := s.pollDue(t, org, pollNow)
	if len(due) != 1 {
		t.Fatalf("due %v, want the Claude Code seat only", due)
	}
	if _, ok := due["claude_seat"]; !ok {
		t.Errorf("the Claude Code seat is not due: %v", due)
	}

	// The same secret on a Codex runtime created earlier does not become the
	// place the seat is read through.
	shared := "shared_value"
	codex := s.pollEngineRuntime(t, org, "codex", pollNow.Add(-48*time.Hour))
	s.pollCred(t, codex, 0, daemon.CredSubscription, shared, 0)
	claude := s.pollCred(t, s.pollRuntime(t, org, pollNow.Add(-time.Hour)), 0, daemon.CredSubscription, shared, 0)
	s.pollLink(t, org, "operator: shared", "laptop", shared, pollNow.Add(-5*time.Minute))
	if c := s.pollDue(t, org, pollNow)[shared]; c.Credential != claude {
		t.Errorf("shared read through %+v, want the Claude Code place %+v", c.Credential, claude)
	}
}

// Only a subscription in play is read: a credential paused by hand, one in a
// cooldown and an API key never are. A cooldown that has ended no longer
// counts.
func TestMeterPollDueSkipsPausedCooldownAndAPIKey(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	org := s.pollOrg(t)
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	paused := s.pollSeat(t, org, "paused")
	cooling := s.pollSeat(t, org, "cooling")
	cooled := s.pollSeat(t, org, "cooled")
	s.pollCred(t, s.pollRuntime(t, org, pollNow.Add(-time.Hour)), 0, daemon.CredAPIKey, "api", 0)
	for _, key := range []string{"paused", "cooling", "cooled", "api"} {
		s.pollLink(t, org, "operator: "+key, "laptop", key, pollNow.Add(-5*time.Minute))
	}
	for _, u := range []struct {
		sql  string
		cred observability.CredentialRef
		at   time.Time
	}{
		{`UPDATE runtime_credentials SET paused_at = $3 WHERE runtime_id = $1 AND ord = $2`, paused, pollNow.Add(-time.Hour)},
		{`UPDATE runtime_credentials SET cooldown_until = $3 WHERE runtime_id = $1 AND ord = $2`, cooling, pollNow.Add(time.Minute)},
		{`UPDATE runtime_credentials SET cooldown_until = $3 WHERE runtime_id = $1 AND ord = $2`, cooled, pollNow.Add(-time.Minute)},
	} {
		if _, err := s.pool.Exec(ctx, u.sql, u.cred.RuntimeID, u.cred.Ord, u.at); err != nil {
			t.Fatal(err)
		}
	}
	due := s.pollDue(t, org, pollNow)
	if len(due) != 1 {
		t.Fatalf("due %v, want the seat whose cooldown ended only", due)
	}
	if _, ok := due["cooled"]; !ok {
		t.Errorf("cooled not due: %v", due)
	}
}

// The same secret value at several places is one seat: returned once, through
// its first place in play by the runtime's creation, its id, then ord. Another
// slot of the key is another seat. The answer is ordered by organisation,
// key and slot.
func TestMeterPollDueOneSeatPerSecret(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	org := s.pollOrg(t)
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	// Created in the opposite order of their creation times.
	late := s.pollRuntime(t, org, pollNow.Add(-time.Hour))
	early := s.pollRuntime(t, org, pollNow.Add(-2*time.Hour))
	s.pollCred(t, late, 0, daemon.CredSubscription, "shared", 0)
	s.pollCred(t, early, 0, daemon.CredSubscription, "other", 0)
	first := s.pollCred(t, early, 1, daemon.CredSubscription, "shared", 0)
	s.pollCred(t, early, 2, daemon.CredSubscription, "shared", 0)
	s.pollCred(t, late, 1, daemon.CredSubscription, "shared", 1)
	s.pollLink(t, org, "operator: shared", "laptop", "shared", pollNow.Add(-24*time.Hour))
	s.pollLinkSlot(t, org, "operator: shared#1", "laptop", "shared", 1, pollNow.Add(-24*time.Hour))
	s.pollLink(t, org, "operator: other", "laptop", "other", pollNow.Add(-24*time.Hour))

	due := s.pollDue(t, org, pollNow)
	if c := due["shared"]; c.Credential != first {
		t.Errorf("shared read through %+v, want the earliest runtime's lowest ord %+v", c.Credential, first)
	}
	if _, ok := due["shared#1"]; !ok || len(due) != 3 {
		t.Errorf("due %v, want shared, shared#1 and other", due)
	}

	// Paused there, the seat is read through its next place.
	if _, err := s.pool.Exec(ctx, `UPDATE runtime_credentials SET paused_at = $3 WHERE runtime_id = $1 AND ord = $2`,
		first.RuntimeID, first.Ord, pollNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c := s.pollDue(t, org, pollNow)["shared"]; c.Credential.RuntimeID != early || c.Credential.Ord != 2 {
		t.Errorf("with the first place paused: %+v, want ord 2 of the early runtime", c.Credential)
	}

	// The whole answer, across organisations, in order.
	second := s.pollOrg(t)
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	s.pollSeat(t, second, "b")
	s.pollSeat(t, second, "a")
	s.pollLink(t, second, "operator: b", "laptop", "b", pollNow.Add(-24*time.Hour))
	s.pollLink(t, second, "operator: a", "laptop", "a", pollNow.Add(-24*time.Hour))
	all, err := s.obs.MeterPollDue(ctx, pollNow, pollFresh, pollActive)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.OrgID != b.OrgID {
			return a.OrgID.String() < b.OrgID.String()
		}
		if a.Credential.SecretKey != b.Credential.SecretKey {
			return a.Credential.SecretKey < b.Credential.SecretKey
		}
		return a.Credential.SecretSlot < b.Credential.SecretSlot
	}) {
		t.Errorf("the answer is not ordered by organisation, key and slot: %+v", all)
	}
}

// A polled rate_limit_event is filed as the in-band one is — one row per
// window, five-hour in window_percent, weekly in week_percent, source inband —
// received at the instant the poll gives (the one its watermark carries), and
// counts as a fresh reading for the next MeterPollDue. A run's in-band filing
// keeps the database's time.
func TestMeterPollReadingsFiled(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	org := s.pollOrg(t)
	polled := s.pollSeat(t, org, "polled")
	inband := s.pollSeat(t, org, "inband")

	if err := s.obs.AddPolledReadings(ctx, org, polled, json.RawMessage(twoWindows), pollNow); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddInbandReadings(ctx, org, inband, json.RawMessage(event9802)); err != nil {
		t.Fatal(err)
	}
	// Nothing without a credential, nothing for another line.
	if err := s.obs.AddPolledReadings(ctx, org, observability.CredentialRef{}, json.RawMessage(twoWindows), pollNow); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddPolledReadings(ctx, org, polled, json.RawMessage(resultLine), pollNow); err != nil {
		t.Fatal(err)
	}

	type row struct {
		key, src, window, status, kind string
		rt                             uuid.UUID
		ord, slot                      int
		five, week                     *float64
		resets                         *time.Time
		supported, stale               bool
		received, fetched              time.Time
	}
	rows, err := s.pool.Query(ctx, `SELECT secret_key, source, window_kind, status, credential_kind, runtime_id,
		credential_ord, secret_slot, window_percent, week_percent, resets_at, supported, stale, received_at, fetched_at
		FROM usage_readings WHERE org_id = $1 ORDER BY secret_key, window_kind`, org)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.src, &r.window, &r.status, &r.kind, &r.rt, &r.ord, &r.slot,
			&r.five, &r.week, &r.resets, &r.supported, &r.stale, &r.received, &r.fetched); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 3 {
		t.Fatalf("%d readings, want one in band and two polled: %+v", len(got), got)
	}
	in, five, week := got[0], got[1], got[2]
	if in.key != "inband" || in.src != observability.SourceInband || in.window != "seven_day" || in.week == nil || *in.week != 13 {
		t.Errorf("in-band reading: %+v", in)
	}
	for _, r := range []row{five, week} {
		if r.key != "polled" || r.src != observability.SourceInband || r.rt != polled.RuntimeID || r.ord != 0 ||
			r.slot != 0 || r.kind != daemon.CredSubscription || !r.supported || r.stale {
			t.Errorf("polled reading filed as %+v", r)
		}
		if !r.received.Equal(pollNow) || !r.fetched.Equal(pollNow) {
			t.Errorf("polled reading received %v, fetched %v, want the poll's %v", r.received, r.fetched, pollNow)
		}
	}
	if five.window != "five_hour" || five.five == nil || *five.five != 41 || five.week != nil || five.status != "allowed_warning" ||
		five.resets == nil || !five.resets.Equal(time.Unix(1795100000, 0)) {
		t.Errorf("five-hour reading: %+v", five)
	}
	if week.window != "seven_day" || week.week == nil || *week.week != 13.5 || week.five != nil || week.status != "allowed" ||
		week.resets == nil || !week.resets.Equal(time.Unix(1795190400, 0)) {
		t.Errorf("weekly reading: %+v", week)
	}

	if in.received.Equal(pollNow) {
		t.Errorf("the in-band reading took the poll's time")
	}

	// Read just now, with the fleet paused and an outside source linked, the
	// polled seat is not due again; the in-band one is not either, and each
	// is once fresh has passed since its reading.
	if _, err := s.pool.Exec(ctx, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	s.pollLink(t, org, "operator: polled", "laptop", "polled", pollNow.Add(-24*time.Hour))
	s.pollLink(t, org, "operator: inband", "laptop", "inband", pollNow.Add(-24*time.Hour))
	for _, c := range []struct {
		key string
		at  time.Time
	}{{"polled", pollNow}, {"inband", in.received}} {
		if _, ok := s.pollDue(t, org, c.at.Add(time.Minute))[c.key]; ok {
			t.Errorf("%s: due a minute after its reading", c.key)
		}
		if _, ok := s.pollDue(t, org, c.at.Add(pollFresh+time.Minute))[c.key]; !ok {
			t.Errorf("%s: not due once fresh has passed", c.key)
		}
	}
}

// AdvanceMeterPollMarks moves the watermark of the poll's hosts, and only
// theirs, up to now: never back, never pushed_at, and a host without one gets
// its first (with first_reported_at, as the table's CHECK wants). The poll's
// host is covey's on a source of a poll name: another host of such a source,
// on a name an uploader took before such names were refused, keeps its own.
func TestMeterPollAdvanceMarks(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	org := s.pollOrg(t)
	s.pollSeat(t, org, "seat")
	poll := observability.MeterPollSourceFor("seat", 0)

	// The poll's own booking, as the orchestrator makes it: through the
	// store, linked to the seat, with the host's watermark at the call.
	booked := pollNow.Add(-50 * time.Minute)
	if _, err := s.obs.UpsertExternalUsage(ctx, org, poll,
		observability.MeterLinkChange{Set: true, Credential: &observability.MeterCredential{SecretKey: "seat"}},
		nil, map[string]time.Time{observability.MeterPollHost: booked}, booked); err != nil {
		t.Fatal(err)
	}
	ins := func(source, host string, through *time.Time, pushed time.Time) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO external_usage_hosts (org_id, source, host, reported_through,
			first_reported_at, pushed_at) VALUES ($1, $2, $3, $4, $4, $5)`, org, source, host, through, pushed); err != nil {
			t.Fatal(err)
		}
	}
	ahead := pollNow.Add(5 * time.Minute)
	uploader := pollNow.Add(-30 * time.Minute)
	ins(observability.MeterPollSource, observability.MeterPollHost, nil, pollNow.Add(-2*time.Hour))
	ins(observability.MeterPollSource+": ahead#0", observability.MeterPollHost, &ahead, pollNow.Add(-2*time.Hour))
	ins("operator: home", "laptop", &uploader, uploader)
	ins("meter polling", "laptop", &uploader, uploader)
	ins(observability.MeterPollSource+": legacy#0", "laptop", &uploader, uploader)
	ins(observability.MeterPollSource, "laptop", nil, uploader)
	ins(poll, "laptop", &uploader, uploader)

	type mark struct {
		through, first *time.Time
		pushed         time.Time
	}
	// By source and host.
	at := func(source, host string) string { return source + " | " + host }
	marks := func() map[string]mark {
		t.Helper()
		rows, err := s.pool.Query(ctx, `SELECT source, host, reported_through, first_reported_at, pushed_at
			FROM external_usage_hosts WHERE org_id = $1`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]mark{}
		for rows.Next() {
			var (
				src, host string
				m         mark
			)
			if err := rows.Scan(&src, &host, &m.through, &m.first, &m.pushed); err != nil {
				t.Fatal(err)
			}
			out[at(src, host)] = m
		}
		return out
	}
	before := marks()
	if err := s.obs.AdvanceMeterPollMarks(ctx, pollNow); err != nil {
		t.Fatal(err)
	}
	// An earlier now moves nothing back.
	if err := s.obs.AdvanceMeterPollMarks(ctx, pollNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	after := marks()
	is := func(p *time.Time, want time.Time) bool { return p != nil && p.Equal(want) }

	covey := observability.MeterPollHost
	if m := after[at(poll, covey)]; !is(m.through, pollNow) || !is(m.first, booked) || !m.pushed.Equal(booked) {
		t.Errorf("the seat's poll host: %+v, want through %v, first %v, pushed %v", m, pollNow, booked, booked)
	}
	if m := after[at(observability.MeterPollSource, covey)]; !is(m.through, pollNow) || !is(m.first, pollNow) ||
		!m.pushed.Equal(pollNow.Add(-2*time.Hour)) {
		t.Errorf("a poll host without a watermark: %+v, want through and first at %v", m, pollNow)
	}
	if m := after[at(observability.MeterPollSource+": ahead#0", covey)]; !is(m.through, ahead) {
		t.Errorf("a watermark ahead of now moved back: %+v", m)
	}
	same := func(a, b *time.Time) bool { return (a == nil) == (b == nil) && (a == nil || a.Equal(*b)) }
	for _, k := range []string{at("operator: home", "laptop"), at("meter polling", "laptop"),
		at(observability.MeterPollSource+": legacy#0", "laptop"), at(observability.MeterPollSource, "laptop"),
		at(poll, "laptop")} {
		a, b := after[k], before[k]
		if _, ok := after[k]; !ok {
			t.Fatalf("%s: no such host", k)
		}
		if !same(a.through, b.through) || !same(a.first, b.first) || !a.pushed.Equal(b.pushed) {
			t.Errorf("%s is not a poll host but moved: %+v -> %+v", k, b, a)
		}
	}
}

// An uploader cannot push into the poll's sources, nor into any name that,
// trimmed and in lower case, begins with "meter poll" — and nothing is
// stored; a name that holds the words elsewhere is an ordinary source.
// Deleting a poll source stays allowed.
func TestMeterPollSourceRefusedByEndpoint(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")

	reported := map[string]time.Time{"laptop": pollNow}
	for _, src := range []string{observability.MeterPollSource, observability.MeterPollSourceFor("claude_code_oauth_token", 0),
		"meter poll: x", " Meter Poll: x", "METER POLL", "meter polling"} {
		body := extUpload(src)
		body["reported_through"] = reported
		res := admin.expect(http.MethodPost, externalPath, body, http.StatusBadRequest)
		if msg, _ := res["error"].(string); !strings.HasPrefix(msg, "invalid external usage: ") || !strings.Contains(msg, "meter poll") {
			t.Errorf("%q: error %q", src, msg)
		}
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM external_usage_hosts WHERE org_id = $1`, s.orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d hosts stored by refused pushes", n)
	}
	body := extUpload("operator: meter poll")
	body["reported_through"] = reported
	admin.expect(http.MethodPost, externalPath, body, http.StatusOK)

	// covey's own booking goes through the store; the API may delete it.
	poll := "meter poll: x"
	if _, err := s.obs.UpsertExternalUsage(ctx, s.orgID, poll, observability.MeterLinkChange{}, nil,
		map[string]time.Time{observability.MeterPollHost: pollNow}, pollNow); err != nil {
		t.Fatal(err)
	}
	admin.expect(http.MethodDelete, externalPath+"?source="+url.QueryEscape(poll), nil, http.StatusOK)
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM external_usage_hosts WHERE org_id = $1 AND source = $2`,
		s.orgID, poll).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the deleted poll source still has %d hosts", n)
	}
}

// meterPollEventRunner stands in for the claude CLI with one call whose
// rate_limit_event reads the weekly window at pct, resetting at resets, and
// whose result line books one Haiku turn.
type meterPollEventRunner struct {
	mu     sync.Mutex
	pct    float64
	resets time.Time
	calls  int
}

func (r *meterPollEventRunner) Run(_ context.Context, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"poll-s2","model":"claude-haiku-5-5"}`,
		fmt.Sprintf(`{"type":"rate_limit_event","session_id":"poll-s2","rate_limit_info":{"status":"allowed","unifiedWindows":{`+
			`"seven_day":{"status":"allowed","resetsAt":%d,"utilization":%g}}}}`, r.resets.Unix(), r.pct/100),
		`{"type":"result","subtype":"success","is_error":false,"session_id":"poll-s2","modelUsage":{` +
			`"claude-haiku-5-5":{"inputTokens":40,"outputTokens":2,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.0008}}}`,
	}, "\n") + "\n"), nil
}

// #153: a polled reading continues the seat's in-band series. covey ran on
// the seat for a while (three in-band readings, the last three hours ago);
// then it went idle, and an outside session on a source linked to the seat
// spent $3 two hours ago. The poll reads the seat, and the split closes a
// step at the polled reading, over the span since the last in-band one, that
// holds the outside call.
//
// The split reads one reading source per seat, the one with the most
// readings (pickCoverage). Filed under a source of its own (usage_cmd, as
// the poll once did), the reading would lose to the three in-band ones on
// count: the series would end three hours ago, and the outside call would lie
// after its last reading, its real share pending.
func TestMeterPollReadingExtendsTheInbandSeries(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	const key = "claude_code_oauth_token"
	ord := s.seat(t, rt.ID, daemon.CredSubscription, key, meterPollLoopToken, "A")
	var slot int
	if err := s.pool.QueryRow(ctx, `SELECT secret_slot FROM runtime_credentials WHERE runtime_id=$1 AND ord=$2`,
		rt.ID, ord).Scan(&slot); err != nil {
		t.Fatal(err)
	}
	// To the microsecond, as Postgres keeps it.
	now := time.Now().UTC().Truncate(time.Microsecond)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": now.Add(-72 * time.Hour)}, http.StatusOK)

	// The in-band series, all in one week: 10, 11 and 12 % at five, four and
	// three hours ago.
	resets := now.Truncate(time.Hour).Add(72 * time.Hour)
	inband := []time.Time{now.Add(-5 * time.Hour), now.Add(-4 * time.Hour), now.Add(-3 * time.Hour)}
	for i, at := range inband {
		if _, err := s.pool.Exec(ctx, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key,
			secret_slot, credential_kind, source, window_kind, supported, week_percent, resets_at, status, stale,
			fetched_at, received_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'seven_day', true, $8, $9, 'allowed', false, $10, $10)`,
			s.orgID, rt.ID, ord, key, slot, daemon.CredSubscription, observability.SourceInband,
			10+float64(i), resets, at); err != nil {
			t.Fatal(err)
		}
	}

	// The idle stretch: one outside call of $3 (Opus output at $20 per
	// million) two hours ago, on a source linked to the seat.
	call := extRow("o1", now.Add(-2*time.Hour), "claude-opus-5-5", 3e6/20)
	call["input"], call["cache_read"], call["cache_write_1h"] = 0, 0, 0
	up := extUpload("operator: home", call)
	up["meter_credential"] = map[string]any{"secret_key": key, "secret_slot": slot}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	runner := &meterPollEventRunner{pct: 14, resets: resets}
	s.orch.MeterPoll = runner
	s.orch.PollMeters(ctx)
	if runner.calls != 1 {
		t.Fatalf("the runner was called %d times, want once", runner.calls)
	}
	var (
		polledAt time.Time
		source   string
	)
	if err := s.pool.QueryRow(ctx, `SELECT received_at, source FROM usage_readings
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND window_kind = 'seven_day' AND week_percent = 14`,
		s.orgID, key, slot).Scan(&polledAt, &source); err != nil {
		t.Fatalf("the polled reading was not filed: %v", err)
	}
	if source != observability.SourceInband {
		t.Errorf("the polled reading was filed as %q, want %q", source, observability.SourceInband)
	}

	// The laptop reports through the present, so the step at the polled
	// reading is reported by both linked sources (the poll's own watermark is
	// the call's).
	push := extUpload("operator: home")
	push["reported_through"] = map[string]any{"laptop": time.Now().UTC()}
	admin.expect(http.MethodPost, externalPath, push, http.StatusOK)

	m := cellsExternal(t, admin, "days=2&bucket=hour").Meter
	var last *observability.ExternalMeterStep
	for i := range m.Steps {
		if st := &m.Steps[i]; st.SecretKey == key && st.SecretSlot == slot && (last == nil || st.At.After(last.At)) {
			last = st
		}
	}
	if last == nil {
		t.Fatalf("no step of the seat: %+v", m.Steps)
	}
	if !last.At.Equal(polledAt) || !last.From.Equal(inband[2]) || math.Abs(last.Points-2) > 1e-9 {
		t.Fatalf("the seat's last step is %v..%v (%v points), want %v..%v (2 points): the polled reading did not close one",
			last.From, last.At, last.Points, inband[2], polledAt)
	}
	if !last.Reported || !last.Split || last.ExternalListUSD < 3 || last.CoveyListUSD != 0 ||
		last.ExternalRealUSD == nil || *last.ExternalRealUSD <= 0 {
		t.Errorf("the polled step = %+v, want it reported and split, the outside call's $3 in its L_out", *last)
	}
	if m.UnmeteredListUSD != 0 {
		t.Errorf("unmetered outside list %v, want 0: the outside call lies in the polled step", m.UnmeteredListUSD)
	}
}
