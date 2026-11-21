package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	identbuiltin "covey/internal/identity/builtin"
	"covey/internal/observability"
	"covey/internal/secrets"
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
	// in is what the handler would pass: whether an admin key is stored.
	in observability.HealthInputs
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
	rep, err := f.s.obs.CostHealth(f.ctx, f.s.orgID, at, f.in)
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

// host records a machine's latest push; watermark: its uploader sends one
// (reported_through), so it pushes on every run, idle or not.
func (f *healthFixture) host(t *testing.T, source, host string, pushed time.Time, watermark bool) {
	t.Helper()
	var through any
	if watermark {
		through = pushed
	}
	f.exec(t, `INSERT INTO external_usage_hosts (org_id, source, host, pushed_at, reported_through, first_reported_at)
		VALUES ($1,$2,$3,$4,$5,$5)
		ON CONFLICT (org_id, source, host) DO UPDATE SET pushed_at = EXCLUDED.pushed_at,
			reported_through = EXCLUDED.reported_through, first_reported_at = EXCLUDED.first_reported_at`,
		f.s.orgID, source, host, pushed, through)
}

// link names the seat a source shares its meter with.
func (f *healthFixture) link(t *testing.T, source, key string) {
	t.Helper()
	f.exec(t, `INSERT INTO external_usage_meters (org_id, source, secret_key, secret_slot) VALUES ($1,$2,$3,0)
		ON CONFLICT (org_id, source) DO UPDATE SET secret_key = EXCLUDED.secret_key`, f.s.orgID, source, key)
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
	var lu, k, slot any
	if lineUUID != "" {
		lu = lineUUID
	}
	if key != "" { // "": a line that names no credential
		k, slot = key, 0
	}
	f.exec(t, `INSERT INTO model_usage (org_id, agent_id, event_id, model, input_tokens, output_tokens, created_at,
		line_uuid, runtime_id, credential_ord, secret_key, secret_slot, credential_kind)
		VALUES ($1,$2,$3,$4,1000,100,$5,$6,$7,$8,$9,$10,$11)`,
		f.s.orgID, f.agent, f.event, model, at, lu, f.rt, ord, k, slot, kind)
}

// reading files a weekly meter reading of the seat at ord, read and received
// at the same instant, in band.
func (f *healthFixture) reading(t *testing.T, ord int, key string, pct float64, resets *time.Time, at time.Time, supported, stale bool) {
	t.Helper()
	f.readingFrom(t, ord, key, "inband", pct, resets, at, at, supported, stale)
}

// readingFrom files a reading of a source, read at `at` and received at
// `received` (a reading entered by hand arrives later than it was read).
func (f *healthFixture) readingFrom(t *testing.T, ord int, key, source string, pct float64, resets *time.Time, at, received time.Time, supported, stale bool) {
	t.Helper()
	var p any
	if supported {
		p = pct
	}
	f.exec(t, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
		source, window_kind, supported, week_percent, resets_at, stale, fetched_at, received_at)
		VALUES ($1,$2,$3,$4,0,'subscription',$5,'seven_day',$6,$7,$8,$9,$10,$11)`,
		f.s.orgID, f.rt, ord, key, source, supported, p, resets, stale, at, received)
}

// basis enters a cost basis of the credential, in force from `from`.
func (f *healthFixture) basis(t *testing.T, key, basis string, from time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO credential_cost_bases (org_id, secret_key, secret_slot, basis, valid_from)
		VALUES ($1,$2,0,$3,$4)`, f.s.orgID, key, basis, from)
}

// plan enters a plan of the credential, in force from `from`.
func (f *healthFixture) plan(t *testing.T, key string, from time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO credential_plans (org_id, secret_key, secret_slot, amount, period, valid_from)
		VALUES ($1,$2,0,200,'month',$3)`, f.s.orgID, key, from)
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

// C1: a host that sends a watermark, pushed in the last 7 days and not
// within 75 minutes has a stopped uploader; one silent for longer than 7
// days is a machine no longer in use.
func TestCostHealthUploaderFresh(t *testing.T) {
	f := newHealthFixture(t)
	f.host(t, "operator", "laptop", healthNow.Add(-30*time.Minute), true)
	f.host(t, "operator", "alt", healthNow.Add(-8*24*time.Hour), true)
	c := f.check(t, "c1")
	expectCheck(t, c, observability.HealthPass, "fresh")
	if c.Facts.LastPushAgo == nil || *c.Facts.LastPushAgo != 30 {
		t.Errorf("last push %v minutes ago, want 30", c.Facts.LastPushAgo)
	}

	f.host(t, "projekte", "werkbank", healthNow.Add(-76*time.Minute), true)
	c = f.check(t, "c1")
	expectCheck(t, c, observability.HealthFail, "stale")
	if len(c.Facts.StaleHosts) != 1 || c.Facts.StaleHosts[0].Host != "werkbank" || c.Facts.StaleHosts[0].Minutes != 76 {
		t.Errorf("stale hosts %+v, want werkbank at 76 minutes", c.Facts.StaleHosts)
	}
}

// C1 (review round 1, finding 6): a host without a watermark sends nothing
// while idle, so its silence is not a stopped uploader — the check names it
// for information and the badge stays as it is.
func TestCostHealthUploaderWithoutWatermarkIsOnlyQuiet(t *testing.T) {
	f := newHealthFixture(t)
	f.host(t, "operator", "laptop", healthNow.Add(-30*time.Minute), true)
	f.host(t, "operator", "notebook", healthNow.Add(-3*time.Hour), false)
	rep := f.report(t, healthNow)
	c := rep.Checks[0]
	expectCheck(t, c, observability.HealthInfo, "quiet")
	if len(c.Facts.StaleHosts) != 0 || len(c.Facts.QuietHosts) != 1 || c.Facts.QuietHosts[0].Host != "notebook" || c.Facts.QuietHosts[0].Minutes != 180 {
		t.Errorf("stale %+v, quiet %+v; want only notebook quiet at 180 minutes", c.Facts.StaleHosts, c.Facts.QuietHosts)
	}
	if rep.Status != observability.HealthPass {
		t.Errorf("a quiet host makes the badge %s, want pass", rep.Status)
	}
}

// C1 (outside review, 5): when every host has been silent for more than 7
// days, nothing outside is counted any more — a warning, not "fresh".
func TestCostHealthEveryUploaderSilentForAWeekWarns(t *testing.T) {
	f := newHealthFixture(t)
	f.host(t, "operator", "laptop", healthNow.Add(-8*24*time.Hour), true)
	f.host(t, "operator", "alt", healthNow.Add(-9*24*time.Hour), false)
	c := f.check(t, "c1")
	expectCheck(t, c, observability.HealthWarn, "all_silent")
	if *c.Facts.LastPushAgo != 8*24*60 {
		t.Errorf("last push %d minutes ago, want %d", *c.Facts.LastPushAgo, 8*24*60)
	}
	f.host(t, "operator", "laptop", healthNow.Add(-10*time.Minute), true)
	expectCheck(t, f.check(t, "c1"), observability.HealthPass, "fresh")

	// Review round 2, finding 19: once the last push is older than the
	// longest range the cost page shows (90 days), no figure on it holds an
	// outside call any more: the warning ends on its own.
	f.host(t, "operator", "laptop", healthNow.Add(-90*24*time.Hour), true)
	f.host(t, "operator", "alt", healthNow.Add(-91*24*time.Hour), false)
	expectCheck(t, f.check(t, "c1"), observability.HealthWarn, "all_silent")
	f.host(t, "operator", "laptop", healthNow.Add(-90*24*time.Hour-time.Minute), true)
	c = f.check(t, "c1")
	expectCheck(t, c, observability.HealthPass, "gone")
	if *c.Facts.LastPushAgo != 90*24*60+1 {
		t.Errorf("last push %d minutes ago, want %d", *c.Facts.LastPushAgo, 90*24*60+1)
	}
}

// The badge turns red once an uploader that sends a watermark has been
// silent for 75 minutes: a host that pushes every 15 minutes and then stops
// is past it an hour after its last scheduled push — over the API, with the
// five-minute cache in between.
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
	f.host(t, "operator", "laptop", healthNow.Add(-60*time.Minute), true)
	if rep := health(); rep.Status != observability.HealthPass {
		t.Fatalf("at 60 minutes the badge is %s, want pass", rep.Status)
	}
	// Inside the five minutes the report is the cached one.
	clock = healthNow.Add(4 * time.Minute)
	f.host(t, "operator", "laptop", healthNow.Add(-2*time.Hour), true)
	if rep := health(); rep.Status != observability.HealthPass || !rep.CheckedAt.Equal(healthNow) {
		t.Fatalf("inside the cache the badge is %s checked at %v, want the pass of %v", rep.Status, rep.CheckedAt, healthNow)
	}
	f.host(t, "operator", "laptop", healthNow.Add(-60*time.Minute), true)
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

// A check that cannot run answers 500, and the failure is not kept: the
// next request runs the checks again (#178: never a green badge from a
// fault, and never a fault served from the cache).
func TestCostHealthFailureIsNotCached(t *testing.T) {
	f := newHealthFixture(t)
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	f.s.srv.CostHealthNow = func() time.Time { return healthNow }
	f.exec(t, `ALTER TABLE external_usage_hosts RENAME TO external_usage_hosts_weg`)
	admin.expect(http.MethodGet, "/api/v1/cost/health", nil, http.StatusInternalServerError)
	f.exec(t, `ALTER TABLE external_usage_hosts_weg RENAME TO external_usage_hosts`)
	var rep observability.HealthReport
	if err := json.Unmarshal(rawGet(t, admin, "/api/v1/cost/health"), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Status != observability.HealthPass || !rep.CheckedAt.Equal(healthNow) {
		t.Fatalf("after the fault the report is %s at %v, want a fresh pass at %v", rep.Status, rep.CheckedAt, healthNow)
	}
}

// panickingKeys is a secret store whose Keys panics: the checks die
// halfway, as a bug in one of them would.
type panickingKeys struct{ secrets.Store }

func (panickingKeys) Keys(context.Context, uuid.UUID) ([]string, error) {
	panic("cost health test: keys")
}

// Review round 2, finding 18: a panic inside the checks answers 500 and ends
// the computation — the next request runs them again rather than wait on a
// run that never finishes.
func TestCostHealthPanicEndsTheRun(t *testing.T) {
	f := newHealthFixture(t)
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	admin.http.Timeout = 10 * time.Second
	f.s.srv.CostHealthNow = func() time.Time { return healthNow }
	store := f.s.srv.Secrets
	f.s.srv.Secrets = panickingKeys{store}
	admin.expect(http.MethodGet, "/api/v1/cost/health", nil, http.StatusInternalServerError)
	f.s.srv.Secrets = store
	var rep observability.HealthReport
	if err := json.Unmarshal(rawGet(t, admin, "/api/v1/cost/health"), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Status != observability.HealthPass || !rep.CheckedAt.Equal(healthNow) {
		t.Fatalf("after the panic the report is %s at %v, want a fresh pass at %v", rep.Status, rep.CheckedAt, healthNow)
	}
}

// One organisation never sees another's rows or its cached report.
func TestCostHealthIsPerOrganisation(t *testing.T) {
	f := newHealthFixture(t)
	adminA := login(t, f.s, "admin@test.local", "admin-passwort")
	f.s.srv.CostHealthNow = func() time.Time { return healthNow }
	f.host(t, "operator", "werkbank", healthNow.Add(-76*time.Minute), true)
	if err := f.s.secrets.Put(f.ctx, f.s.orgID, "anthropic_admin_key", "sk-ant-admin01-test"); err != nil {
		t.Fatal(err)
	}
	get := func(c *apiClient) observability.HealthReport {
		t.Helper()
		var rep observability.HealthReport
		if err := json.Unmarshal(rawGet(t, c, "/api/v1/cost/health"), &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	repA := get(adminA)
	if repA.Status != observability.HealthFail || repA.Checks[0].Reason != "stale" || repA.Checks[6].Reason != "not_run" {
		t.Fatalf("organisation A: %s, c1 %s, c7 %s; want fail, stale, not_run", repA.Status, repA.Checks[0].Reason, repA.Checks[6].Reason)
	}

	orgB := uuid.New()
	f.exec(t, `INSERT INTO organizations (id, name) VALUES ($1,'Other Ltd')`, orgB)
	hash, _ := identbuiltin.HashPassword("b-passwort")
	accB := uuid.New()
	f.exec(t, `INSERT INTO accounts (id, email, password_hash, display_name, email_verified_at)
		VALUES ($1,'b@other.example',$2,'B',now())`, accB, hash)
	f.exec(t, `INSERT INTO humans (id, org_id, account_id, email, display_name, password_hash, role)
		VALUES ($1,$2,$3,'b@other.example','B',$4,'org_admin')`, uuid.New(), orgB, accB, hash)
	repB := get(login(t, f.s, "b@other.example", "b-passwort"))
	if repB.Status != observability.HealthPass || repB.Checks[0].Reason != "no_uploader" || repB.Checks[6].Reason != "not_linked" {
		t.Fatalf("organisation B: %s, c1 %s, c7 %s; want pass, no_uploader, not_linked", repB.Status, repB.Checks[0].Reason, repB.Checks[6].Reason)
	}
	if repA = get(adminA); repA.Status != observability.HealthFail {
		t.Fatalf("organisation A after B: %s, want its own fail", repA.Status)
	}
}

// C2, per seat: a seat that spent and was never read fails; one that spent
// more than two hours past its latest reading warns and says whether the
// fleet is paused; a row without a figure is not a reading; a seat that did
// not spend needs none.
func TestCostHealthMeterFresh(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "no_meter")
	f.line(t, "claude-opus-5-5", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-10*time.Minute))
	c := f.check(t, "c2")
	expectCheck(t, c, observability.HealthFail, "none")
	if c.Facts.Credential != "claude_code_oauth_token #0" {
		t.Errorf("credential %q, want claude_code_oauth_token #0", c.Facts.Credential)
	}

	f.reading(t, ord, "claude_code_oauth_token", 40, nil, healthNow.Add(-3*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 0, nil, healthNow.Add(-10*time.Minute), false, false)
	f.exec(t, `UPDATE organizations SET fleet_killed = true WHERE id = $1`, f.s.orgID)
	c = f.check(t, "c2")
	expectCheck(t, c, observability.HealthWarn, "stale")
	if *c.Facts.ReadingAgo != 180 || *c.Facts.WeekPercent != 40 || !*c.Facts.FleetKilled || !c.Facts.SpentAt.Equal(healthNow.Add(-10*time.Minute)) {
		t.Errorf("facts %+v, want 180 minutes, 40 %%, fleet paused, spent 10 minutes ago", c.Facts)
	}

	f.reading(t, ord, "claude_code_oauth_token", 42, nil, healthNow.Add(-30*time.Minute), true, false)
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "fresh")
}

// C2 (review round 1, finding 1, probe A): one seat read ten minutes ago
// does not stand for another that spends and was never read; spending of an
// outside source counts for the seat it is linked to.
func TestCostHealthMeterFreshIsPerSeat(t *testing.T) {
	f := newHealthFixture(t)
	a := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz-a", "A")
	b := f.s.seat(t, f.rt, daemon.CredSubscription, "seat_b_token", "sitz-b", "B")
	f.line(t, "claude-opus-5-5", a, "claude_code_oauth_token", "subscription", "", healthNow.Add(-5*time.Minute))
	f.reading(t, a, "claude_code_oauth_token", 30, nil, healthNow.Add(-10*time.Minute), true, false)
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "fresh")

	f.line(t, "claude-opus-5-5", b, "seat_b_token", "subscription", "", healthNow.Add(-5*time.Minute))
	c := f.check(t, "c2")
	expectCheck(t, c, observability.HealthFail, "none")
	if c.Facts.Credential != "seat_b_token #0" {
		t.Errorf("credential %q, want seat_b_token #0", c.Facts.Credential)
	}

	// Seat B is read an hour before its last spending: fresh. Seat A's
	// reading moves 150 minutes back and its own lines go; an outside source
	// spends a minute ago, and counts for A only once it is linked to A.
	f.reading(t, b, "seat_b_token", 10, nil, healthNow.Add(-4*time.Hour), true, false)
	f.exec(t, `DELETE FROM model_usage WHERE secret_key = 'seat_b_token'`)
	f.line(t, "claude-opus-5-5", b, "seat_b_token", "subscription", "", healthNow.Add(-3*time.Hour))
	f.outsideCall(t, "operator", "m-1", "claude-opus-5-5", healthNow.Add(-time.Minute))
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "fresh")
	f.exec(t, `UPDATE usage_readings SET fetched_at = $2, received_at = $2 WHERE org_id = $1 AND secret_key = 'claude_code_oauth_token'`,
		f.s.orgID, healthNow.Add(-150*time.Minute))
	f.exec(t, `DELETE FROM model_usage WHERE secret_key = 'claude_code_oauth_token'`)
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "fresh")
	f.link(t, "operator", "claude_code_oauth_token")
	c = f.check(t, "c2")
	expectCheck(t, c, observability.HealthWarn, "stale")
	if c.Facts.Credential != "claude_code_oauth_token #0" || *c.Facts.ReadingAgo != 150 {
		t.Errorf("credential %q read %d minutes ago, want claude_code_oauth_token #0 at 150", c.Facts.Credential, *c.Facts.ReadingAgo)
	}
}

// C2 (review round 1, finding 1, probe B): a seat whose basis in force is
// usage has its money from the usage charges; its meter prices nothing, so
// no reading is missing.
func TestCostHealthMeterFreshSkipsAUsageBasis(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.line(t, "claude-opus-5-5", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-5*time.Minute))
	expectCheck(t, f.check(t, "c2"), observability.HealthFail, "none")
	f.basis(t, "claude_code_oauth_token", "usage", healthNow.Add(-24*time.Hour))
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "no_meter")
}

// C2 (review round 1, finding 2): a reading is as old as when it was read,
// not when it was typed in.
func TestCostHealthMeterFreshAgesByWhenRead(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.line(t, "claude-opus-5-5", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-5*time.Minute))
	f.readingFrom(t, ord, "claude_code_oauth_token", "manual", 30, nil, healthNow.Add(-3*time.Hour), healthNow.Add(-time.Minute), true, false)
	c := f.check(t, "c2")
	expectCheck(t, c, observability.HealthWarn, "stale")
	if *c.Facts.ReadingAgo != 180 {
		t.Errorf("reading %d minutes old, want 180", *c.Facts.ReadingAgo)
	}

	// Review round 2, finding 20: an in-band reading read 30 minutes ago and
	// received before the one typed in is the latest — by when it was read,
	// not when it arrived. (Two of them, so that the in-band series is the
	// one the page reads: readSources.)
	f.readingFrom(t, ord, "claude_code_oauth_token", "inband", 30, nil, healthNow.Add(-time.Hour), healthNow.Add(-time.Hour), true, false)
	f.readingFrom(t, ord, "claude_code_oauth_token", "inband", 31, nil, healthNow.Add(-30*time.Minute), healthNow.Add(-30*time.Minute), true, false)
	c = f.check(t, "c2")
	expectCheck(t, c, observability.HealthPass, "fresh")
	if *c.Facts.ReadingAgo != 30 || *c.Facts.WeekPercent != 31 {
		t.Errorf("reading %d minutes old at %v %%, want 30 at 31", *c.Facts.ReadingAgo, *c.Facts.WeekPercent)
	}
}

// The weekly meter is one timeline of every reader since #214 (merged): a
// reading from another source (the meter probe, entered through the API)
// beside an in-band series that stopped keeps the figure fresh, and so C2.
// That is the point: a paused fleet must not lose the meter.
func TestCostHealthMeterFreshReadsThePagesSource(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	for i := 0; i < 5; i++ { // in band, every half hour, up to 4 h ago
		at := healthNow.Add(-4*time.Hour - time.Duration(i)*30*time.Minute)
		f.reading(t, ord, "claude_code_oauth_token", float64(20-i), nil, at, true, false)
	}
	f.line(t, "claude-opus-5-5", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-10*time.Minute))
	expectCheck(t, f.check(t, "c2"), observability.HealthWarn, "stale")
	f.readingFrom(t, ord, "claude_code_oauth_token", "manual", 25, nil, healthNow.Add(-5*time.Minute), healthNow.Add(-5*time.Minute), true, false)
	c := f.check(t, "c2")
	expectCheck(t, c, observability.HealthPass, "fresh")
	if *c.Facts.ReadingAgo != 5 {
		t.Errorf("reading %d minutes old, want 5 (the merged meter's latest, from any source)", *c.Facts.ReadingAgo)
	}
}

// Outside lane review (finding 6): every seat that spent with no reading is
// named, so the page marks each one's figures, not only the first.
func TestCostHealthMeterFreshNamesEverySeatWithoutReading(t *testing.T) {
	f := newHealthFixture(t)
	a := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz-a", "A")
	b := f.s.seat(t, f.rt, daemon.CredSubscription, "seat_b_token", "sitz-b", "B")
	f.line(t, "claude-opus-5-5", a, "claude_code_oauth_token", "subscription", "", healthNow.Add(-5*time.Minute))
	f.line(t, "claude-opus-5-5", b, "seat_b_token", "subscription", "", healthNow.Add(-5*time.Minute))
	c := f.check(t, "c2")
	expectCheck(t, c, observability.HealthFail, "none")
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0", "seat_b_token #0"}) {
		t.Errorf("credentials %v, want both seats", c.Facts.Credentials)
	}
}

// Outside lane review (finding 1): Codex's ChatGPT plan is a subscription with
// no weekly meter and no Claude list price. Its lines are no seat for C2 or
// C6 and no model for C5; the same lines on a Claude Code runtime are.
func TestCostHealthOtherEnginesAreNoSeat(t *testing.T) {
	f := newHealthFixture(t)
	codex, err := f.s.runtimes.Create(f.ctx, f.s.orgID, "codex", "Codex", "")
	if err != nil {
		t.Fatal(err)
	}
	claude := f.rt
	f.rt = codex.ID
	ord := f.s.seat(t, codex.ID, daemon.CredSubscription, "codex_auth_json", "chatgpt", "ChatGPT plan")
	f.line(t, "gpt-5.5-codex", ord, "codex_auth_json", "subscription", "", healthNow.Add(-5*time.Minute))
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "no_meter")
	expectCheck(t, f.check(t, "c5"), observability.HealthPass, "no_models")
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")

	f.rt = claude
	ord = f.s.seat(t, claude, daemon.CredSubscription, "codex_auth_json", "chatgpt", "ChatGPT plan")
	f.line(t, "gpt-5.5-codex", ord, "codex_auth_json", "subscription", "", healthNow.Add(-5*time.Minute))
	expectCheck(t, f.check(t, "c2"), observability.HealthFail, "none")
	expectCheck(t, f.check(t, "c5"), observability.HealthFail, "unpriced")
	expectCheck(t, f.check(t, "c6"), observability.HealthWarn, "missing")
}

// Review round 2, finding 13: a seat that only a linked outside source spent
// on is a seat for C6 as it is for C2 — its outside part is priced by the
// seat's plan, so without one its real cost is unknown.
func TestCostHealthSeatSpentOnlyOutside(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.link(t, "operator", "claude_code_oauth_token")
	f.outsideCall(t, "operator", "a", "claude-opus-5-5", healthNow.Add(-time.Hour))
	f.reading(t, ord, "claude_code_oauth_token", 5, nil, healthNow.Add(-30*time.Minute), true, false)
	expectCheck(t, f.check(t, "c2"), observability.HealthPass, "fresh")
	c := f.check(t, "c6")
	expectCheck(t, c, observability.HealthWarn, "missing")
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0"}) {
		t.Errorf("credentials %v, want [claude_code_oauth_token #0]", c.Facts.Credentials)
	}
	f.plan(t, "claude_code_oauth_token", healthNow.Add(-30*24*time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")

	// An unlinked source is billed per use: it spends on no seat.
	g := newHealthFixture(t)
	ord = g.s.seat(t, g.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	g.outsideCall(t, "privat", "a", "claude-opus-5-5", healthNow.Add(-time.Hour))
	expectCheck(t, g.check(t, "c6"), observability.HealthPass, "set")
}

// C3: inside one week the week percent never drops by more than a lagging
// point; a new reset time starts a new week.
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
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0"}) {
		t.Errorf("credentials %v, want [claude_code_oauth_token #0]", c.Facts.Credentials)
	}
}

// C3 (review round 2, findings 14 and 15): one unrecorded reset is one drop,
// however many readings follow below the old top; the next drop counts once
// the figure was back at the top. The facts name the seats with a drop, and
// only those.
func TestCostHealthMeterDropIsOnePerRun(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	other := f.s.seat(t, f.rt, daemon.CredSubscription, "seat_b_token", "sitz-b", "B")
	resets := healthNow.Add(2 * 24 * time.Hour)
	for i, pct := range []float64{45, 2, 5, 9} {
		f.reading(t, ord, "claude_code_oauth_token", pct, &resets, healthNow.Add(time.Duration(i-5)*time.Hour), true, false)
	}
	f.reading(t, other, "seat_b_token", 10, &resets, healthNow.Add(-5*time.Hour), true, false)
	f.reading(t, other, "seat_b_token", 12, &resets, healthNow.Add(-4*time.Hour), true, false)
	c := f.check(t, "c3")
	expectCheck(t, c, observability.HealthFail, "drops")
	if *c.Facts.Drops != 1 || !c.Facts.LastDropAt.Equal(healthNow.Add(-4*time.Hour)) {
		t.Errorf("drops %d, the last at %v; want 1 at %v", *c.Facts.Drops, c.Facts.LastDropAt, healthNow.Add(-4*time.Hour))
	}
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0"}) {
		t.Errorf("credentials %v, want [claude_code_oauth_token #0]", c.Facts.Credentials)
	}

	// Back above the old top, then down again: a second drop. Seat B drops too.
	f.reading(t, ord, "claude_code_oauth_token", 50, &resets, healthNow.Add(-50*time.Minute), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 3, &resets, healthNow.Add(-40*time.Minute), true, false)
	f.reading(t, other, "seat_b_token", 1, &resets, healthNow.Add(-30*time.Minute), true, false)
	c = f.check(t, "c3")
	if *c.Facts.Drops != 3 || !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0", "seat_b_token #0"}) {
		t.Errorf("drops %d on %v, want 3 on both seats", *c.Facts.Drops, c.Facts.Credentials)
	}
}

// C3 (review round 1, finding 3): the check reads the meter's own rule. A
// reset time that jitters by a second is the same week (probe A: the drop
// counts), and two readers of one seat are one meter within a point (probe
// B: a reading one point below another reader's is lag, no drop; #214).
func TestCostHealthMeterDropFollowsTheMeter(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	resets := healthNow.Add(2 * 24 * time.Hour)
	jitter := resets.Add(time.Second)
	f.reading(t, ord, "claude_code_oauth_token", 45, &resets, healthNow.Add(-3*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 12, &jitter, healthNow.Add(-2*time.Hour), true, false)
	expectCheck(t, f.check(t, "c3"), observability.HealthFail, "drops")

	g := newHealthFixture(t)
	ord = g.s.seat(t, g.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	g.readingFrom(t, ord, "claude_code_oauth_token", "inband", 45, &resets, healthNow.Add(-3*time.Hour), healthNow.Add(-3*time.Hour), true, false)
	g.readingFrom(t, ord, "claude_code_oauth_token", "manual", 44, &resets, healthNow.Add(-2*time.Hour), healthNow.Add(-2*time.Hour), true, false)
	expectCheck(t, g.check(t, "c3"), observability.HealthPass, "monotonic")
}

// Since #214 the page reads one meter from every source: a reading typed in by
// hand at 45 % beside an in-band series at 30-33 % is a contradiction in that
// meter, and C3 says so. An in-band series alone stays monotonic until it falls.
func TestCostHealthMeterDropOnlyInThePagesSource(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	resets := healthNow.Add(2 * 24 * time.Hour)
	for i := 0; i < 4; i++ {
		f.reading(t, ord, "claude_code_oauth_token", float64(30+i), &resets, healthNow.Add(time.Duration(i-6)*time.Hour), true, false)
	}
	expectCheck(t, f.check(t, "c3"), observability.HealthPass, "monotonic")
	f.readingFrom(t, ord, "claude_code_oauth_token", "manual", 45, &resets, healthNow.Add(-5*time.Hour-30*time.Minute), healthNow, true, false)
	expectCheck(t, f.check(t, "c3"), observability.HealthFail, "drops")

	h := newHealthFixture(t)
	ord = h.s.seat(t, h.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	for i := 0; i < 4; i++ {
		h.reading(t, ord, "claude_code_oauth_token", float64(30+i), &resets, healthNow.Add(time.Duration(i-6)*time.Hour), true, false)
	}
	h.reading(t, ord, "claude_code_oauth_token", 10, &resets, healthNow.Add(-time.Hour), true, false)
	expectCheck(t, h.check(t, "c3"), observability.HealthFail, "drops")
}

// C3 (outside review, 1): an account reset in mid-week moves the reset time
// and drops the figure while the old week was still running — a reset
// nobody recorded. An ordinary weekly rollover, its reset time behind it, is
// not one.
func TestCostHealthMeterResetInMidWeek(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	week0 := healthNow.Add(-5 * 24 * time.Hour)
	week1 := healthNow.Add(2 * 24 * time.Hour)
	f.reading(t, ord, "claude_code_oauth_token", 80, &week0, healthNow.Add(-6*24*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 3, &week1, healthNow.Add(-4*24*time.Hour), true, false)
	f.reading(t, ord, "claude_code_oauth_token", 60, &week1, healthNow.Add(-3*time.Hour), true, false)
	expectCheck(t, f.check(t, "c3"), observability.HealthPass, "monotonic")

	moved := healthNow.Add(7 * 24 * time.Hour)
	at := healthNow.Add(-2 * time.Hour)
	f.reading(t, ord, "claude_code_oauth_token", 2, &moved, at, true, false)
	c := f.check(t, "c3")
	expectCheck(t, c, observability.HealthFail, "early_reset")
	if *c.Facts.EarlyResets != 1 || *c.Facts.Drops != 0 || !c.Facts.LastDropAt.Equal(at) {
		t.Errorf("early resets %d, drops %d at %v; want 1, 0 at %v", *c.Facts.EarlyResets, *c.Facts.Drops, c.Facts.LastDropAt, at)
	}
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0"}) {
		t.Errorf("credentials %v, want [claude_code_oauth_token #0]", c.Facts.Credentials)
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

// Outside lane review (finding 3): the uploader sends every transcript again,
// so a source renamed (--source, --label, --source-per-project) uploads the
// same calls under a second source. The primary key cannot stop that; C4
// does, with the schema whole.
func TestCostHealthSameCallUnderTwoSources(t *testing.T) {
	f := newHealthFixture(t)
	f.outsideCall(t, "operator", "msg-1", "claude-opus-5-5", healthNow.Add(-time.Hour))
	f.outsideCall(t, "operator", "msg-2", "claude-opus-5-5", healthNow.Add(-time.Hour))
	f.outsideCall(t, "projects-sylius", "msg-3", "claude-opus-5-5", healthNow.Add(-time.Hour))
	expectCheck(t, f.check(t, "c4"), observability.HealthPass, "unique")

	f.outsideCall(t, "covey operator", "msg-1", "claude-opus-5-5", healthNow.Add(-time.Hour))
	f.outsideCall(t, "covey operator", "msg-2", "claude-opus-5-5", healthNow.Add(-time.Hour))
	c := f.check(t, "c4")
	expectCheck(t, c, observability.HealthFail, "duplicates")
	if *c.Facts.ExternalDuplicates != 2 || *c.Facts.AgentDuplicates != 0 {
		t.Errorf("duplicates %d outside / %d agent, want 2 / 0", *c.Facts.ExternalDuplicates, *c.Facts.AgentDuplicates)
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
	// Review round 2, finding 17: which side used it, so that "covey only"
	// is not marked for a model only outside calls used.
	if len(c.Facts.UnpricedAgents) != 0 || !slices.Equal(c.Facts.UnpricedOutside, []string{"claude-unbepreist-2"}) {
		t.Errorf("unpriced by agents %v, outside %v; want none and [claude-unbepreist-2]", c.Facts.UnpricedAgents, c.Facts.UnpricedOutside)
	}
	f.line(t, "claude-unbepreist-2", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-time.Hour))
	f.line(t, "claude-unbepreist-3", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-time.Hour))
	c = f.check(t, "c5")
	if !slices.Equal(c.Facts.Unpriced, []string{"claude-unbepreist-2", "claude-unbepreist-3"}) || *c.Facts.Models != 3 ||
		!slices.Equal(c.Facts.UnpricedAgents, []string{"claude-unbepreist-2", "claude-unbepreist-3"}) ||
		!slices.Equal(c.Facts.UnpricedOutside, []string{"claude-unbepreist-2"}) {
		t.Errorf("unpriced %v of %d (agents %v, outside %v); want 2 and 3 of 3, both by agents, 2 outside",
			c.Facts.Unpriced, *c.Facts.Models, c.Facts.UnpricedAgents, c.Facts.UnpricedOutside)
	}
}

// C5 (outside review, nit): a line with tokens and no model name has no
// price either; one without tokens costs nothing.
func TestCostHealthModelsPricedNamesALineWithoutModel(t *testing.T) {
	f := newHealthFixture(t)
	f.exec(t, `INSERT INTO model_usage (org_id, agent_id, event_id, model, created_at) VALUES ($1,$2,1,'',$3)`,
		f.s.orgID, f.agent, healthNow.Add(-time.Hour))
	f.event = 1
	expectCheck(t, f.check(t, "c5"), observability.HealthPass, "no_models")
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.line(t, "", ord, "claude_code_oauth_token", "subscription", "", healthNow.Add(-time.Hour))
	c := f.check(t, "c5")
	expectCheck(t, c, observability.HealthFail, "unpriced")
	if !slices.Equal(c.Facts.Unpriced, []string{""}) {
		t.Errorf("unpriced %q, want the nameless one", c.Facts.Unpriced)
	}
}

// C6: a subscription credential that spent in 7 days without a plan in
// force (or a basis of usage) warns; an API key needs neither.
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
	f.basis(t, "claude_code_oauth_token", "usage", healthNow.Add(-24*time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")
}

// C6 (review round 1, finding 4): a basis of seat prices the meter by the
// plan, so without a plan the seat's real cost is still unknown.
func TestCostHealthSeatBasisNeedsAPlan(t *testing.T) {
	f := newHealthFixture(t)
	seat := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.line(t, "claude-opus-5-5", seat, "claude_code_oauth_token", "subscription", "", healthNow.Add(-time.Hour))
	f.basis(t, "claude_code_oauth_token", "seat", healthNow.Add(-24*time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthWarn, "missing")
	f.plan(t, "claude_code_oauth_token", healthNow.Add(-30*24*time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")
}

// C6 (outside review, 6): a line whose credential kind nobody knows any more
// is not taken for an API key, and a line that names no credential is
// counted as unattributed.
func TestCostHealthUnknownKindAndUnattributedSpend(t *testing.T) {
	f := newHealthFixture(t)
	key := f.s.seat(t, f.rt, daemon.CredAPIKey, "anthropic_api_key", "sk-test", "API")
	// ord 99: a credential that no longer exists, filed without a kind.
	f.line(t, "claude-opus-5-5", 99, "claude_code_oauth_token", "", "", healthNow.Add(-time.Hour))
	c := f.check(t, "c6")
	expectCheck(t, c, observability.HealthWarn, "missing")
	if !slices.Equal(c.Facts.Credentials, []string{"claude_code_oauth_token #0"}) {
		t.Errorf("credentials %v, want [claude_code_oauth_token #0]", c.Facts.Credentials)
	}

	f.plan(t, "claude_code_oauth_token", healthNow.Add(-30*24*time.Hour))
	f.line(t, "claude-opus-5-5", key, "", "api_key", "", healthNow.Add(-time.Hour))
	expectCheck(t, f.check(t, "c6"), observability.HealthPass, "set")
	f.line(t, "claude-opus-5-5", 99, "", "", "", healthNow.Add(-time.Hour))
	c = f.check(t, "c6")
	expectCheck(t, c, observability.HealthWarn, "unattributed")
	if c.Facts.Unattributed == nil || *c.Facts.Unattributed != 1 {
		t.Errorf("unattributed %v, want 1", c.Facts.Unattributed)
	}
}

// C7: the bill sync has nothing to say where no admin key is stored —
// whatever an earlier sync left behind — and warns when its last attempt
// failed.
func TestCostHealthBillSync(t *testing.T) {
	f := newHealthFixture(t)
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "not_linked")
	if err := f.s.obs.NoteBillingSync(f.ctx, f.s.orgID, observability.BillingSyncAttempt{
		At: healthNow.Add(-2 * time.Hour), Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.obs.NoteBillingSync(f.ctx, f.s.orgID, observability.BillingSyncAttempt{
		At: healthNow.Add(-time.Hour), Status: "failed", Error: "analytics: 401"}); err != nil {
		t.Fatal(err)
	}
	// The key was removed after the failed sync: the row stays, the sync is gone.
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "not_linked")

	f.in.AdminKey = true
	c := f.check(t, "c7")
	expectCheck(t, c, observability.HealthWarn, "failed")
	if c.Facts.LastError != "analytics: 401" || !c.Facts.LastSuccessAt.Equal(healthNow.Add(-2*time.Hour)) {
		t.Errorf("facts %+v, want the error and the success two hours back", c.Facts)
	}
	if err := f.s.obs.NoteBillingSync(f.ctx, f.s.orgID, observability.BillingSyncAttempt{
		At: healthNow.Add(-30 * time.Minute), Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "ok")
}

// C7 (review round 1, finding 5; outside review, 4): with a key stored, a
// sync that never ran, that stopped running for two of its hourly rounds,
// or that found no bill where covey's API keys spent a day before, warns.
func TestCostHealthBillSyncStoppedOrNotLanded(t *testing.T) {
	f := newHealthFixture(t)
	f.in.AdminKey = true
	expectCheck(t, f.check(t, "c7"), observability.HealthWarn, "not_run")
	// A row without an attempt has not run either (outside review round 2):
	// "stopped" would say "has not run for –".
	f.exec(t, `INSERT INTO billing_sync (org_id) VALUES ($1)`, f.s.orgID)
	expectCheck(t, f.check(t, "c7"), observability.HealthWarn, "not_run")

	if err := f.s.obs.NoteBillingSync(f.ctx, f.s.orgID, observability.BillingSyncAttempt{
		At: healthNow.Add(-10 * time.Minute), Status: "not_landed"}); err != nil {
		t.Fatal(err)
	}
	// Nothing spent at token prices: nothing to land. Neither did a source
	// billed per use (review round 2, finding 16): nothing says its machine
	// called with a key of the organisation whose bill is read.
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "ok")
	f.outsideCall(t, "privat", "x", "claude-opus-5-5", healthNow.Add(-30*time.Hour))
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "ok")
	// Nor did another engine's API key (outside review round 2): Codex's
	// openai_api_key is on OpenAI's bill, not on the one read here.
	codex, err := f.s.runtimes.Create(f.ctx, f.s.orgID, "codex", "Codex", "")
	if err != nil {
		t.Fatal(err)
	}
	claude := f.rt
	f.rt = codex.ID
	openai := f.s.seat(t, codex.ID, daemon.CredAPIKey, "openai_api_key", "sk-openai", "API")
	f.line(t, "gpt-5.5-codex", openai, "openai_api_key", "api_key", "", healthNow.Add(-30*time.Hour))
	f.rt = claude
	expectCheck(t, f.check(t, "c7"), observability.HealthPass, "ok")
	key := f.s.seat(t, f.rt, daemon.CredAPIKey, "anthropic_api_key", "sk-test", "API")
	f.line(t, "claude-opus-5-5", key, "anthropic_api_key", "api_key", "", healthNow.Add(-30*time.Hour))
	expectCheck(t, f.check(t, "c7"), observability.HealthWarn, "not_landed")

	// Two hours and a minute after the last attempt: the sync stopped.
	var stopped observability.HealthCheck
	for _, c := range f.report(t, healthNow.Add(111*time.Minute)).Checks {
		if c.ID == "c7" {
			stopped = c
		}
	}
	expectCheck(t, stopped, observability.HealthWarn, "stopped")
	if *stopped.Facts.LastAttemptAgo != 121 {
		t.Errorf("last attempt %d minutes ago, want 121", *stopped.Facts.LastAttemptAgo)
	}
}

// C8: calls of an outside source linked to a seat from before that seat's
// first weekly reading are counted, for information; with no reading at
// all, every linked call is before it. A source billed per use shares no
// meter, and another seat's reading says nothing about this one's.
func TestCostHealthPreMeterUsage(t *testing.T) {
	f := newHealthFixture(t)
	ord := f.s.seat(t, f.rt, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "Work")
	f.link(t, "operator", "claude_code_oauth_token")
	first := healthNow.Add(-24 * time.Hour)
	f.outsideCall(t, "operator", "a", "claude-opus-5-5", first.Add(-2*time.Hour))
	f.outsideCall(t, "operator", "b", "claude-opus-5-5", first.Add(-time.Hour))
	f.outsideCall(t, "operator", "c", "claude-opus-5-5", first.Add(time.Hour))
	f.outsideCall(t, "privat", "d", "claude-opus-5-5", first.Add(-2*time.Hour))
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

	b := f.s.seat(t, f.rt, daemon.CredSubscription, "seat_b_token", "sitz-b", "B")
	f.link(t, "projekte", "seat_b_token")
	f.reading(t, b, "seat_b_token", 5, nil, first.Add(-5*time.Hour), true, false)
	f.outsideCall(t, "projekte", "e", "claude-opus-5-5", first.Add(-time.Hour))
	if c := f.check(t, "c8"); *c.Facts.Calls != 2 {
		t.Errorf("with a second seat read earlier %d calls before a first reading, want 2", *c.Facts.Calls)
	}
}
