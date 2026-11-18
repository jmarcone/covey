package integration

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// billedRow stores one billed_cost row of the given width on an API key.
func (f billingFixture) billedRow(t *testing.T, width string, start time.Time, apiKey, workspace string, cents int, fetched time.Time) {
	t.Helper()
	end := start.Add(time.Hour)
	if width == "1d" {
		end = start.Add(24 * time.Hour)
	}
	f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, workspace_id, model,
			cost_type, token_type, amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,'claude-opus-5-5','tokens','output_tokens',$7,$7,$8,$8)`,
		f.s.orgID, width, start, end, apiKey, workspace, cents, fetched)
}

// billedShown is GET /cost/billed as the page reads it: per credential the
// tile's billed $, and the sum of the chart's points from the window's start
// on (billedPerPeriod in web/src/pages/costFigures.ts).
func billedShown(t *testing.T, admin *apiClient, query string) (width string, tile, chart map[string]float64) {
	t.Helper()
	got := admin.expect(http.MethodGet, "/api/v1/cost/billed?"+query, nil, http.StatusOK)
	w, _ := got["window"].(map[string]any)
	if w == nil {
		t.Fatalf("GET /cost/billed?%s: no window in %v", query, got)
	}
	start, err := time.Parse(time.RFC3339Nano, w["start"].(string))
	if err != nil {
		t.Fatal(err)
	}
	usd := func(v any) float64 {
		f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
		if err != nil {
			t.Fatalf("not an amount: %v", v)
		}
		return f
	}
	tile, chart = map[string]float64{}, map[string]float64{}
	for _, x := range w["totals"].([]any) {
		b := x.(map[string]any)
		tile[b["secret_key"].(string)] += usd(b["billed_usd"])
	}
	for _, x := range got["points"].([]any) {
		p := x.(map[string]any)
		at, err := time.Parse(time.RFC3339Nano, p["bucket"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if key, ok := p["secret_key"].(string); ok && !at.Before(start) {
			chart[key] += usd(p["billed_usd"])
		}
	}
	return w["width"].(string), tile, chart
}

// The billed tile reads the same rows per day as the chart beside it. A
// mapping is chosen per row: a day's 1d row goes whole to the key's latest
// holder, its 1h rows split at the hand-over. So an hourly chart beside a
// tile over whole days — a key handed over three days ago, or yesterday
// while the daily re-read is fresher than the hourly one — showed the
// hand-over in the bars and not in the tile. The hour starting exactly at
// the hand-over belongs to the new holder only.
func TestBilledTileAndChartReadTheSameRows(t *testing.T) {
	f := newBillingFixture(t, "rechnung-gleiche-zeilen")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	old, yesterday := today.Add(-3*24*time.Hour), today.Add(-24*time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_b', 0, 'apikey_old', $2, $3), ($1, 'key_c', 0, 'apikey_old', $3, NULL),
			($1, 'key_d', 0, 'apikey_new', $2, $4), ($1, 'key_e', 0, 'apikey_new', $4, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), old.Add(10*time.Hour), yesterday.Add(10*time.Hour))
	// Three days ago: the hours as read when they were inside the hourly
	// reach, the day re-read since. Yesterday: inside the reach, the daily
	// re-read ran after this hour's hourly one.
	for h := 0; h < 24; h++ {
		f.billedRow(t, "1h", old.Add(time.Duration(h)*time.Hour), "apikey_old", "", 400, old.Add(30*time.Hour))
		f.billedRow(t, "1h", yesterday.Add(time.Duration(h)*time.Hour), "apikey_new", "", 100, now.Add(-2*time.Minute))
	}
	f.billedRow(t, "1d", old, "apikey_old", "", 9600, now.Add(-24*time.Hour))
	f.billedRow(t, "1d", yesterday, "apikey_new", "", 2400, now.Add(-time.Minute))

	width, tile, chart := billedShown(t, admin, "days=5&bucket=hour")
	if width != "1d" {
		t.Fatalf("5-day window: width %s, want 1d", width)
	}
	for key, want := range map[string]float64{"key_b": 40, "key_c": 56, "key_d": 10, "key_e": 14} {
		if math.Abs(tile[key]-want) > 1e-9 || math.Abs(chart[key]-want) > 1e-9 {
			t.Errorf("hourly view, %s: tile %v, chart %v — want %v in both", key, tile[key], chart[key], want)
		}
	}

	// A daily chart takes the freshest width per day, and so does its tile.
	_, tile, chart = billedShown(t, admin, "days=5&bucket=day")
	for _, key := range []string{"key_b", "key_c", "key_d", "key_e"} {
		if math.Abs(tile[key]-chart[key]) > 1e-9 {
			t.Errorf("daily view, %s: tile %v, chart %v — want the same", key, tile[key], chart[key])
		}
	}
	if tile["key_c"] != 96 || tile["key_e"] != 24 {
		t.Errorf("daily view: tile %v — want the re-read days whole with their latest holders", tile)
	}
}

// ?days=2 is a window of exactly two days: inside the hourly reach, so the
// tile is made of hours like the chart the page asks for. The handler took
// since a moment before the window's own now, which made it a hair older
// than 48 hours and the window daily.
func TestBilledTwoDaysIsHourly(t *testing.T) {
	f := newBillingFixture(t, "rechnung-zwei-tage")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	for i := 0; i < 5; i++ {
		got := admin.expect(http.MethodGet, "/api/v1/cost/billed?days=2&bucket=hour", nil, http.StatusOK)
		if w, _ := got["window"].(map[string]any); w == nil || w["width"] != "1h" {
			t.Fatalf("GET /cost/billed?days=2: window %v, want width 1h", got["window"])
		}
	}
}

// The reconciliation gives a bill row to the one mapping of its time, as the
// chart and the tile do: a value re-saved inside a day (the old mapping
// closed, a new one opened on the same key) is one row for that day, with
// the bill once and the lines of both mappings beside it — not the day's
// bill once per mapping.
func TestReconcileResavedDayBilledOnce(t *testing.T) {
	f := newBillingFixture(t, "abgleich-neu-gespeichert")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-2 * 24 * time.Hour)
	resaved := day.Add(10 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'anthropic_api_key', 0, 'apikey_r', $2, $3), ($1, 'anthropic_api_key', 0, 'apikey_r', $3, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), resaved)
	f.billedRow(t, "1d", day, "apikey_r", "", 4000, now)
	f.exec(t, `INSERT INTO billed_usage (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, output,
			data_refreshed_at, fetched_at)
		VALUES ($1,'1d',$2,$3,'apikey_r','claude-opus-5-5',2000000,$4,$4)`, f.s.orgID, day, day.Add(24*time.Hour), now)
	cred := observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "anthropic_api_key", Kind: daemon.CredAPIKey}
	f.line(t, cred, day.Add(8*time.Hour), 1000000)  // the first mapping's
	f.line(t, cred, day.Add(14*time.Hour), 1000000) // the second's

	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, day)
	if err != nil {
		t.Fatal(err)
	}
	var models []observability.ReconcileModel
	for _, d := range rec.Days {
		if d.Day == day.Format("2006-01-02") && d.SecretKey == "anthropic_api_key" {
			models = append(models, d.Models...)
		}
	}
	if len(models) != 1 {
		t.Fatalf("re-saved day: %d model rows %+v, want one", len(models), models)
	}
	m := models[0]
	if m.BilledUSD != "40.00000000" || m.ReportedUSD != "2.00000000" || m.ResidualUSD != "0.00000000" {
		t.Errorf("re-saved day: billed %s, reported %s, residual %s — want 40, 2 (both lines) and 0",
			m.BilledUSD, m.ReportedUSD, m.ResidualUSD)
	}
	for _, tok := range m.Tokens {
		if tok.Type == "output" && (tok.Billed != 2000000 || tok.Reported != 2000000) {
			t.Errorf("re-saved day, output tokens: %+v — want 2 000 000 billed and reported", tok)
		}
	}
}

// A mapping that ended where it began (closed by a write in the same
// instant) was never valid: a bucket around it belongs to the mapping that
// is. A mapping limited to a workspace holds only that workspace's rows of
// the key. A day bucket starting exactly where a mapping ended is not that
// mapping's. The same in the chart, the tile and the reconciliation.
func TestBilledMappingEdges(t *testing.T) {
	f := newBillingFixture(t, "rechnung-raender")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	yesterday, older := today.Add(-24*time.Hour), today.Add(-3*24*time.Hour)
	at := yesterday.Add(10 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, workspace_id, valid_from, valid_to)
		VALUES ($1, 'key_y', 0, 'apikey_z', '', $2, NULL), ($1, 'key_z', 0, 'apikey_z', '', $3, $3),
			($1, 'key_w', 0, 'apikey_w', 'wrkspc_a', $2, NULL),
			($1, 'key_ended', 0, 'apikey_e', '', $2, $4), ($1, 'key_from', 0, 'apikey_e', '', $4, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), at.Add(45*time.Minute), older)
	f.billedRow(t, "1h", at, "apikey_z", "", 700, now)
	f.billedRow(t, "1h", at, "apikey_w", "wrkspc_a", 300, now)
	f.billedRow(t, "1h", at, "apikey_w", "wrkspc_b", 500, now)
	f.billedRow(t, "1d", older, "apikey_e", "", 900, now)

	want := map[string]float64{"key_y": 7, "key_z": 0, "key_w": 3, "key_ended": 0, "key_from": 9}
	for _, q := range []string{"days=5&bucket=hour", "days=5&bucket=day"} {
		_, tile, chart := billedShown(t, admin, q)
		for key, usd := range want {
			if tile[key] != usd || chart[key] != usd {
				t.Errorf("%s, %s: tile %v, chart %v — want %v", q, key, tile[key], chart[key], usd)
			}
		}
	}

	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, now.Add(-5*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, d := range rec.Days {
		for _, m := range d.Models {
			usd, _ := strconv.ParseFloat(m.BilledUSD, 64)
			got[d.SecretKey] += usd
		}
	}
	for key, usd := range want {
		if got[key] != usd {
			t.Errorf("reconciliation, %s: billed %v, want %v", key, got[key], usd)
		}
	}
}
