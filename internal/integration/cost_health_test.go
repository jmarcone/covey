package integration

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// Cost health (#178): the checks C1–C8 as read-only queries, each with a
// fixture that passes and one that does not, all against a fixed instant —
// never the wall clock.

// healthNow is the fixture clock every check in this file runs at.
var healthNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// healthFixture writes the rows the checks read, relative to healthNow.
type healthFixture struct {
	s     *stack
	ctx   context.Context
	agent uuid.UUID
	rt    uuid.UUID
	event int64
}

func newHealthFixture(t *testing.T) *healthFixture {
	t.Helper()
	s := newStack(t)
	f := &healthFixture{s: s, ctx: context.Background(), agent: s.newSupportAgent("kostenwaechter").ID}
	rt, err := s.runtimes.Create(f.ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	f.rt = rt.ID
	return f
}

func (f *healthFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.s.pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// report runs the checks at at.
func (f *healthFixture) report(t *testing.T, at time.Time) observability.HealthReport {
	t.Helper()
	rep, err := f.s.obs.CostHealth(f.ctx, f.s.orgID, at)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// check is one check of a report run at healthNow.
func (f *healthFixture) check(t *testing.T, id string) observability.HealthCheck {
	t.Helper()
	for _, c := range f.report(t, healthNow).Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return observability.HealthCheck{}
}

func expectCheck(t *testing.T, c observability.HealthCheck, status, reason string) {
	t.Helper()
	if c.Status != status || c.Reason != reason {
		t.Fatalf("%s: %s/%s, want %s/%s (facts %+v)", c.ID, c.Status, c.Reason, status, reason, c.Facts)
	}
}

func (f *healthFixture) host(t *testing.T, source, host string, pushed time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO external_usage_hosts (org_id, source, host, pushed_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (org_id, source, host) DO UPDATE SET pushed_at = EXCLUDED.pushed_at`, f.s.orgID, source, host, pushed)
}

func (f *healthFixture) outsideCall(t *testing.T, source, message, model string, ts time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO external_usage (org_id, source, host, session_id, message_id, ts, model,
		input, output, cache_read, cache_write_5m, cache_write_1h)
		VALUES ($1,$2,'laptop','sitzung-1',$3,$4,$5,100,10,0,0,0)`, f.s.orgID, source, message, ts, model)
}

// line files one agent line paid by the credential at ord.
func (f *healthFixture) line(t *testing.T, model string, ord int, key, kind, lineUUID string, at time.Time) {
	t.Helper()
	f.event++
	var lu any
	if lineUUID != "" {
		lu = lineUUID
	}
	f.exec(t, `INSERT INTO model_usage (org_id, agent_id, event_id, model, input_tokens, output_tokens, created_at,
		line_uuid, runtime_id, credential_ord, secret_key, secret_slot, credential_kind)
		VALUES ($1,$2,$3,$4,1000,100,$5,$6,$7,$8,$9,0,$10)`,
		f.s.orgID, f.agent, f.event, model, at, lu, f.rt, ord, key, kind)
}

// reading files a weekly meter reading of the seat at ord.
func (f *healthFixture) reading(t *testing.T, ord int, key string, pct float64, resets *time.Time, at time.Time, supported, stale bool) {
	t.Helper()
	var p any
	if supported {
		p = pct
	}
	f.exec(t, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
		source, window_kind, supported, week_percent, resets_at, stale, fetched_at, received_at)
		VALUES ($1,$2,$3,$4,0,'subscription','inband','seven_day',$5,$6,$7,$8,$9,$9)`,
		f.s.orgID, f.rt, ord, key, supported, p, resets, stale, at)
}

// A fresh organisation, with nothing to check, is green: no uploader, no
// meter, no model, no bill — none of it is a fault.
func TestCostHealthEmptyOrganisationIsGreen(t *testing.T) {
	f := newHealthFixture(t)
	rep := f.report(t, healthNow)
	if rep.Status != observability.HealthPass || rep.Worst() != nil {
		t.Fatalf("an empty organisation is %s: %+v", rep.Status, rep.Checks)
	}
	want := map[string]string{"c1": "no_uploader", "c2": "no_meter", "c3": "monotonic", "c4": "unique",
		"c5": "no_models", "c6": "set", "c7": "not_linked", "c8": "pre_meter"}
	if len(rep.Checks) != len(want) {
		t.Fatalf("%d checks, want %d", len(rep.Checks), len(want))
	}
	for _, c := range rep.Checks {
		if c.Reason != want[c.ID] || !c.CheckedAt.Equal(healthNow) {
			t.Errorf("%s: %s at %v, want %s at %v", c.ID, c.Reason, c.CheckedAt, want[c.ID], healthNow)
		}
	}
	if c := rep.Checks[7]; c.Status != observability.HealthInfo || c.Facts.Calls == nil || *c.Facts.Calls != 0 {
		t.Errorf("c8 is %s with %v calls, want info with 0", c.Status, c.Facts.Calls)
	}
}

// C1: a host that pushed in the last 7 days and not within 75 minutes has a
// stopped uploader; one silent for longer than 7 days is a machine no longer
// in use.
func TestCostHealthUploaderFresh(t *testing.T) {
	f := newHealthFixture(t)
	f.host(t, "operator", "laptop", healthNow.Add(-30*time.Minute))
	f.host(t, "operator", "alt", healthNow.Add(-8*24*time.Hour))
	c := f.check(t, "c1")
	expectCheck(t, c, observability.HealthPass, "fresh")
	if c.Facts.LastPushAgo == nil || *c.Facts.LastPushAgo != 30 {
		t.Errorf("last push %v minutes ago, want 30", c.Facts.LastPushAgo)
	}

	f.host(t, "projekte", "werkbank", healthNow.Add(-76*time.Minute))
	c = f.check(t, "c1")
	expectCheck(t, c, observability.HealthFail, "stale")
	if len(c.Facts.StaleHosts) != 1 || c.Facts.StaleHosts[0].Host != "werkbank" || c.Facts.StaleHosts[0].Minutes != 76 {
		t.Errorf("stale hosts %+v, want werkbank at 76 minutes", c.Facts.StaleHosts)
	}
}

// The badge turns red within one hour of an uploader stopping: a host that
// pushes every 15 minutes and then stops is past 75 minutes an hour after its
// last scheduled push — over the API, with the five-minute cache in between.
func TestCostHealthBadgeTurnsRedWhenAnUploaderStops(t *testing.T) {
	f := newHealthFixture(t)
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	clock := healthNow
	f.s.srv.CostHealthNow = func() time.Time { return clock }
	health := func() observability.HealthReport {
		t.Helper()
		var rep observability.HealthReport
		if err := json.Unmarshal(rawGet(t, admin, "/api/v1/cost/health"), &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}

	// The last push lies 60 minutes back: still fresh.
	f.host(t, "operator", "laptop", healthNow.Add(-60*time.Minute))
	if rep := health(); rep.Status != observability.HealthPass {
		t.Fatalf("at 60 minutes the badge is %s, want pass", rep.Status)
	}
	// Inside the five minutes the report is the cached one.
	clock = healthNow.Add(4 * time.Minute)
	f.host(t, "operator", "laptop", healthNow.Add(-2*time.Hour))
	if rep := health(); rep.Status != observability.HealthPass || !rep.CheckedAt.Equal(healthNow) {
		t.Fatalf("inside the cache the badge is %s checked at %v, want the pass of %v", rep.Status, rep.CheckedAt, healthNow)
	}
	f.host(t, "operator", "laptop", healthNow.Add(-60*time.Minute))
	// Sixteen minutes on, the host is 76 minutes silent: red, and the reason
	// names it.
	clock = healthNow.Add(16 * time.Minute)
	rep := health()
	w := rep.Worst()
	if rep.Status != observability.HealthFail || w == nil || w.ID != "c1" || w.Reason != "stale" {
		t.Fatalf("at 76 minutes the badge is %s with %+v, want fail by c1 stale", rep.Status, w)
	}
	if len(w.Facts.StaleHosts) != 1 || w.Facts.StaleHosts[0].Minutes != 76 {
		t.Errorf("stale hosts %+v, want laptop at 76 minutes", w.Facts.StaleHosts)
	}
}

// C2: no reading while a seat exists fails; a reading older than two hours
// warns and says whether the fleet is paused; a row without a figure is not
// a reading.
func TestCostHealthMeterFresh(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	expectCheck(t, f.check(t, "c2"), observability.HealthFail, "none")

	f.reading(t, ord, "claude_code_oauth_token", 40, nil, healthNow.Add(-3*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 0, nil, healthNow.Add(-10*time.Minute), false, false)
	f.exec(t, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, f.s.orgID)
	c := f.check(t, "c2")
	expectCheck(t, c, observability.HealthWarn, "stale")
	if *c.Facts.ReadingAgo != 180 || *c.Facts.WeekPercent != 40 || !*c.Facts.FleetKilled {
		t.Errorf("facts %+v, want 180 minutes, 40 %%, fleet paused", c.Facts)
	}

	f.reading(t, ord, "claude_code_oauth_token", 42, nil, healthNow.Add(-30*time.Minute), true, false)
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "fresh")
}

// C3: inside one reset period the week percent never drops by more than a
// lagging point; a new reset time starts a new period.
func TestCostHealthMeterMonotonic(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	week1 := healthNow.Add(2 * 24 * time.Hour)
	week0 := healthNow.Add(-5 * 24 * time.Hour)
	f.reading(t, ord, "claude_code_oauth_token", 80, &week0, healthNow.Add(-6*24*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 3, &week1, healthNow.Add(-4*24*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 45, &week1, healthNow.Add(-3*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 44.5, &week1, healthNow.Add(-2*time.Hour), true, false)
	expectCheck(t, f.check(t, "c3"), observability.HealthPass, "monotonic")

	at := healthNow.Add(-time.Hour)
	f.reading(t, ord, "claude_code_oauth_token", 12, &week1, at, true, false)
	c := f.check(t, "c3")
	expectCheck(t, c, observability.HealthFail, "drops")
	if *c.Facts.Drops != 1 || !c.Facts.LastDropAt.Equal(at) {
		t.Errorf("drops %d at %v, want 1 at %v", *c.Facts.Drops, c.Facts.LastDropAt, at)
	}
}

// C4: one row per call. The schema holds both keys today, so the failing
// fixture first takes the constraint away, as an install that lost it would
// have (this test's own database; dropped for this test alone).
func TestCostHealthNoDoubleBooking(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	// One result line split per model: line_uuid repeats legitimately.
	f.line(t, "claude-opus-5-5", ord, "claude_code_oauth_token", "subscription", "zeile-1", healthNow.Add(-time.Hour))
	f.line(t, "claude-haiku-5-5", ord, "claude_code_oauth_token", "subscription", "zeile-1", healthNow.Add(-time.Hour))
	f.outsideCall(t, "operator", "msg-1", "claude-opus-5-5", healthNow.Add(-time.Hour))
	expectCheck(t, f.check(t, "c4"), observability.HealthPass, "unique")

	f.exec(t, `DROP INDEX idx_model_usage_line`)
	f.line(t, "claude-opus-5-5", ord, "claude_code_oauth_token", "subscription", "zeile-1", healthNow.Add(-time.Hour))
	c := f.check(t, "c4")
	expectCheck(t, c, observability.HealthFail, "duplicates")
	if *c.Facts.AgentDuplicates != 1 || *c.Facts.ExternalDuplicates != 0 {
		t.Errorf("duplicates %d agent / %d outside, want 1 / 0", *c.Facts.AgentDuplicates, *c.Facts.ExternalDuplicates)
	}

	f.exec(t, `ALTER TABLE external_usage DROP CONSTRAINT external_usage_pkey`)
	f.outsideCall(t, "operator", "msg-1", "claude-opus-5-5", healthNow.Add(-time.Hour))
	c = f.check(t, "c4")
	if *c.Facts.ExternalDuplicates != 1 {
		t.Errorf("outside duplicates %d, want 1", *c.Facts.ExternalDuplicates)
	}
}

// C5: every model used in 30 days, inside covey or outside, has a list
// price; Claude Code's own <synthetic> lines are not a model.
func TestCostHealthModelsPriced(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.line(t, "claude-opus-5-5[1m]", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-time.Hour))
	f.line(t, "<synthetic>", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-time.Hour))
	f.outsideCall(t, "operator", "alt", "claude-unbepreist-1", healthNow.Add(-31*24*time.Hour))
	c := f.check(t, "c5")
	expectCheck(t, c, observability.HealthPass, "priced")
	if *c.Facts.Models != 1 {
		t.Errorf("%d models, want 1", *c.Facts.Models)
	}

	f.outsideCall(t, "operator", "neu", "claude-unbepreist-2", healthNow.Add(-time.Hour))
	c = f.check(t, "c5")
	expectCheck(t, c, observability.HealthFail, "unpriced")
	if !slices.Equal(c.Facts.Unpriced, []string{"claude-unbepreist-2"}) {
		t.Errorf("unpriced %v, want [claude-unbepreist-2]", c.Facts.Unpriced)
	}
}

// C6: a subscription credential that spent in 7 days without a basis or a
// plan in force warns; an API key does not need either.
func TestCostHealthCostBasisSet(t *testing.T) {
	f := newHealthFixture(t)
	seat := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	key := f.s.seat(t, f.rt, daemon.CredAPIKey, "anthropic_api_key", "sk-test", "API")
	f.line(t, "claude-opus-5-5", key, "anthropic_api_key", "api_key", "", healthNow.Add(-time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")

	// The kind as the line was filed; empty, the credential's as it stands.
	f.line(t, "claude-opus-5-5", seat, "claude_code_oauth_token", "", "", healthNow.Add(-time.Hour))
	c := f.check(t, "c6")
	expectCheck(t, c, observability.HealthWarn, "missing")
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0"}) {
		t.Errorf("credentials %v, want [claude_code_oauth_token #0]", c.Facts.Credentials)
	}

	// A plan that ended before now does not count; one in force does —
	// matched by the secret value, whatever place it was entered at.
	f.exec(t, `INSERT INTO credential_plans (org_id, secret_key, secret_slot, amount, period, valid_from, valid_to)
		VALUES ($1,'claude_code_oauth_token',0,200,'month',$2,$3)`, f.s.orgID, healthNow.Add(-30*24*time.Hour), healthNow.Add(-24*time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthWarn, "missing")
	f.exec(t, `INSERT INTO credential_cost_bases (org_id, secret_key, secret_slot, basis, valid_from)
		VALUES ($1,'claude_code_oauth_token',0,'usage',$2)`, f.s.orgID, healthNow.Add(-24*time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")
}

// C7: the bill sync warns when its last attempt failed, and has nothing to
// say where no key is linked.
func TestCostHealthBillSync(t *testing.T) {
	f := newHealthFixture(t)
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "not_linked")
	if err := f.s.obs.NoteBillingSync(f.ctx, f.s.orgID, observability.BillingSyncAttempt{
		At: healthNow.Add(-2 * time.Hour), Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "ok")
	if err := f.s.obs.NoteBillingSync(f.ctx, f.s.orgID, observability.BillingSyncAttempt{
		At: healthNow.Add(-time.Hour), Status: "failed", Error: "analytics: 401"}); err != nil {
		t.Fatal(err)
	}
	c := f.check(t, "c7")
	expectCheck(t, c, observability.HealthWarn, "failed")
	if c.Facts.LastError != "analytics: 401" || !c.Facts.LastSuccessAt.Equal(healthNow.Add(-2*time.Hour)) {
		t.Errorf("facts %+v, want the error and the success two hours back", c.Facts)
	}
}

// C8: outside calls before the first weekly reading are counted, for
// information; with no reading at all, every outside call is before it.
func TestCostHealthPreMeterUsage(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	first := healthNow.Add(-24 * time.Hour)
	f.outsideCall(t, "operator", "a", "claude-opus-5-5", first.Add(-2*time.Hour))
	f.outsideCall(t, "operator", "b", "claude-opus-5-5", first.Add(-time.Hour))
	f.outsideCall(t, "operator", "c", "claude-opus-5-5", first.Add(time.Hour))
	if c := f.check(t, "c8"); *c.Facts.Calls != 3 {
		t.Errorf("without a reading %d calls before it, want 3", *c.Facts.Calls)
	}
	f.reading(t, ord, "claude_code_oauth_token", 0, nil, first.Add(-3*time.Hour), false, false)
	f.reading(t, ord, "claude_code_oauth_token", 5, nil, first, true, false)
	c := f.check(t, "c8")
	expectCheck(t, c, observability.HealthInfo, "pre_meter")
	if *c.Facts.Calls != 2 {
		t.Errorf("%d calls before the first reading, want 2", *c.Facts.Calls)
	}
}
