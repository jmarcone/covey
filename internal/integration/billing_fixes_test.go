package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
	"covey/internal/secrets"
)

// billingFixture is a stack with one mapped API-key credential and a way to
// put lines on it at a given time.
type billingFixture struct {
	s     *stack
	ctx   context.Context
	agent uuid.UUID
	org   uuid.UUID
}

func (f billingFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.s.pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

// line files a Claude Code result line on the credential: costUSD 1, Opus
// output tokens at $20 per million.
func (f billingFixture) line(t *testing.T, cred observability.CredentialRef, at time.Time, outputTokens int) {
	t.Helper()
	task := uuid.New()
	l := fmt.Sprintf(`{"type":"result","session_id":"s-%s","modelUsage":{"claude-opus-5-5":{"costUSD":1,"outputTokens":%d}}}`, task, outputTokens)
	if err := f.s.obs.RecordRuntime(f.ctx, f.org, f.agent, &task, cred, json.RawMessage(l)); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE model_usage SET created_at = $1 WHERE task_id = $2`, at, task)
}

func newBillingFixture(t *testing.T, slug string) billingFixture {
	s := newStack(t)
	a := s.newSupportAgent(slug)
	return billingFixture{s: s, ctx: context.Background(), agent: a.ID, org: a.OrgID}
}

// The bill is complete only up to its data_refreshed_at, so the reported and
// recomputed side of the billed comparison ends there too: a line after it is
// not compared with a bill that cannot contain it yet. Before any read of the
// bill has landed nothing is compared.
func TestBillingWindowComparedUntilDataRefreshed(t *testing.T) {
	f := newBillingFixture(t, "rechnung-stand")
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from)
		VALUES ($1, 'anthropic_api_key', 0, 'apikey_stand', $2)`, f.s.orgID, now.Add(-10*24*time.Hour))
	cred := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "anthropic_api_key", Kind: daemon.CredAPIKey}
	f.line(t, cred, now.Add(-12*time.Hour), 50000) // before data_refreshed_at: $1
	f.line(t, cred, now.Add(-2*time.Hour), 125000) // after it: $2.50, not yet billable

	w, err := f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, since, now)
	if err != nil {
		t.Fatal(err)
	}
	if !w.ComparedUntil.Equal(w.Start) || len(w.Totals) != 1 || w.Totals[0].ReportedUSD != 0 || w.Totals[0].ListUSD != 0 {
		t.Errorf("before any bill landed: compared until %v (start %v), totals %+v — want nothing compared",
			w.ComparedUntil, w.Start, w.Totals)
	}

	refreshed := now.Add(-6 * time.Hour).Truncate(time.Microsecond)
	at := now.Add(-12 * time.Hour).Truncate(time.Hour)
	f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
		amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
		VALUES ($1,'1h',$2,$3,'apikey_stand','claude-opus-5-5','tokens','output_tokens',100,100,$4,$5)`,
		f.s.orgID, at, at.Add(time.Hour), refreshed, now.Add(-time.Hour))
	w, err = f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, since, now)
	if err != nil {
		t.Fatal(err)
	}
	if !w.ComparedUntil.Equal(refreshed) || !w.End.Equal(now) {
		t.Errorf("compared until %v, end %v — want the bill's data_refreshed_at %v and now", w.ComparedUntil, w.End, refreshed)
	}
	if b := w.Totals[0]; b.BilledUSD != "1.00000000" || math.Abs(b.ReportedUSD-1) > 1e-9 || math.Abs(b.ListUSD-1) > 1e-9 {
		t.Errorf("totals %+v — want billed 1 beside reported 1 and recomputed 1 (the line after data_refreshed_at left out)", b)
	}
}

// Every billed figure sums amount_cents as US cents: a read with a row in
// another currency is refused whole — the sync fails and says why, and
// nothing of it is stored — rather than summed as if it were dollars.
func TestBillingRefusesAnotherCurrency(t *testing.T) {
	ctx := context.Background()
	fake := &fakeAnalytics{currency: "EUR", refreshed: time.Now().UTC().Add(-time.Hour)}
	fake.hours = []billedHour{{start: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour), apiKey: "apikey_eur",
		model: opus, tier: "standard", costs: map[string][2]string{"output_tokens": {"500.000000", "500.000000"}}, out: 250}}
	s := billingStack(t, fake)
	if err := s.secrets.Put(ctx, s.orgID, secrets.AdminKey, fakeAdminKey); err != nil {
		t.Fatal(err)
	}
	err := s.orch.SyncBillingOrg(ctx, s.orgID, true)
	if err == nil || !strings.Contains(err.Error(), "EUR") {
		t.Fatalf("sync of a bill in EUR: %v, want an error naming the currency", err)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM billed_cost WHERE org_id = $1)
		+ (SELECT count(*) FROM billed_usage WHERE org_id = $1)`, s.orgID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d billed rows stored from a read in EUR, want none", rows)
	}
	st, err := s.obs.BillingSync(ctx, s.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastStatus != "failed" || !strings.Contains(st.LastError, "EUR") {
		t.Errorf("billing sync state = %+v, want failed naming EUR", st)
	}

	// Lower case is the same currency, not another one.
	fake.mu.Lock()
	fake.currency = "usd"
	fake.mu.Unlock()
	if err := s.orch.SyncBillingOrg(ctx, s.orgID, true); err != nil {
		t.Fatalf("sync of a bill in usd: %v", err)
	}
	var cur string
	if err := s.pool.QueryRow(ctx, `SELECT DISTINCT currency FROM billed_cost WHERE org_id = $1`, s.orgID).Scan(&cur); err != nil || cur != "USD" {
		t.Errorf("stored currency %q (%v), want USD", cur, err)
	}
}

// One API key is billed to one credential at a time. A second credential
// mapped to a key another one holds over any part of the same interval is
// refused — its bill would count twice — whether that mapping is open or was
// closed by a rotation after the second value was written. A value written
// after the first mapping ended may take the key.
func TestBillingKeyMappedToOneCredential(t *testing.T) {
	f := newBillingFixture(t, "ein-schluessel")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	rt, err := f.s.runtimes.Create(f.ctx, f.s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	one := f.s.seat(t, rt.ID, daemon.CredAPIKey, "anthropic_api_key", "sk-ant-api03-billing-one", "Eins")
	two := f.s.seat(t, rt.ID, daemon.CredAPIKey, "anthropic_api_key", "sk-ant-api03-billing-two", "Zwei")
	f.exec(t, `UPDATE secrets SET updated_at = $2 WHERE org_id = $1 AND key = 'anthropic_api_key'`,
		f.s.orgID, time.Now().Add(-5*24*time.Hour))
	place := func(ord int) string {
		return fmt.Sprintf("/api/v1/runtime-instances/%s/credentials/%d/billing-key", rt.ID, ord)
	}
	refused := func(ord int, key string) {
		t.Helper()
		resp := admin.do(http.MethodPatch, place(ord), map[string]string{"api_key_id": key})
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "already mapped") {
			t.Errorf("mapping ord %d to %s: HTTP %d %s, want 400 already mapped", ord, key, resp.StatusCode, body)
		}
	}

	admin.expect(http.MethodPatch, place(one), map[string]string{"api_key_id": "apikey_dup"}, http.StatusOK)
	refused(two, "apikey_dup")
	admin.expect(http.MethodPatch, place(two), map[string]string{"api_key_id": "apikey_own"}, http.StatusOK)
	// A correction of the same credential's mapping is no conflict.
	admin.expect(http.MethodPatch, place(one), map[string]string{"api_key_id": "apikey_dup", "workspace_id": "wrkspc_a"}, http.StatusOK)

	// The first value is rotated: its mapping ends now. The second value was
	// written before that, so it was not the only one with the key.
	if err := f.s.secrets.Replace(f.ctx, secrets.Ref{OrgID: f.s.orgID, Key: "anthropic_api_key", Slot: 0},
		"sk-ant-api03-billing-rotated", secrets.Lifetime{}); err != nil {
		t.Fatal(err)
	}
	refused(two, "apikey_dup")
	// A value written after the old mapping ended may take the key.
	three := f.s.seat(t, rt.ID, daemon.CredAPIKey, "anthropic_api_key", "sk-ant-api03-billing-three", "Drei")
	admin.expect(http.MethodPatch, place(three), map[string]string{"api_key_id": "apikey_dup"}, http.StatusOK)

	// The database holds the open case on its own.
	if _, err := f.s.pool.Exec(f.ctx, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from)
		VALUES ($1, 'other_key', 0, 'apikey_dup', now())`, f.s.orgID); err == nil {
		t.Error("a second open mapping of apikey_dup was stored")
	}
}

// A bill bucket belongs to the credential the API key was mapped to during
// that bucket, not to whichever mapping is open now: before a key changed
// hands it is the old credential's, between two mappings nobody's.
func TestBilledSeriesMappingAtBucketTime(t *testing.T) {
	f := newBillingFixture(t, "rechnung-zeitpunkt")
	now := time.Now().UTC()
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_old', 0, 'apikey_moved', $2, $3), ($1, 'key_new', 0, 'apikey_moved', $4, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), now.Add(-3*24*time.Hour), now.Add(-2*24*time.Hour))
	hour := func(at time.Time) time.Time { return at.Truncate(time.Hour) }
	for _, at := range []time.Time{hour(now.Add(-5 * 24 * time.Hour)), hour(now.Add(-60 * time.Hour)), hour(now.Add(-24 * time.Hour))} {
		f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
			amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,'1h',$2,$3,'apikey_moved','claude-opus-5-5','tokens','output_tokens',100,100,$4,$4)`,
			f.s.orgID, at, at.Add(time.Hour), now)
	}
	series, err := f.s.obs.BilledSeries(f.ctx, f.s.orgID, "hour", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[time.Time]string{}
	for _, p := range series.Points {
		got[p.Bucket.UTC()] = "<none>"
		if p.SecretKey != nil {
			got[p.Bucket.UTC()] = *p.SecretKey
		}
	}
	for at, want := range map[time.Time]string{hour(now.Add(-5 * 24 * time.Hour)): "key_old",
		hour(now.Add(-60 * time.Hour)): "<none>", hour(now.Add(-24 * time.Hour)): "key_new"} {
		if got[at] != want {
			t.Errorf("bucket %v billed to %q, want %q", at, got[at], want)
		}
	}
}

// The billed tile gives a bucket to the same one mapping the billed series
// does — the one valid at the bucket's time, the latest when one ended inside
// it — so the tile and the sum of the chart agree. A value re-saved inside a
// day (the old mapping closed, a new one opened on the same key) does not bill
// that day twice to the credential, and a key that changed hands inside a day
// bills it to the new holder only, not to both.
func TestBilledWindowOneMappingPerBucket(t *testing.T) {
	f := newBillingFixture(t, "rechnung-eine-zuordnung")
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	resaved, handed := today.Add(-3*24*time.Hour), today.Add(-2*24*time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_a', 0, 'apikey_a', $2, $3), ($1, 'key_a', 0, 'apikey_a', $3, NULL),
			($1, 'key_b', 0, 'apikey_x', $2, $4), ($1, 'key_c', 0, 'apikey_x', $4, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), resaved.Add(10*time.Hour), handed.Add(10*time.Hour))
	for _, r := range []struct {
		day   time.Time
		key   string
		cents int
	}{{resaved, "apikey_a", 4000}, {handed, "apikey_x", 1000}} {
		f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, cost_type, token_type,
			amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,'1d',$2,$3,$4,'claude-opus-5-5','tokens','output_tokens',$5,$5,$6,$6)`,
			f.s.orgID, r.day, r.day.Add(24*time.Hour), r.key, r.cents, now)
	}

	since := now.Add(-5 * 24 * time.Hour)
	w, err := f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, since, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != "1d" {
		t.Fatalf("window width %q, want 1d", w.Width)
	}
	type shown struct {
		hasBilled bool
		usd       string
	}
	tile := map[string]shown{}
	for _, b := range w.Totals {
		tile[b.SecretKey] = shown{b.HasBilled, b.BilledUSD}
	}
	for key, want := range map[string]shown{"key_a": {true, "40.00000000"}, "key_b": {false, "0.00000000"},
		"key_c": {true, "10.00000000"}} {
		if tile[key] != want {
			t.Errorf("tile of %s: %+v, want %+v", key, tile[key], want)
		}
	}

	series, err := f.s.obs.BilledSeries(f.ctx, f.s.orgID, "day", since)
	if err != nil {
		t.Fatal(err)
	}
	chart := map[string]float64{}
	for _, p := range series.Points {
		if p.SecretKey == nil || p.Bucket.Before(w.Start) {
			continue
		}
		var usd float64
		fmt.Sscan(p.BilledUSD, &usd)
		chart[*p.SecretKey] += usd
	}
	for _, b := range w.Totals {
		var usd float64
		fmt.Sscan(b.BilledUSD, &usd)
		if math.Abs(usd-chart[b.SecretKey]) > 1e-9 {
			t.Errorf("%s: tile %s, chart sums to %v — want the same", b.SecretKey, b.BilledUSD, chart[b.SecretKey])
		}
	}
}
