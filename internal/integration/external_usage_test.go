package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/identity"
	"covey/internal/observability"
)

// Usage outside covey (#120): an uploader sends the token counts of Claude
// Code sessions that run on an operator's machine. These tests hold part A's
// acceptance criteria 1, 2, 3 (table side), 6, 7 and 8, and the 10-minute and
// 10-hour buckets.

const externalPath = "/api/v1/cost/external-usage"

// extRow is one upload row with every required field.
func extRow(id string, ts time.Time, model string, out int64) map[string]any {
	return map[string]any{"host": "laptop", "session_id": "s-1", "message_id": id,
		"ts": ts.UTC().Format(time.RFC3339Nano), "model": model,
		"input": 10, "output": out, "cache_read": 1000, "cache_write_5m": 0, "cache_write_1h": 50}
}

func extUpload(source string, rows ...map[string]any) map[string]any {
	if rows == nil {
		rows = []map[string]any{}
	}
	return map[string]any{"source": source, "rows": rows}
}

// rawGet returns a GET's body as bytes, failing on anything but 200.
func rawGet(t *testing.T, c *apiClient, path string) []byte {
	t.Helper()
	resp := c.do(http.MethodGet, path, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	return body
}

// cellsExternal reads /cost/cells and decodes its external field.
func cellsExternal(t *testing.T, c *apiClient, query string) observability.ExternalUsage {
	t.Helper()
	var cells observability.CostCells
	if err := json.Unmarshal(rawGet(t, c, "/api/v1/cost/cells?"+query), &cells); err != nil {
		t.Fatal(err)
	}
	return cells.External
}

func (s *stack) externalCount(t *testing.T, org uuid.UUID) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM external_usage WHERE org_id = $1`, org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Criterion 1: the same batch twice changes neither the row count nor any
// total; a superset adds only the new rows; a changed row replaces its old
// self. The primary key (org, source, message_id) and the upsert are what
// make it so: without them the second post would double every figure.
func TestExternalUsageUploadIsIdempotent(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	batch := []map[string]any{}
	for i := 0; i < 3; i++ {
		batch = append(batch, extRow(fmt.Sprintf("msg_%d", i), base.Add(time.Duration(i)*time.Minute), "claude-opus-5-5", 1000))
	}

	// One call carries Claude Code's own price, with more decimals than the
	// column keeps: a re-upload of it is still unchanged.
	batch[1]["reported_usd"] = 0.123456789
	batch[2]["origin"] = "subagent"

	first := admin.expect(http.MethodPost, externalPath, extUpload("operator: home", batch...), http.StatusOK)
	if first["inserted"] != 3.0 || first["unchanged"] != 0.0 {
		t.Fatalf("first upload: %v", first)
	}
	before := cellsExternal(t, admin, "days=1&bucket=hour")
	if r := before.Sources[0].ReportedUSD; r == nil || math.Abs(*r-0.123457) > 1e-9 {
		t.Errorf("reported_usd = %v, want 0.123457 (NUMERIC(14,6))", r)
	}

	again := admin.expect(http.MethodPost, externalPath, extUpload("operator: home", batch...), http.StatusOK)
	if again["inserted"] != 0.0 || again["updated"] != 0.0 || again["unchanged"] != 3.0 {
		t.Errorf("the same batch again: %v, want 3 unchanged", again)
	}
	if n := s.externalCount(t, s.orgID); n != 3 {
		t.Errorf("%d rows after the second upload, want 3", n)
	}
	after := cellsExternal(t, admin, "days=1&bucket=hour")
	if a, b := mustJSON(t, before), mustJSON(t, after); a != b {
		t.Errorf("a re-upload changed the figures:\nbefore %s\nafter  %s", a, b)
	}

	// A superset: only the two new calls are added.
	super := append(append([]map[string]any{}, batch...),
		extRow("msg_3", base.Add(3*time.Minute), "claude-opus-5-5", 1000),
		extRow("msg_4", base.Add(4*time.Minute), "claude-opus-5-5", 1000))
	res := admin.expect(http.MethodPost, externalPath, extUpload("operator: home", super...), http.StatusOK)
	if res["inserted"] != 2.0 || res["unchanged"] != 3.0 {
		t.Errorf("a superset: %v, want 2 inserted and 3 unchanged", res)
	}
	if n := s.externalCount(t, s.orgID); n != 5 {
		t.Errorf("%d rows after the superset, want 5", n)
	}
	grown := cellsExternal(t, admin, "days=1&bucket=hour")
	if got, want := *grown.Sources[0].ListUSD, *before.Sources[0].ListUSD*5/3; math.Abs(got-want) > 1e-9 {
		t.Errorf("list $ after the superset = %v, want %v", got, want)
	}

	// A row sent again with other counts replaces the stored one.
	changed := extRow("msg_0", base, "claude-opus-5-5", 2000)
	res = admin.expect(http.MethodPost, externalPath, extUpload("operator: home", changed), http.StatusOK)
	if res["updated"] != 1.0 || s.externalCount(t, s.orgID) != 5 {
		t.Errorf("a changed row: %v, %d rows; want 1 updated of 5", res, s.externalCount(t, s.orgID))
	}

	// The same message id under another source is another call.
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work", batch[0]), http.StatusOK)
	if n := s.externalCount(t, s.orgID); n != 6 {
		t.Errorf("%d rows, want 6: the key is per source", n)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// postRaw posts a body as it stands, so a test can send what the client's
// marshalling would never produce.
func postRaw(t *testing.T, c *apiClient, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.base+externalPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// Criterion 2: what the rules refuse is a 400 with nothing stored; no key is
// 401, a seat outside securityRoles 403 — by session and by API key alike;
// and an upload lands in the organisation of the caller's key, never in
// another one.
func TestExternalUsageValidationAndAccess(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	now := time.Now()
	ok := extRow("msg_ok", now.Add(-time.Minute), "claude-opus-5-5", 10)

	with := func(k string, v any) map[string]any {
		r := map[string]any{}
		for kk, vv := range ok {
			r[kk] = vv
		}
		r[k] = v
		return r
	}
	tooMany := make([]map[string]any, observability.ExternalMaxRows+1)
	for i := range tooMany {
		tooMany[i] = extRow(fmt.Sprintf("m%d", i), now.Add(-time.Minute), "claude-opus-5-5", 1)
	}
	for _, c := range []struct {
		name string
		body any
		want string
	}{
		{"an unknown field in a row", extUpload("op", with("prompt", "secret text")), `unknown field \"prompt\"`},
		{"an unknown field at the top", map[string]any{"source": "op", "rows": []any{ok}, "transcript": "x"}, `unknown field \"transcript\"`},
		{"an unknown field in meter_credential", map[string]any{"source": "op", "rows": []any{ok},
			"meter_credential": map[string]any{"secret_key": "k", "secret_slot": 0, "note": "x"}}, `unknown field \"note\"`},
		{"a negative count", extUpload("op", with("cache_read", -1)), "cache_read must not be negative"},
		{"a fractional count", extUpload("op", with("input", 1.5)), "invalid body"},
		{"ts beyond now + 5 min", extUpload("op", with("ts", now.Add(10*time.Minute).UTC().Format(time.RFC3339))), "later than now + 5m0s"},
		{"a ts that is no RFC 3339", extUpload("op", with("ts", "yesterday")), "invalid body"},
		{"a missing count", extUpload("op", func() map[string]any { r := with("x", 0); delete(r, "x"); delete(r, "output"); return r }()), "output is required"},
		{"more than 5,000 rows", extUpload("op", tooMany...), "at most 5000"},
		{"no source", extUpload("", ok), "source is required"},
		{"a source over 64 characters", extUpload(strings.Repeat("s", 65), ok), "at most 64"},
	} {
		resp := admin.do(http.MethodPost, externalPath, c.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), c.want) {
			t.Errorf("%s: HTTP %d %s, want 400 with %q", c.name, resp.StatusCode, body, c.want)
		}
	}
	if code, body := postRaw(t, admin, []byte(`{"source":"op","rows":[]} {"source":"x"}`)); code != http.StatusBadRequest {
		t.Errorf("two objects: HTTP %d %s, want 400", code, body)
	}
	// Over 2 MiB: refused before anything is decoded.
	huge := []byte(`{"source":"op","rows":[],"pad":"` + strings.Repeat("x", observability.ExternalMaxBodyBytes) + `"}`)
	if code, body := postRaw(t, admin, huge); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over 2 MiB: HTTP %d %s, want 413", code, body)
	}
	if n := s.externalCount(t, s.orgID); n != 0 {
		t.Fatalf("%d rows stored by refused uploads", n)
	}

	// No key: 401. A seat whose role is not in securityRoles: 403, with a
	// session and with that seat's API key. security may, like billing-sync.
	resp, err := http.Post(s.http.URL+externalPath, "application/json", strings.NewReader(`{"source":"op","rows":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a key: HTTP %d, want 401", resp.StatusCode)
	}
	if r := bearer(t, s, "covey_not-a-key", http.MethodPost, externalPath, extUpload("op", ok)); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unknown key: HTTP %d, want 401", r.StatusCode)
	}
	s.mitglied(t, "auditor@test.local", "Auditor", identity.RoleAuditor, "auditor-passwort")
	s.mitglied(t, "security@test.local", "Security", identity.RoleSecurity, "security-passwort")
	auditor := login(t, s, "auditor@test.local", "auditor-passwort")
	auditor.expect(http.MethodPost, externalPath, extUpload("op", ok), http.StatusForbidden)
	auditor.expect(http.MethodDelete, externalPath+"?source=op", nil, http.StatusForbidden)
	auditorKey, _ := createKey(t, auditor, "uploader", 30)
	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		r := bearer(t, s, auditorKey, m, externalPath+"?source=op", extUpload("op", ok))
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("%s with an auditor's key: HTTP %d, want 403", m, r.StatusCode)
		}
	}
	// The page reads with any role.
	auditor.expect(http.MethodGet, "/api/v1/cost/cells", nil, http.StatusOK)
	security := login(t, s, "security@test.local", "security-passwort")
	security.expect(http.MethodPost, externalPath, extUpload("op", ok), http.StatusOK)

	// Two organisations: the admin's account holds a seat in a second one,
	// and a key minted there writes there — and only there.
	second := uuid.New()
	if _, err := s.pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, 'Zweite GmbH')`, second); err != nil {
		t.Fatal(err)
	}
	var account uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT account_id FROM humans WHERE id = $1`, s.adminID).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO humans (id, org_id, account_id, email, display_name, password_hash, role)
		VALUES ($1, $2, $3, 'admin@test.local', 'Admin', ' ', 'org_admin')`, uuid.New(), second, account); err != nil {
		t.Fatal(err)
	}
	other := login(t, s, "admin@test.local", "admin-passwort")
	other.expect(http.MethodPost, "/api/v1/auth/switch-org", map[string]any{"org_id": second.String()}, http.StatusOK)
	otherKey, _ := createKey(t, other, "uploader", 30)
	rows := []map[string]any{ok, extRow("msg_other", now.Add(-2*time.Minute), "claude-opus-5-5", 10)}
	r := bearer(t, s, otherKey, http.MethodPost, externalPath, extUpload("op", rows...))
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("upload with the second organisation's key: HTTP %d", r.StatusCode)
	}
	if a, b := s.externalCount(t, s.orgID), s.externalCount(t, second); a != 1 || b != 2 {
		t.Errorf("rows: %d in the first organisation, %d in the second; want 1 and 2", a, b)
	}
	ext := cellsExternal(t, admin, "days=1")
	if len(ext.Sources) != 1 || ext.Sources[0].Calls != 1 {
		t.Errorf("the first organisation sees %+v, want its own single call", ext.Sources)
	}

	// DELETE removes the source of the caller's organisation, nothing else.
	r = bearer(t, s, otherKey, http.MethodDelete, externalPath+"?source=op", nil)
	var del map[string]any
	json.NewDecoder(r.Body).Decode(&del)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || del["deleted"] != 2.0 {
		t.Errorf("delete in the second organisation: HTTP %d %v", r.StatusCode, del)
	}
	if a, b := s.externalCount(t, s.orgID), s.externalCount(t, second); a != 1 || b != 0 {
		t.Errorf("after the delete: %d and %d rows, want 1 and 0", a, b)
	}
	admin.expect(http.MethodDelete, externalPath, nil, http.StatusBadRequest)
	admin.expect(http.MethodDelete, externalPath+"?source=op", nil, http.StatusOK)
	if n := s.externalCount(t, s.orgID); n != 0 {
		t.Errorf("%d rows after deleting the source", n)
	}
}

// Criterion 3, the table side: the table has the columns #120 lists and the
// organisation, nothing that could hold a prompt, a response or tool output.
func TestExternalUsageTableHoldsOnlyTheListedColumns(t *testing.T) {
	s := newStack(t)
	rows, err := s.pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'external_usage' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	rows.Close()
	want := []string{"cache_read", "cache_write_1h", "cache_write_5m", "host", "input", "message_id", "model",
		"org_id", "origin", "output", "reported_usd", "session_id", "source", "ts"}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("external_usage columns:\n got %v\nwant %v", got, want)
	}
}

// Criterion 6, F3 against the database: two hours of meter readings on a
// subscription seat, covey's lines and outside calls on it. The meter's real
// cost is split by list-price share per UTC hour; an hour without outside
// calls gives them 0; an hour without a reading has no figure; and the two
// shares add up to the meter's cost. The split is per hour whatever the
// chart's bucket.
func TestExternalUsageMeterShare(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("meter-anteil")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	h1 := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	h2, h3 := h1.Add(time.Hour), h1.Add(2*time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": h1.Add(-48 * time.Hour)}, http.StatusOK)
	perPoint := 200.0 * 12 / 52 / 100

	// The baseline before the window, then +2 points in h1 and +1 in h2.
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{h1.Add(-30 * time.Hour), 10}, {h1.Add(40 * time.Minute), 12}, {h2.Add(40 * time.Minute), 13}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	// covey's lines: Opus output at $20 per million, 50,000 tokens = $1 in
	// h1 and $2 in h2.
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	f.line(t, cred, h1.Add(10*time.Minute), 50_000)
	f.line(t, cred, h2.Add(10*time.Minute), 100_000)

	// Outside calls linked to the seat: $3 of output in h1, none in h2, and
	// $5 in h3, an hour the meter was not read in.
	out := func(id string, at time.Time, usd float64) map[string]any {
		r := extRow(id, at, "claude-opus-5-5", int64(usd*1e6/20))
		r["input"], r["cache_read"], r["cache_write_1h"] = 0, 0, 0
		return r
	}
	up := extUpload("operator: home", out("a", h1.Add(5*time.Minute), 3), out("c", h3.Add(5*time.Minute), 5))
	up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": 0}
	res := admin.expect(http.MethodPost, externalPath, up, http.StatusOK)
	if mc, _ := res["meter_credential"].(map[string]any); mc["secret_key"] != "claude_code_oauth_token" {
		t.Fatalf("the link was not stored: %v", res)
	}
	// A work machine billed per use: rows, no link, no share.
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work", out("w", h1.Add(5*time.Minute), 7)), http.StatusOK)

	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }
	var reference string
	for _, bucket := range []string{"hour", "day", "10min", "10hour"} {
		ext := cellsExternal(t, admin, "days=2&bucket="+bucket)
		m := ext.Meter
		byHour := map[time.Time]observability.ExternalMeterHour{}
		for _, h := range m.Hours {
			byHour[h.Hour.UTC()] = h
		}
		r1, r2 := 2*perPoint, perPoint
		if h := byHour[h1]; !near(h.RealUSD, r1) || !near(h.ExternalRealUSD, r1*3/4) || !near(h.CoveyRealUSD, r1/4) ||
			math.Abs(h.ExternalListUSD-3) > 1e-9 || math.Abs(h.CoveyListUSD-1) > 1e-9 {
			t.Errorf("%s: h1 = %+v, want R %v split 3:1", bucket, h, r1)
		}
		if h := byHour[h2]; !near(h.RealUSD, r2) || !near(h.ExternalRealUSD, 0) || !near(h.CoveyRealUSD, r2) {
			t.Errorf("%s: h2 = %+v, want R_out 0 and R_cov %v", bucket, h, r2)
		}
		if h, ok := byHour[h3]; !ok || h.RealUSD != nil || h.ExternalRealUSD != nil || h.ExternalListUSD != 5 {
			t.Errorf("%s: h3 = %+v (present %v), want L_out 5 and no figure", bucket, h, ok)
		}
		for _, h := range m.Hours {
			if h.RealUSD != nil && math.Abs(*h.ExternalRealUSD+*h.CoveyRealUSD-*h.RealUSD) > 1e-9 {
				t.Errorf("%s: %v: R_out + R_cov != R (%+v)", bucket, h.Hour, h)
			}
		}
		if math.Abs(m.RealUSD-(r1+r2)) > 1e-9 || math.Abs(m.ExternalRealUSD-r1*3/4) > 1e-9 ||
			!near(m.Share, r1*3/4/(r1+r2)) || m.UnmeteredListUSD != 5 {
			t.Errorf("%s: totals real %v outside %v share %v unmetered %v", bucket, m.RealUSD, m.ExternalRealUSD, m.Share, m.UnmeteredListUSD)
		}
		if len(m.Credentials) != 1 || strings.Join(m.Credentials[0].Sources, ",") != "operator: home" {
			t.Errorf("%s: credentials = %+v, want the seat with the home source only", bucket, m.Credentials)
		}
		if reference == "" {
			reference = mustJSON(t, m)
		} else if got := mustJSON(t, m); got != reference {
			t.Errorf("the split at %s differs from the hourly one:\n%s\n%s", bucket, got, reference)
		}
	}

	// The link must name a subscription credential of the organisation.
	bad := extUpload("operator: home")
	bad["meter_credential"] = map[string]any{"secret_key": "nobody", "secret_slot": 0}
	admin.expect(http.MethodPost, externalPath, bad, http.StatusBadRequest)
	apiRT, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "API", "")
	if err != nil {
		t.Fatal(err)
	}
	s.seat(t, apiRT.ID, daemon.CredAPIKey, "anthropic_api_key", "sk-x", "API")
	bad["meter_credential"] = map[string]any{"secret_key": "anthropic_api_key", "secret_slot": 0}
	admin.expect(http.MethodPost, externalPath, bad, http.StatusBadRequest)

	// Absent leaves the link, null removes it.
	res = admin.expect(http.MethodPost, externalPath, extUpload("operator: home"), http.StatusOK)
	if res["meter_credential"] == nil {
		t.Errorf("an upload without meter_credential dropped the link: %v", res)
	}
	clear := extUpload("operator: home")
	clear["meter_credential"] = nil
	res = admin.expect(http.MethodPost, externalPath, clear, http.StatusOK)
	if res["meter_credential"] != nil {
		t.Errorf("meter_credential null kept the link: %v", res)
	}
	if m := cellsExternal(t, admin, "days=2&bucket=hour").Meter; len(m.Hours) != 0 || m.Share != nil {
		t.Errorf("without a link there is a share: %+v", m)
	}
}

// Criterion 7, F4: the agents' figures in /cost/cells, /cost/models and
// /cost/org are the same with and without outside rows — byte for byte; the
// outside series is only the new `external` field. And nothing is written to
// the agents' tables.
func TestExternalUsageLeavesTheAgentsFiguresAlone(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("unberuehrt")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	h := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": h.Add(-48 * time.Hour)}, http.StatusOK)
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	f.line(t, cred, h.Add(10*time.Minute), 50_000)
	if err := s.obs.AddCost(ctx, agent.ID, nil, 1.25, observability.Tokens{Input: 1000, Output: 200}, "claude-opus-5-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{h.Add(-time.Hour), 10}, {h.Add(30 * time.Minute), 14}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	counts := func() string {
		var ce, mu, budgets int
		if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM cost_entries), (SELECT count(*) FROM model_usage),
			(SELECT count(*) FROM agents WHERE budget_usd <> 0)`).Scan(&ce, &mu, &budgets); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(ce, mu, budgets)
	}
	paths := []string{"/api/v1/cost/cells?days=1&bucket=hour", "/api/v1/cost/cells?days=1&bucket=day",
		"/api/v1/cost/models?days=1", "/api/v1/cost/org?days=1&bucket=hour", "/api/v1/cost/org?days=1"}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, p := range paths {
			body := rawGet(t, admin, p)
			if strings.HasPrefix(p, "/api/v1/cost/cells") {
				var m map[string]json.RawMessage
				if err := json.Unmarshal(body, &m); err != nil {
					t.Fatal(err)
				}
				if _, ok := m["external"]; !ok {
					t.Fatalf("%s has no external field", p)
				}
				delete(m, "external")
				body, _ = json.Marshal(m)
			}
			out[p] = string(body)
		}
		return out
	}
	before, beforeCounts := snapshot(), counts()

	rows := []map[string]any{}
	for i := 0; i < 20; i++ {
		rows = append(rows, extRow(fmt.Sprintf("m%d", i), h.Add(time.Duration(i)*time.Minute), "claude-opus-5-5", 100_000))
	}
	rows = append(rows, extRow("unknown", h, "claude-sonnet-5-7", 1000))
	up := extUpload("operator: home", rows...)
	up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": 0}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	after := snapshot()
	for _, p := range paths {
		if before[p] != after[p] {
			t.Errorf("%s changed with outside rows present:\nbefore %s\nafter  %s", p, before[p], after[p])
		}
	}
	if c := counts(); c != beforeCounts {
		t.Errorf("cost_entries, model_usage, budgets: %s before, %s after", beforeCounts, c)
	}
	if ext := cellsExternal(t, admin, "days=1&bucket=hour"); len(ext.Cells) == 0 || ext.Meter.Share == nil {
		t.Errorf("the outside series is missing: %+v", ext)
	}
}

// Criterion 8: a call of a model the list does not know is stored, has no
// dollar figure — null, not 0 — and its tokens are counted as unpriced.
func TestExternalUsageUnknownModelIsUnpriced(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	at := time.Now().Add(-time.Hour)
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work",
		extRow("u1", at, "claude-sonnet-5-7", 1000)), http.StatusOK)
	if n := s.externalCount(t, s.orgID); n != 1 {
		t.Fatalf("%d rows, want the unknown model's call stored", n)
	}

	body := rawGet(t, admin, "/api/v1/cost/cells?days=1&bucket=hour")
	var raw struct {
		External struct {
			Sources []map[string]json.RawMessage `json:"sources"`
			Cells   []map[string]json.RawMessage `json:"cells"`
		} `json:"external"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.External.Cells) != 1 || len(raw.External.Sources) != 1 {
		t.Fatalf("external = %s", body)
	}
	cell, src := raw.External.Cells[0], raw.External.Sources[0]
	tokens := "2060" // 10 + 1000 + 1000 + 0 + 50
	for name, m := range map[string]map[string]json.RawMessage{"cell": cell, "source": src} {
		if string(m["list_usd"]) != "null" {
			t.Errorf("%s list_usd = %s, want null", name, m["list_usd"])
		}
		if string(m["unpriced_tokens"]) != tokens {
			t.Errorf("%s unpriced_tokens = %s, want %s", name, m["unpriced_tokens"], tokens)
		}
	}
	if string(cell["measured"]) != "{}" {
		t.Errorf("the cell was split into cost types: %s", cell["measured"])
	}
	if string(src["unpriced_models"]) != `["claude-sonnet-5-7"]` {
		t.Errorf("unpriced_models = %s", src["unpriced_models"])
	}

	// Beside a priced call the cell has the priced call's figure, and the
	// unknown one's tokens stay unpriced.
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work",
		extRow("p1", at, "claude-opus-5-5", 1000)), http.StatusOK)
	ext := cellsExternal(t, admin, "days=1&bucket=hour")
	want := observability.ExternalListUSD("claude-opus-5-5", observability.ModelTokens{Input: 10, Output: 1000, CacheRead: 1000, CacheWrite1h: 50})
	if len(ext.Cells) != 1 || ext.Cells[0].ListUSD == nil || math.Abs(*ext.Cells[0].ListUSD-*want) > 1e-12 ||
		ext.Cells[0].UnpricedTokens != 2060 || ext.Cells[0].Calls != 2 {
		t.Errorf("mixed cell = %+v, want list %v and 2060 unpriced tokens", ext.Cells, *want)
	}
}

// The 10-minute and 10-hour buckets: every cost endpoint with ?bucket= takes
// them, they are cut from the epoch (fixed boundaries), a 10-hour bucket is
// the sum of its 10-minute ones (F2), and the bill — hourly at the finest —
// answers 10min with an empty series and a reason.
func TestCostBucketsTenMinutesAndTenHours(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("zehner")
	if err := s.obs.AddCost(ctx, agent.ID, nil, 0.5, observability.Tokens{Input: 10}, "claude-opus-5-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}

	// On 2026-11-18 the 10-hour boundaries are 00:00, 10:00 and 20:00 UTC.
	for in, want := range map[string]string{"2026-11-18 09:59:59Z": "2026-11-18 00:00:00", "2026-11-18 10:00:00Z": "2026-11-18 10:00:00",
		"2026-11-18 23:15:00Z": "2026-11-18 20:00:00", "2026-11-19 05:00:00Z": "2026-11-18 20:00:00"} {
		var got string
		if err := s.pool.QueryRow(ctx, `SELECT to_char(date_bin('10 hours', $1::timestamptz, TIMESTAMPTZ '1970-01-01 00:00 UTC')
			AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')`, in).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("10-hour bucket of %s: %s, want %s", in, got, want)
		}
	}

	// 300 calls, one every 4 minutes, over 20 hours.
	start := time.Now().UTC().Add(-21 * time.Hour)
	rows := []map[string]any{}
	for i := 0; i < 300; i++ {
		r := extRow(fmt.Sprintf("m%03d", i), start.Add(time.Duration(i)*4*time.Minute), "claude-opus-5-5", int64(1000+i*37))
		r["origin"] = []string{"main", "subagent", "workflow"}[i%3]
		rows = append(rows, r)
	}
	admin.expect(http.MethodPost, externalPath, extUpload("operator: home", rows...), http.StatusOK)

	aligned := func(name string, p time.Time, width time.Duration) {
		if p.Unix()%int64(width/time.Second) != 0 {
			t.Errorf("%s: period %v is not on a %v boundary from the epoch", name, p, width)
		}
	}
	sums := map[string]map[time.Time]float64{}
	for bucket, width := range map[string]time.Duration{"10min": 10 * time.Minute, "10hour": 10 * time.Hour} {
		var cells observability.CostCells
		if err := json.Unmarshal(rawGet(t, admin, "/api/v1/cost/cells?days=2&bucket="+bucket), &cells); err != nil {
			t.Fatal(err)
		}
		if len(cells.Cells) == 0 || len(cells.External.Cells) == 0 {
			t.Fatalf("%s: no cells", bucket)
		}
		for _, c := range cells.Cells {
			aligned(bucket+" agent cell", c.Period, width)
		}
		sums[bucket] = map[time.Time]float64{}
		for _, c := range cells.External.Cells {
			aligned(bucket+" external cell", c.Period, width)
			sums[bucket][c.Period.UTC()] += *c.ListUSD
		}

		org := admin.expect(http.MethodGet, "/api/v1/cost/org?days=2&bucket="+bucket, nil, http.StatusOK)
		if org["bucket"] != bucket {
			t.Errorf("/cost/org bucket = %v, want %s", org["bucket"], bucket)
		}
		for _, sp := range org["series"].([]any) {
			p, _ := time.Parse(time.RFC3339, sp.(map[string]any)["period"].(string))
			aligned(bucket+" org series", p, width)
		}
		for _, sp := range admin.expectList(http.MethodGet, "/api/v1/agents/"+agent.ID.String()+"/cost/series?days=2&bucket="+bucket, nil, http.StatusOK) {
			p, _ := time.Parse(time.RFC3339, sp["period"].(string))
			aligned(bucket+" agent series", p, width)
		}
	}

	// F2: each 10-hour bucket is the sum of its sixty 10-minute buckets.
	if len(sums["10hour"]) < 2 {
		t.Fatalf("the calls span %d 10-hour buckets, want at least 2", len(sums["10hour"]))
	}
	var total10m, total10h float64
	for p, usd := range sums["10hour"] {
		var sum float64
		for q, u := range sums["10min"] {
			if !q.Before(p) && q.Before(p.Add(10*time.Hour)) {
				sum += u
			}
		}
		if math.Abs(sum-usd) > 1e-9 {
			t.Errorf("10-hour bucket %v: %v, Σ of its 10-minute buckets %v", p, usd, sum)
		}
		total10h += usd
	}
	for _, u := range sums["10min"] {
		total10m += u
	}
	if math.Abs(total10m-total10h) > 1e-9 {
		t.Errorf("totals: 10min %v, 10hour %v", total10m, total10h)
	}

	// The bill: empty at 10min with the reason, a plain series at 10hour.
	billed := admin.expect(http.MethodGet, "/api/v1/cost/billed?days=2&bucket=10min", nil, http.StatusOK)
	if pts, _ := billed["points"].([]any); billed["reason"] != observability.ReasonBucketBelowSource || pts == nil || len(pts) != 0 {
		t.Errorf("billed at 10min: %v", billed)
	}
	billed = admin.expect(http.MethodGet, "/api/v1/cost/billed?days=2&bucket=10hour", nil, http.StatusOK)
	if _, ok := billed["reason"]; ok || billed["bucket"] != "10hour" {
		t.Errorf("billed at 10hour: %v", billed)
	}
	// And the buckets it had keep their answer without a reason.
	for _, b := range []string{"hour", "day", "week"} {
		if billed := admin.expect(http.MethodGet, "/api/v1/cost/billed?days=2&bucket="+b, nil, http.StatusOK); billed["reason"] != nil {
			t.Errorf("billed at %s carries a reason: %v", b, billed["reason"])
		}
	}
}
