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
	// The push itself is recorded (the host's pushed_at); the figures are not
	// touched.
	for _, e := range []*observability.ExternalUsage{&before, &after} {
		for i := range e.Sources {
			for j := range e.Sources[i].Hosts {
				e.Sources[i].Hosts[j].PushedAt = time.Time{}
			}
		}
	}
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
		// Postgres refuses 0x00 in text: a 400, not a 500 at the insert.
		{"a NUL in the host", extUpload("op", with("host", "lap\u0000top")), "host must not contain a NUL"},
		{"a count above the bound", extUpload("op", with("input", int64(2_000_000_000))), "input must be at most"},
		{"a watermark without a host", map[string]any{"source": "op", "rows": []any{},
			"reported_through": map[string]any{"": now}}, "reported_through host is required"},
	} {
		resp := admin.do(http.MethodPost, externalPath, c.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), c.want) {
			t.Errorf("%s: HTTP %d %s, want 400 with %q", c.name, resp.StatusCode, body, c.want)
		}
	}
	for _, raw := range []string{`{"source":"op","rows":[]} {"source":"x"}`, `{"source":"op","rows":[]}]`, `{"source":"op","rows":[]}}`} {
		if code, body := postRaw(t, admin, []byte(raw)); code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s, want 400", raw, code, body)
		}
	}
	if code, body := postRaw(t, admin, []byte(`{"source":"op","rows":[]}`+"\n ")); code != http.StatusOK {
		t.Errorf("one object and white space: HTTP %d %s, want 200", code, body)
	}
	if r := admin.do(http.MethodDelete, externalPath+"?source=a%00b", nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("DELETE of a source with a NUL: HTTP %d, want 400", r.StatusCode)
	} else {
		r.Body.Close()
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
// subscription seat, covey's lines and outside calls on it. Since F3c the
// meter's real cost is split per chunk of readings by the chunk's list-price
// share: the two readings (+2 points over L_out 3 / L_cov 1, +1 over 0 / 2)
// are one chunk of 3 points over 3 / 3, so each takes the share ½ — the
// reading with no outside call too; an hour without a reading has no
// figure; and the two shares add up to the meter's cost. The split is the
// same whatever the chart's bucket.
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
	h2, h3, h4 := h1.Add(time.Hour), h1.Add(2*time.Hour), h1.Add(3*time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": h1.Add(-48 * time.Hour)}, http.StatusOK)
	perPoint := 200.0 * 12 / 52 / 100

	// The baseline before the window, then +2 points at h1:40, +1 at h2:40
	// and +1 at h4:40, each step over the span since the reading before it.
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{h1.Add(-30 * time.Hour), 10}, {h1.Add(40 * time.Minute), 12}, {h2.Add(40 * time.Minute), 13},
		{h4.Add(40 * time.Minute), 14}} {
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

	// Outside calls linked to the seat: $3 of output in h1 and $5 in h3, an
	// hour the meter was not read in: the h4:40 step's span holds it. The
	// laptop has reported through h3:30 only, so that step is not split.
	out := func(id string, at time.Time, usd float64) map[string]any {
		r := extRow(id, at, "claude-opus-5-5", int64(usd*1e6/20))
		r["input"], r["cache_read"], r["cache_write_1h"] = 0, 0, 0
		return r
	}
	up := extUpload("operator: home", out("a", h1.Add(5*time.Minute), 3), out("c", h3.Add(5*time.Minute), 5))
	up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": 0}
	up["reported_through"] = map[string]any{"laptop": h3.Add(30 * time.Minute)}
	res := admin.expect(http.MethodPost, externalPath, up, http.StatusOK)
	if mc, _ := res["meter_credential"].(map[string]any); mc["secret_key"] != "claude_code_oauth_token" {
		t.Fatalf("the link was not stored: %v", res)
	}
	// A work machine billed per use: rows, no link, no share.
	admin.expect(http.MethodPost, externalPath, extUpload("operator: work", out("w", h1.Add(5*time.Minute), 7)), http.StatusOK)

	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }
	nearV := func(v, want float64) bool { return math.Abs(v-want) < 1e-9 }
	r1, r2 := 2*perPoint, perPoint
	var reference string
	for _, bucket := range []string{"hour", "day", "10min", "10hour"} {
		m := cellsExternal(t, admin, "days=2&bucket="+bucket).Meter
		if len(m.Steps) != 3 {
			t.Fatalf("%s: steps = %+v, want the three readings after the baseline", bucket, m.Steps)
		}
		s1, s2, s3 := m.Steps[0], m.Steps[1], m.Steps[2]
		if !s1.At.Equal(h1.Add(40*time.Minute)) || !s1.From.Equal(h1.Add(-30*time.Hour)) || !s1.Split ||
			!near(s1.RealUSD, r1) || !near(s1.ExternalRealUSD, r1/2) || !near(s1.CoveyRealUSD, r1/2) ||
			!nearV(s1.ExternalListUSD, 3) || !nearV(s1.CoveyListUSD, 1) {
			t.Errorf("%s: step 1 = %+v, want R %v split 1:1 (its chunk's share) over (baseline, h1:40]", bucket, s1, r1)
		}
		if !s2.Split || !near(s2.ExternalRealUSD, r2/2) || !near(s2.CoveyRealUSD, r2/2) {
			t.Errorf("%s: step 2 = %+v, want R_out and R_cov %v each", bucket, s2, r2/2)
		}
		// h4:40 lies after the laptop's watermark: not reported, not split,
		// though its span holds h3's call.
		if s3.Split || s3.Reported || s3.Reason != observability.SplitNotReported || !nearV(s3.UnsplitRealUSD, perPoint) ||
			!nearV(s3.ExternalListUSD, 5) || s3.ExternalRealUSD != nil || s3.CoveyRealUSD != nil {
			t.Errorf("%s: step 3 = %+v, want R %v unsplit with L_out 5", bucket, s3, perPoint)
		}
		if len(m.Hours) != 3 || !m.Hours[0].Hour.Equal(h1) || !m.Hours[2].Hour.Equal(h4) {
			t.Errorf("%s: hours = %+v, want h1, h2, h4", bucket, m.Hours)
		}
		if !nearV(m.RealUSD, r1+r2+perPoint) || !nearV(m.ExternalRealUSD, (r1+r2)/2) || !nearV(m.CoveyRealUSD, (r1+r2)/2) ||
			!nearV(m.UnsplitRealUSD, perPoint) || !near(m.Share, 0.5) || m.UnmeteredListUSD != 0 {
			t.Errorf("%s: totals real %v outside %v covey %v unsplit %v share %v unmetered %v", bucket,
				m.RealUSD, m.ExternalRealUSD, m.CoveyRealUSD, m.UnsplitRealUSD, m.Share, m.UnmeteredListUSD)
		}
		if m.SplitExternalListUSD != 3 || !nearV(m.SplitCoveyListUSD, 3) || m.SplitPoints != 3 || m.SplitRuns != 1 {
			t.Errorf("%s: split list %v / %v, points %v, runs %d; want 3 / 3, 3, 1", bucket,
				m.SplitExternalListUSD, m.SplitCoveyListUSD, m.SplitPoints, m.SplitRuns)
		}
		if len(m.Credentials) != 1 || strings.Join(m.Credentials[0].Sources, ",") != "operator: home" {
			t.Errorf("%s: credentials = %+v, want the seat with the home source only", bucket, m.Credentials)
		}
		if a := m.Credentials[0].Agents; len(a) != 1 || a[0].AgentID != agent.ID || !nearV(a[0].RealUSD, m.CoveyRealUSD) ||
			!nearV(a[0].SplitListUSD, 3) {
			t.Errorf("%s: agents = %+v, want the one agent with all of covey's part", bucket, a)
		}
		if reference == "" {
			reference = mustJSON(t, m)
		} else if got := mustJSON(t, m); got != reference {
			t.Errorf("the split at %s differs from the hourly one:\n%s\n%s", bucket, got, reference)
		}
	}

	// The watermark is clamped to the push and never moves back. An empty
	// push through now reports the last step: it joins the chunk (4 points
	// over L_out 8 / L_cov 3), and takes its share 8/11, though its own span
	// has no covey line.
	now := time.Now().UTC()
	push := func(at time.Time) []any {
		t.Helper()
		up := extUpload("operator: home")
		up["reported_through"] = map[string]any{"laptop": at}
		hosts, _ := admin.expect(http.MethodPost, externalPath, up, http.StatusOK)["hosts"].([]any)
		return hosts
	}
	hosts := push(now.Add(time.Hour))
	h, _ := hosts[0].(map[string]any)
	stored, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(h["reported_through"]))
	if len(hosts) != 1 || h["host"] != "laptop" || stored.After(time.Now()) || stored.Before(now.Add(-time.Second)) {
		t.Errorf("a watermark an hour ahead: hosts %v, want it clamped to the push", hosts)
	}
	if h, _ := push(h1)[0].(map[string]any); fmt.Sprint(h["reported_through"]) != stored.Format(time.RFC3339Nano) {
		t.Errorf("an older watermark moved it back: %v, want %v", h["reported_through"], stored)
	}
	m := cellsExternal(t, admin, "days=2&bucket=hour").Meter
	if s3 := m.Steps[2]; !s3.Split || !near(s3.ExternalRealUSD, perPoint*8/11) || !near(s3.CoveyRealUSD, perPoint*3/11) ||
		m.UnsplitRealUSD != 0 || m.SplitRuns != 1 {
		t.Errorf("after the push: step 3 = %+v, unsplit %v, chunks %d; want R_out %v", s3, m.UnsplitRealUSD, m.SplitRuns, perPoint*8/11)
	}
	src := cellsExternal(t, admin, "days=2&bucket=hour").Sources
	if len(src) != 2 || len(src[0].Hosts) != 1 || src[0].Hosts[0].ReportedThrough == nil {
		t.Errorf("sources = %+v, want the home source's laptop with its watermark", src)
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

// F3 on two seats, one of them with no linked source (#126 fixes 1 and 2).
// Seat A is linked to "operator: home"; seat B is covey's alone. At $10 a
// point (a $52,000 yearly plan: $1,000 a week):
//
//   - A, +10 points ($100) at h1:40: L_out $1.50, L_cov $1.00, so $60 to the
//     outside sessions and $40 to covey.
//   - B, +20 points ($200) at h1:45: no source can report on it, so L_out =
//     0 and all $200 is covey's. Before the fix it was unsplit as "not
//     reported", and the headline showed covey $40 beside $200 not split.
//   - A, +1 point ($10) at h2:40: L_cov $0.10, and three outside calls of a
//     model the list does not know, without reported_usd. L_out is unknown,
//     not 0: unsplit (outside_unpriced), never covey's $10.
//
// So the dent is $310: covey $240, outside $60, not split $10.
func TestExternalUsageMeterShareSeatWithoutSource(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("zwei-sitze")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ordA := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz-a", "A")
	ordB := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz-b", "B")
	h1 := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	h2 := h1.Add(time.Hour)
	creds := map[int]observability.CredentialRef{}
	for _, ord := range []int{ordA, ordB} {
		admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
			map[string]any{"plan_name": "Max", "amount": 52000, "currency": "USD", "period": "year", "tax_basis": "net",
				"valid_from": h1.Add(-48 * time.Hour)}, http.StatusOK)
		var slot int
		if err := s.pool.QueryRow(ctx, `SELECT secret_slot FROM runtime_credentials WHERE runtime_id=$1 AND ord=$2`,
			rt.ID, ord).Scan(&slot); err != nil {
			t.Fatal(err)
		}
		creds[ord] = observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token",
			SecretSlot: slot, Kind: "subscription"}
	}
	credA, credB := creds[ordA], creds[ordB]
	if credA.SecretSlot == credB.SecretSlot {
		t.Fatalf("both seats on slot %d", credA.SecretSlot)
	}
	read := func(cred observability.CredentialRef, at time.Time, pct float64) {
		t.Helper()
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: pct, FetchedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	read(credA, h1.Add(-30*time.Hour), 10)
	read(credA, h1.Add(40*time.Minute), 20)
	read(credA, h2.Add(40*time.Minute), 21)
	read(credB, h1.Add(-30*time.Hour), 10)
	read(credB, h1.Add(45*time.Minute), 30)

	// covey's lines, Opus output at $20 per million: $1.00 on A and $2.00 on
	// B in h1, $0.10 on A in h2.
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	f.line(t, credA, h1.Add(10*time.Minute), 50_000)
	f.line(t, credB, h1.Add(20*time.Minute), 100_000)
	f.line(t, credA, h2.Add(10*time.Minute), 5_000)

	// Outside on A: $1.50 of Opus output in h1; in h2 three calls of an
	// unknown model with no reported cost. The laptop has reported through
	// h3, past every reading.
	opus := extRow("o1", h1.Add(5*time.Minute), "claude-opus-5-5", 75_000)
	opus["input"], opus["cache_read"], opus["cache_write_1h"] = 0, 0, 0
	rows := []map[string]any{opus}
	for i := 0; i < 3; i++ {
		rows = append(rows, extRow(fmt.Sprintf("u%d", i), h2.Add(time.Duration(5+i)*time.Minute), "claude-sonnet-5-7", 1000))
	}
	up := extUpload("operator: home", rows...)
	up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": credA.SecretSlot}
	up["reported_through"] = map[string]any{"laptop": h1.Add(2 * time.Hour)}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	nearV := func(v, want float64) bool { return math.Abs(v-want) < 1e-9 }
	near := func(p *float64, want float64) bool { return p != nil && nearV(*p, want) }
	m := cellsExternal(t, admin, "days=2&bucket=hour").Meter
	if len(m.Steps) != 3 {
		t.Fatalf("steps = %+v, want A at h1:40, B at h1:45, A at h2:40", m.Steps)
	}
	a1, b1, a2 := m.Steps[0], m.Steps[1], m.Steps[2]
	if a1.SecretSlot != credA.SecretSlot || !a1.Split || !near(a1.RealUSD, 100) || !near(a1.ExternalRealUSD, 60) ||
		!near(a1.CoveyRealUSD, 40) || !nearV(a1.ExternalListUSD, 1.5) || !nearV(a1.CoveyListUSD, 1) {
		t.Errorf("A at h1:40 = %+v, want $100 split $60 / $40", a1)
	}
	if b1.SecretSlot != credB.SecretSlot || !b1.Reported || !b1.Split || b1.Reason != "" || !near(b1.RealUSD, 200) ||
		!near(b1.ExternalRealUSD, 0) || !near(b1.CoveyRealUSD, 200) || b1.UnsplitRealUSD != 0 {
		t.Errorf("B at h1:45 = %+v, want reported, $200 all covey's", b1)
	}
	if a2.SecretSlot != credA.SecretSlot || a2.Split || a2.Reason != observability.SplitOutsideUnpriced ||
		!a2.Reported || !near(a2.RealUSD, 10) || !nearV(a2.UnsplitRealUSD, 10) || a2.CoveyRealUSD != nil ||
		a2.ExternalListUSD != 0 || !nearV(a2.CoveyListUSD, 0.1) {
		t.Errorf("A at h2:40 = %+v, want $10 unsplit (%s)", a2, observability.SplitOutsideUnpriced)
	}

	// The headline.
	if !nearV(m.RealUSD, 310) || !nearV(m.CoveyRealUSD, 240) || !nearV(m.ExternalRealUSD, 60) ||
		!nearV(m.UnsplitRealUSD, 10) || !near(m.Share, 60.0/300) {
		t.Errorf("headline: real %v covey %v outside %v unsplit %v share %v; want 310, 240, 60, 10, 0.2",
			m.RealUSD, m.CoveyRealUSD, m.ExternalRealUSD, m.UnsplitRealUSD, m.Share)
	}
	if m.SplitSteps != 2 || m.UnsplitSteps != 1 || m.OutsideUnpricedSteps != 1 || !nearV(m.OutsideUnpricedRealUSD, 10) ||
		m.UnpricedCalls != 3 {
		t.Errorf("steps: %d split, %d unsplit, %d outside unpriced ($%v), %d unpriced calls; want 2, 1, 1 ($10), 3",
			m.SplitSteps, m.UnsplitSteps, m.OutsideUnpricedSteps, m.OutsideUnpricedRealUSD, m.UnpricedCalls)
	}

	// Per seat and per agent: the agent's real share is covey's part of the
	// split steps, $40 on A and $200 on B; A's $0.10 line is unsplit.
	if len(m.Credentials) != 2 {
		t.Fatalf("credentials = %+v, want the two seats", m.Credentials)
	}
	for _, c := range m.Credentials {
		var wantCovey, wantUnsplit, wantSplitList, wantUnsplitList float64
		var wantSources string
		switch c.SecretSlot {
		case credA.SecretSlot:
			wantCovey, wantUnsplit, wantSplitList, wantUnsplitList, wantSources = 40, 10, 1, 0.1, "operator: home"
		case credB.SecretSlot:
			wantCovey, wantSplitList = 200, 2
		}
		if strings.Join(c.Sources, ",") != wantSources || !nearV(c.CoveyRealUSD, wantCovey) || !nearV(c.UnsplitRealUSD, wantUnsplit) {
			t.Errorf("seat %d: sources %v covey %v unsplit %v; want %q, %v, %v", c.SecretSlot, c.Sources,
				c.CoveyRealUSD, c.UnsplitRealUSD, wantSources, wantCovey, wantUnsplit)
		}
		if len(c.Agents) != 1 || c.Agents[0].AgentID != agent.ID || !nearV(c.Agents[0].RealUSD, wantCovey) ||
			!nearV(c.Agents[0].SplitListUSD, wantSplitList) || !nearV(c.Agents[0].UnsplitListUSD, wantUnsplitList) {
			t.Errorf("seat %d agents = %+v, want real $%v, split list $%v, unsplit list $%v", c.SecretSlot, c.Agents,
				wantCovey, wantSplitList, wantUnsplitList)
		}
	}

	// The chart's hours: B's hour is covey's whole, A's second hour unsplit.
	type hk struct {
		h    time.Time
		slot int
	}
	hours := map[hk]observability.ExternalMeterHour{}
	for _, h := range m.Hours {
		hours[hk{h.Hour, h.SecretSlot}] = h
	}
	if h := hours[hk{h1, credB.SecretSlot}]; h.SplitSteps != 1 || !nearV(h.CoveyRealUSD, 200) || h.UnsplitRealUSD != 0 {
		t.Errorf("B's hour h1 = %+v, want $200 covey's", h)
	}
	if h := hours[hk{h1, credA.SecretSlot}]; !nearV(h.ExternalRealUSD, 60) || !nearV(h.CoveyRealUSD, 40) {
		t.Errorf("A's hour h1 = %+v, want $60 / $40", h)
	}
	if h := hours[hk{h2, credA.SecretSlot}]; h.SplitSteps != 0 || !nearV(h.UnsplitRealUSD, 10) || h.CoveyRealUSD != 0 {
		t.Errorf("A's hour h2 = %+v, want $10 unsplit", h)
	}
}

// F3c end to end (#120): the split per chunk of readings and the
// unattributed share. One seat at $200 a month (pp a point); each reading
// with calls has $1 of covey's Opus output and $2 of the home source's,
// list $3:
//
//   - h1:40, +10 points: chunk 1, the clean one, k = (10 + 2) / 3 = 4
//     points per list $; U = 0, split 1:2.
//   - h2:40, +1 point, no call on either side (the meter's lag): it opens
//     chunk 2 and is split with it, not unattributed whole.
//   - h3:40, +15 points over $3: chunk 2 closes at 16 points, U = 16 − 2 −
//     4 × 3 = 2 points, spread 1 : 15 over h2 and h3; the other 14 split
//     1:2.
//   - h4:40, +1 point with no call, then h5:40, +20 points over $3 and a
//     covey booking no line was linked to: chunk 3 is incomplete, so it has
//     no unattributed part (it would have 21 − 2 − 12 = 7 points) and sets
//     no rate.
//   - h6:40, +1 point with no call right after chunk 3 closed at its size,
//     the last reading: the meter's lag, folded into chunk 3 and split 1:2
//     with it, not a chunk of its own booked to nobody.
//
// So of 48 points, 2 are unattributed (booked to nobody), covey 46/3, the
// outside sessions 92/3.
func TestExternalUsageUnattributedShare(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("ohne-zuordnung")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	h1 := time.Now().UTC().Truncate(time.Hour).Add(-7 * time.Hour)
	h2, h3, h4, h5, h6 := h1.Add(time.Hour), h1.Add(2*time.Hour), h1.Add(3*time.Hour), h1.Add(4*time.Hour), h1.Add(5*time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": h1.Add(-48 * time.Hour)}, http.StatusOK)
	pp := 200.0 * 12 / 52 / 100
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{h1.Add(-30 * time.Hour), 10}, {h1.Add(40 * time.Minute), 20}, {h2.Add(40 * time.Minute), 21},
		{h3.Add(40 * time.Minute), 36}, {h4.Add(40 * time.Minute), 37}, {h5.Add(40 * time.Minute), 57},
		{h6.Add(40 * time.Minute), 58}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	for _, h := range []time.Time{h1, h3, h5} {
		f.line(t, cred, h.Add(10*time.Minute), 50_000)
	}
	// A booking on the seat no line was linked to (an older daemon).
	f.exec(t, `INSERT INTO cost_entries (agent_id, model, usd, output_tokens, created_at, secret_key, secret_slot)
		VALUES ($1, 'claude-opus-5-5', 0.5, 1000, $2, 'claude_code_oauth_token', 0)`, agent.ID, h5.Add(20*time.Minute))
	out := func(id string, at time.Time) map[string]any {
		r := extRow(id, at, "claude-opus-5-5", 100_000)
		r["input"], r["cache_read"], r["cache_write_1h"] = 0, 0, 0
		return r
	}
	up := extUpload("operator: home", out("a", h1.Add(5*time.Minute)), out("b", h3.Add(5*time.Minute)), out("c", h5.Add(5*time.Minute)))
	up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": 0}
	up["reported_through"] = map[string]any{"laptop": h6.Add(50 * time.Minute)}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	nearV := func(v, want float64) bool { return math.Abs(v-want) < 1e-9 }
	m := cellsExternal(t, admin, "days=2&bucket=hour").Meter
	if len(m.Steps) != 6 || len(m.Credentials) != 1 {
		t.Fatalf("steps %+v, credentials %+v; want six steps on one seat", m.Steps, m.Credentials)
	}
	c := m.Credentials[0]
	if c.PointsPerListUSD == nil || !nearV(*c.PointsPerListUSD, 4) || len(c.PlanRates) != 1 || !nearV(c.PlanRates[0].USDPerPoint, pp) {
		t.Errorf("k = %v, plan rates %+v; want 4 points per list $ at one plan price", c.PointsPerListUSD, c.PlanRates)
	}
	st := m.Steps
	if !st[0].Split || st[0].UnattributedRealUSD != 0 || !nearV(*st[0].CoveyRealUSD, 10*pp/3) || !nearV(*st[0].ExternalRealUSD, 20*pp/3) {
		t.Errorf("h1:40 = %+v, want 10 points split 1:2, none unattributed", st[0])
	}
	if !st[1].Split || st[1].Reason != observability.SplitNoReportedCall || !nearV(st[1].UnattributedRealUSD, pp/8) ||
		!nearV(*st[1].CoveyRealUSD, 7*pp/24) || st[1].UnsplitRealUSD != 0 {
		t.Errorf("h2:40 = %+v, want its point split with chunk 2 (⅛ unattributed)", st[1])
	}
	if !st[2].Split || !nearV(st[2].UnattributedRealUSD, 15*pp/8) || !nearV(st[2].UnattributedPoints, 15.0/8) ||
		!nearV(*st[2].CoveyRealUSD, 35*pp/8) || !nearV(*st[2].ExternalRealUSD, 35*pp/4) {
		t.Errorf("h3:40 = %+v, want 15/8 points unattributed and the rest split 1:2", st[2])
	}
	if !st[3].Split || !st[4].Split || st[3].UnattributedRealUSD != 0 || st[4].UnattributedRealUSD != 0 ||
		!nearV(*st[4].CoveyRealUSD, 20*pp/3) || !nearV(*st[3].CoveyRealUSD, pp/3) {
		t.Errorf("h4:40 = %+v, h5:40 = %+v; want chunk 3 split 1:2 with nothing unattributed", st[3], st[4])
	}
	if !st[5].Split || st[5].Reason != observability.SplitNoReportedCall || st[5].UnattributedRealUSD != 0 ||
		!nearV(*st[5].CoveyRealUSD, pp/3) || !nearV(*st[5].ExternalRealUSD, 2*pp/3) {
		t.Errorf("h6:40 = %+v; want the lag folded into chunk 3, split 1:2", st[5])
	}
	if !nearV(m.RealUSD, 48*pp) || !nearV(m.UnattributedRealUSD, 2*pp) || !nearV(m.UnattributedPoints, 2) ||
		m.UnattributedSteps != 2 || !nearV(m.CoveyRealUSD, 46*pp/3) || !nearV(m.ExternalRealUSD, 92*pp/3) ||
		m.UnsplitRealUSD != 0 || m.UnsplitSteps != 0 || m.UnattributedNoCallRealUSD != 0 || m.IncompleteChunks != 1 ||
		m.SplitRuns != 3 || m.SplitSteps != 6 {
		t.Errorf("headline: real %v unattributed %v (%v points, %d steps, no call %v) covey %v outside %v unsplit %v (%d), "+
			"incomplete %d, chunks %d, split steps %d", m.RealUSD, m.UnattributedRealUSD, m.UnattributedPoints, m.UnattributedSteps,
			m.UnattributedNoCallRealUSD, m.CoveyRealUSD, m.ExternalRealUSD, m.UnsplitRealUSD, m.UnsplitSteps, m.IncompleteChunks,
			m.SplitRuns, m.SplitSteps)
	}
	if a := c.Agents; len(a) != 1 || !nearV(a[0].RealUSD, m.CoveyRealUSD) {
		t.Errorf("agents %+v, want the one agent with covey's 46/3 points", a)
	}
	hours := map[time.Time]observability.ExternalMeterHour{}
	for _, h := range m.Hours {
		hours[h.Hour] = h
	}
	if h := hours[h3]; !nearV(h.UnattributedRealUSD, 15*pp/8) || h.UnattributedSteps != 1 {
		t.Errorf("hour h3 = %+v, want 15/8 points unattributed", h)
	}
	// The wire names.
	var raw struct {
		External struct {
			Meter map[string]any `json:"meter"`
		} `json:"external"`
	}
	if err := json.Unmarshal(rawGet(t, admin, "/api/v1/cost/cells?days=2&bucket=hour"), &raw); err != nil {
		t.Fatal(err)
	}
	creds, _ := raw.External.Meter["credentials"].([]any)
	seat, _ := creds[0].(map[string]any)
	for _, k := range []string{"unattributed_real_usd", "unattributed_points", "unattributed_steps", "unattributed_no_call_real_usd",
		"unattributed_no_call_points", "incomplete_chunks"} {
		if _, ok := raw.External.Meter[k]; !ok {
			t.Errorf("meter has no %s", k)
		}
	}
	for _, k := range []string{"points_per_list_usd", "plan_rates"} {
		if _, ok := seat[k]; !ok {
			t.Errorf("the seat has no %s: %v", k, seat)
		}
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
	up["reported_through"] = map[string]any{"laptop": time.Now()}
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

// #126 round 1, finding 1 and cost-controller's part A findings: a step is
// split over its own reading span, so an outside session that moved the
// meter while covey was idle is in the step of the next reading, not lost
// to covey's line beside it. Outside calls of a model the list does not
// know count at their reported_usd, and the ones with neither price are
// counted; covey's line of such a model counts at the engine's figure.
func TestExternalUsageSplitsAReadingOverItsSpan(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("spanne")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	base := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)
	admin.expect(http.MethodPut, fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/plan", rt.ID, ord),
		map[string]any{"plan_name": "Max", "amount": 200, "currency": "USD", "period": "month", "tax_basis": "net",
			"valid_from": base.Add(-48 * time.Hour)}, http.StatusOK)
	perPoint := 200.0 * 12 / 52 / 100
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: ord, SecretKey: "claude_code_oauth_token", Kind: "subscription"}

	// The meter is read at :10 (10 %), then not again until 2:10 (20 %),
	// during covey's next run.
	for _, r := range []struct {
		at  time.Time
		pct float64
	}{{base.Add(10 * time.Minute), 10}, {base.Add(2*time.Hour + 10*time.Minute), 20}} {
		if err := s.obs.AddUsageReading(ctx, s.orgID, observability.MeterReading{Credential: cred,
			Supported: true, WindowPercent: -1, WeekPercent: r.pct, FetchedAt: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	// $50 of Opus output outside covey between 1:00 and 1:49, in the hour
	// no reading falls in; an unknown model's call priced by Claude Code at
	// $0.50, and one with no price at all.
	var rows []map[string]any
	for i, min := range []int{60, 70, 80, 90, 109} {
		r := extRow(fmt.Sprintf("o%d", i), base.Add(time.Duration(min)*time.Minute), "claude-opus-5-5", 500_000)
		r["input"], r["cache_read"], r["cache_write_1h"] = 0, 0, 0
		rows = append(rows, r)
	}
	unknown := extRow("u-priced", base.Add(95*time.Minute), "claude-sonnet-5-7", 1000)
	unknown["reported_usd"] = 0.5
	rows = append(rows, unknown, extRow("u-bare", base.Add(100*time.Minute), "claude-sonnet-5-7", 1000))
	up := extUpload("operator: home", rows...)
	up["meter_credential"] = map[string]any{"secret_key": "claude_code_oauth_token", "secret_slot": 0}
	up["reported_through"] = map[string]any{"laptop": base.Add(2*time.Hour + 30*time.Minute)}
	admin.expect(http.MethodPost, externalPath, up, http.StatusOK)

	// covey's run at 2:05: $0.10 of Opus output, and $0.40 of a model the
	// list does not know, at the engine's figure.
	f := billingFixture{s: s, ctx: ctx, agent: agent.ID, org: s.orgID}
	f.line(t, cred, base.Add(2*time.Hour+5*time.Minute), 5_000)
	task := uuid.New()
	line := fmt.Sprintf(`{"type":"result","session_id":"s-%s","modelUsage":{"claude-sonnet-5-7":{"costUSD":0.4,"outputTokens":1000}}}`, task)
	if err := s.obs.RecordRuntime(ctx, s.orgID, agent.ID, &task, cred, json.RawMessage(line)); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE model_usage SET created_at = $1 WHERE task_id = $2`, base.Add(2*time.Hour+6*time.Minute), task)

	m := cellsExternal(t, admin, "days=1&bucket=hour").Meter
	r := 10 * perPoint
	lOut, lCov := 50.5, 0.5
	if len(m.Steps) != 1 || len(m.Hours) != 1 {
		t.Fatalf("steps %+v hours %+v, want the one step at 2:10", m.Steps, m.Hours)
	}
	st, h := m.Steps[0], m.Hours[0]
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !st.Split || !near(st.ExternalListUSD, lOut) || !near(st.CoveyListUSD, lCov) ||
		!near(*st.ExternalRealUSD, r*lOut/(lOut+lCov)) || !near(*st.CoveyRealUSD, r*lCov/(lOut+lCov)) {
		t.Errorf("the step = %+v, want L_out %v, L_cov %v and R %v split by them", st, lOut, lCov, r)
	}
	if !h.Hour.Equal(base.Add(2*time.Hour)) || !near(h.ExternalRealUSD, r*lOut/(lOut+lCov)) || !near(h.CoveyListUSD, lCov) {
		t.Errorf("hour = %+v, want the step's split at 2:00", h)
	}
	// The bare call's use is not in L_out: the step's chunk is incomplete
	// (F3c), sets no rate and has no unattributed part.
	if m.IncompleteChunks != 1 || m.UnattributedRealUSD != 0 {
		t.Errorf("incomplete chunks %d, unattributed %v; want 1 and 0", m.IncompleteChunks, m.UnattributedRealUSD)
	}
	if m.UnpricedCalls != 1 || m.UnpricedTokens != 2060 || m.Credentials[0].UnpricedCalls != 1 {
		t.Errorf("unpriced calls %d (%d tokens), seat %d; want the bare call, 2060 tokens", m.UnpricedCalls,
			m.UnpricedTokens, m.Credentials[0].UnpricedCalls)
	}
	// The cells keep the unknown model's calls without a list price.
	for _, src := range cellsExternal(t, admin, "days=1&bucket=hour").Sources {
		if src.UnpricedCalls != 2 || src.ListUSD == nil || !near(*src.ListUSD, 50) {
			t.Errorf("source %+v, want list $50 and 2 unpriced calls", src)
		}
	}
}

// #126 round 1, finding 2: counts are bounded per call, so the cost page's
// sums stay inside int64 — two rows at the bound in one cell read back with
// 200, not a 500 for the whole organisation. Finding 6: 10-minute buckets
// reach back two days at most, whatever ?days= says.
func TestExternalUsageBoundsAndTenMinuteReach(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	at := time.Now().Add(-time.Hour)
	big := func(id string) map[string]any {
		r := extRow(id, at, "claude-opus-5-5", 1_000_000_000)
		for _, k := range []string{"input", "cache_read", "cache_write_5m", "cache_write_1h"} {
			r[k] = 1_000_000_000
		}
		return r
	}
	admin.expect(http.MethodPost, externalPath, extUpload("op", big("b1"), big("b2")), http.StatusOK)
	ext := cellsExternal(t, admin, "days=1&bucket=hour")
	if len(ext.Cells) != 1 || ext.Cells[0].Tokens["input"] != 2_000_000_000 {
		t.Errorf("cells = %+v, want one cell of both rows", ext.Cells)
	}

	old := extRow("old", time.Now().Add(-72*time.Hour), "claude-opus-5-5", 10)
	admin.expect(http.MethodPost, externalPath, extUpload("op", old), http.StatusOK)
	if n := len(cellsExternal(t, admin, "days=365&bucket=hour").Cells); n != 2 {
		t.Errorf("hourly cells over a year: %d, want 2", n)
	}
	if c := cellsExternal(t, admin, "days=365&bucket=10min").Cells; len(c) != 1 || c[0].Period.Before(time.Now().Add(-49*time.Hour)) {
		t.Errorf("10-minute cells over a year: %+v, want the last two days only", c)
	}
}

// #126 round 1, finding 3: the bill at 10hour reads the hourly rows only.
// It says where they begin, and a 10-hour bucket that starts before them
// holds only some of its hours: not complete.
func TestBilledTenHourSaysWhereTheHourlyRowsBegin(t *testing.T) {
	f := newBillingFixture(t, "zehn-stunden")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	// Daily rows for ten days, hourly ones from 3 h into the 10-hour bucket
	// of two days ago.
	for d := 1; d <= 10; d++ {
		day := now.Truncate(24 * time.Hour).Add(-time.Duration(d) * 24 * time.Hour)
		f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
			amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,'1d',$2,$3,'apikey_z','claude-opus-5-5','tokens','output_tokens',2400,2400,$4,$4)`,
			f.s.orgID, day, day.Add(24*time.Hour), now)
	}
	// 10-hour buckets are cut from the Unix epoch.
	bin := time.Unix(now.Add(-48*time.Hour).Unix()/36000*36000, 0).UTC()
	from := bin.Add(3 * time.Hour)
	for at := from; at.Add(time.Hour).Before(now); at = at.Add(time.Hour) {
		f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
			amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,'1h',$2,$3,'apikey_z','claude-opus-5-5','tokens','output_tokens',100,100,$4,$4)`,
			f.s.orgID, at, at.Add(time.Hour), now)
	}
	series, err := f.s.obs.BilledSeries(f.ctx, f.s.orgID, "10hour", now.Add(-10*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if series.HourlyFrom == nil || !series.HourlyFrom.Equal(from) {
		t.Fatalf("hourly_from = %v, want %v", series.HourlyFrom, from)
	}
	if len(series.Points) < 2 || !series.Points[0].Bucket.Equal(bin) || series.Points[0].Complete || !series.Points[1].Complete {
		t.Errorf("points = %+v, want the first (from %v, 7 of 10 hours) not complete, the next complete", series.Points, bin)
	}
	var out struct {
		HourlyFrom *time.Time `json:"hourly_from"`
	}
	if err := json.Unmarshal(rawGet(t, admin, "/api/v1/cost/billed?days=10&bucket=10hour"), &out); err != nil {
		t.Fatal(err)
	}
	if out.HourlyFrom == nil || !out.HourlyFrom.Equal(from) {
		t.Errorf("/cost/billed hourly_from = %v, want %v", out.HourlyFrom, from)
	}
}
