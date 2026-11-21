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
// tile's billed $, and the sum of the chart's points and whole days from the
// window's start on (billedPerPeriod and billedWholeDays in
// web/src/pages/costFigures.ts).
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
	for _, x := range billedPoints(got) {
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

// billedPoints is every point of a GET /cost/billed answer: the buckets'
// and, at hour and 10hour, the days read whole.
func billedPoints(got map[string]any) []any {
	out, _ := got["points"].([]any)
	whole, _ := got["whole_days"].([]any)
	return append(append([]any{}, out...), whole...)
}

// The billed tile reads the same rows per day as the chart beside it, at
// every bucket. A mapping is chosen per row: a day's 1d row goes whole to the
// key's latest holder, its 1h rows split at the hand-over. So an hourly chart
// beside a tile over whole days — a key handed over three days ago, or
// yesterday while the daily re-read is fresher than the hourly one — showed
// the hand-over in the bars and not in the tile; and the daily view gave the
// day whole to the newer holder where the hourly one split it. The hour
// starting exactly at the hand-over belongs to the new holder only.
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
		f.billedRow(t, "1h", yesterday.Add(time.Duration(h)*time.Hour), "apikey_new", "", 100, now)
	}
	f.billedRow(t, "1d", old, "apikey_old", "", 9600, now.Add(-24*time.Hour))
	f.billedRow(t, "1d", yesterday, "apikey_new", "", 2400, now.Add(time.Second))

	for _, q := range []string{"days=5&bucket=hour", "days=5&bucket=day", "days=90&bucket=week"} {
		width, tile, chart := billedShown(t, admin, q)
		if width != "1d" {
			t.Fatalf("%s: width %s, want 1d", q, width)
		}
		for key, want := range map[string]float64{"key_b": 40, "key_c": 56, "key_d": 10, "key_e": 14} {
			if math.Abs(tile[key]-want) > 1e-9 || math.Abs(chart[key]-want) > 1e-9 {
				t.Errorf("%s, %s: tile %v, chart %v — want %v in both, the days split at the hand-overs",
					q, key, tile[key], chart[key], want)
			}
		}
	}
}

// The reconciliation, the tile and the chart read one row set whatever the
// bucket: a hand-over day by its hours, every other day by its freshest
// width. The reconciliation read every day by its hours wherever they were
// complete, while the daily and weekly views took the fresher 1d rows. The
// 1h rows are not read again once a day leaves the hourly reach, so for
// anything older than about three days the reconciliation showed hours the
// page did not, and a revision of the 35-day re-read ($40 → $38 here) never
// reached it.
func TestReconcileReadsTheRowsOfEveryView(t *testing.T) {
	f := newBillingFixture(t, "abgleich-jede-ansicht")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-4 * 24 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_b', 0, 'apikey_h', $2, $3), ($1, 'key_c', 0, 'apikey_h', $3, NULL),
			($1, 'key_s', 0, 'apikey_s', $2, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), day.Add(10*time.Hour))
	// The hours as read after the day ended, inside the hourly reach; the
	// day re-read since, both keys revised.
	read := day.Add(30 * time.Hour)
	for h := 0; h < 24; h++ {
		f.billedRow(t, "1h", day.Add(time.Duration(h)*time.Hour), "apikey_h", "", 400, read)
	}
	for h := 0; h < 10; h++ {
		f.billedRow(t, "1h", day.Add(time.Duration(h)*time.Hour), "apikey_s", "", 400, read)
	}
	f.billedRow(t, "1d", day, "apikey_h", "", 9000, now)
	f.billedRow(t, "1d", day, "apikey_s", "", 3800, now)

	want := map[string]float64{"key_b": 40, "key_c": 56, "key_s": 38}
	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, now.Add(-5*24*time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	reconciled := map[string]float64{}
	for _, d := range rec.Days {
		for _, m := range d.Models {
			usd, _ := strconv.ParseFloat(m.BilledUSD, 64)
			reconciled[d.SecretKey] += usd
		}
	}
	for _, q := range []string{"days=5&bucket=hour", "days=5&bucket=day", "days=90&bucket=week"} {
		_, tile, chart := billedShown(t, admin, q)
		for key, usd := range want {
			if math.Abs(tile[key]-usd) > 1e-9 || math.Abs(chart[key]-usd) > 1e-9 || math.Abs(reconciled[key]-usd) > 1e-9 {
				t.Errorf("%s, %s: tile %v, chart %v, reconciliation %v — want %v in all three",
					q, key, tile[key], chart[key], reconciled[key], usd)
			}
		}
	}
}

// The bars of a response without ?bucket= (a daily chart) and its window (an
// hourly one, for ?days=2) read the same rows: the bucket shapes the bars
// only. Without it the chart took the day's fresher 1d row whole to the
// newer holder while the tile, and the hourly chart, split the day.
func TestBilledBarsWithoutABucketSplitLikeTheTile(t *testing.T) {
	f := newBillingFixture(t, "rechnung-ohne-bucket")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	yesterday := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_d', 0, 'apikey_n', $2, $3), ($1, 'key_e', 0, 'apikey_n', $3, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), yesterday.Add(10*time.Hour))
	for h := 0; h < 24; h++ {
		f.billedRow(t, "1h", yesterday.Add(time.Duration(h)*time.Hour), "apikey_n", "", 100, now)
	}
	f.billedRow(t, "1d", yesterday, "apikey_n", "", 2400, now.Add(time.Second))

	perDay := func(q string) (map[string]float64, any) {
		got := admin.expect(http.MethodGet, "/api/v1/cost/billed?"+q, nil, http.StatusOK)
		out := map[string]float64{}
		for _, x := range billedPoints(got) {
			p := x.(map[string]any)
			at, err := time.Parse(time.RFC3339Nano, p["bucket"].(string))
			if err != nil {
				t.Fatal(err)
			}
			key, ok := p["secret_key"].(string)
			if !ok || !at.Truncate(24*time.Hour).Equal(yesterday) {
				continue
			}
			usd, _ := strconv.ParseFloat(fmt.Sprint(p["billed_usd"]), 64)
			out[key] += usd
		}
		w := got["window"].(map[string]any)
		return out, []any{w["start"], w["width"], fmt.Sprint(w["totals"])}
	}
	hourly, hourlyWindow := perDay("days=2&bucket=hour")
	daily, dailyWindow := perDay("days=2")
	for key, want := range map[string]float64{"key_d": 10, "key_e": 14} {
		if hourly[key] != want || daily[key] != want {
			t.Errorf("yesterday, %s: hourly bars %v, bars without a bucket %v — want %v in both", key, hourly[key], daily[key], want)
		}
	}
	if fmt.Sprint(hourlyWindow) != fmt.Sprint(dailyWindow) {
		t.Errorf("window with bucket=hour %v, without a bucket %v — want the same", hourlyWindow, dailyWindow)
	}
}

// A hand-over day is read by its hours only where they hold the whole day,
// or at least every whole hour its 1d row does. The hours of a day are
// complete only if an hourly read landed after the day ended and before it
// left the hourly reach; with the poller down over that stretch the day
// keeps the hours its last hourly read saw, and only the daily re-read has
// it whole. Taking those hours anyway made the tile follow an under-counted
// chart, and the bill change with the Hour/Day switch.
func TestBilledHourlyRuleSkipsIncompleteHours(t *testing.T) {
	f := newBillingFixture(t, "rechnung-unvollstaendige-stunden")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-4 * 24 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_w', 0, 'apikey_x', $2, $3), ($1, 'key_x', 0, 'apikey_x', $3, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), day.Add(5*time.Hour))
	// Handed over at 05:00. The last hourly read of the day landed at 10:00;
	// the daily re-read of yesterday has the whole day.
	for h := 0; h < 10; h++ {
		f.billedRow(t, "1h", day.Add(time.Duration(h)*time.Hour), "apikey_x", "", 400, day.Add(10*time.Hour))
	}
	f.billedRow(t, "1d", day, "apikey_x", "", 9600, now.Add(-24*time.Hour))

	for _, q := range []string{"days=7&bucket=hour", "days=7&bucket=day"} {
		_, tile, chart := billedShown(t, admin, q)
		if tile["key_x"] != 96 || chart["key_x"] != 96 || tile["key_w"] != 0 || chart["key_w"] != 0 {
			t.Errorf("%s: tile %v, chart %v — want the re-read day whole (96) in both, with the newer holder", q, tile, chart)
		}
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

	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, day, time.Time{})
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

// A credential mapped to one key, then re-saved into another workspace of it
// on the same day, is two reconciliation entries, one per workspace, each
// with its own bill — not one entry under the first workspace with the model
// twice.
func TestReconcileWorkspaceSplitsTheDay(t *testing.T) {
	f := newBillingFixture(t, "abgleich-arbeitsbereiche")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-2 * 24 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, workspace_id, valid_from, valid_to)
		VALUES ($1, 'key_v', 0, 'apikey_v', 'wrkspc_a', $2, $3), ($1, 'key_v', 0, 'apikey_v', 'wrkspc_b', $3, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), day.Add(10*time.Hour))
	f.billedRow(t, "1h", day.Add(8*time.Hour), "apikey_v", "wrkspc_a", 200, now)
	f.billedRow(t, "1h", day.Add(14*time.Hour), "apikey_v", "wrkspc_b", 500, now)

	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, day, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range rec.Days {
		if d.SecretKey != "key_v" {
			continue
		}
		if len(d.Models) != 1 {
			t.Errorf("%s: %d model rows, want one", d.WorkspaceID, len(d.Models))
			continue
		}
		got[d.WorkspaceID] = d.Models[0].BilledUSD
	}
	if len(got) != 2 || got["wrkspc_a"] != "2.00000000" || got["wrkspc_b"] != "5.00000000" {
		t.Errorf("entries %v — want wrkspc_a 2 and wrkspc_b 5", got)
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
	f.line(t, observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "key_w", Kind: daemon.CredAPIKey},
		at.Add(10*time.Minute), 1000000)

	want := map[string]float64{"key_y": 7, "key_z": 0, "key_w": 3, "key_ended": 0, "key_from": 9}
	for _, q := range []string{"days=5&bucket=hour", "days=5&bucket=day"} {
		_, tile, chart := billedShown(t, admin, q)
		for key, usd := range want {
			if tile[key] != usd || chart[key] != usd {
				t.Errorf("%s, %s: tile %v, chart %v — want %v", q, key, tile[key], chart[key], usd)
			}
		}
		if _, listed := tile["key_z"]; listed {
			t.Errorf("%s: the never-valid mapping key_z is listed in the tile", q)
		}
	}

	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, now.Add(-5*24*time.Hour), time.Time{})
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
	// The workspace mapping's line and its bill are one entry, not a
	// lines-only row beside a bill-only one.
	var entries []observability.ReconcileDay
	for _, d := range rec.Days {
		if d.SecretKey == "key_w" {
			entries = append(entries, d)
		}
	}
	if len(entries) != 1 || len(entries[0].Models) != 1 || entries[0].WorkspaceID != "wrkspc_a" {
		t.Fatalf("reconciliation, key_w: %+v — want one entry in wrkspc_a with one model", entries)
	}
	if m := entries[0].Models[0]; m.BilledUSD != "3.00000000" || !m.HasReported || m.ReportedUSD == "0.00000000" {
		t.Errorf("reconciliation, key_w: billed %s, reported %s — want 3 and the line beside it", m.BilledUSD, m.ReportedUSD)
	}
}

// The reconciliation reads a hand-over day by its hours, as the tile does at
// every bucket: its lines are split at the mapping's exact interval, so its
// bill has to be too. Read by its fresher 1d row, the day went whole to the
// newer holder: the older one billed nothing against its lines, the newer one
// the whole day against its share, each residual off by the older holder's
// part although the bill matched the lines to the cent.
func TestReconcileHandOverDaySplitsLikeTheTile(t *testing.T) {
	f := newBillingFixture(t, "abgleich-uebergabe")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-2 * 24 * time.Hour)
	handover := day.Add(10 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_b', 0, 'apikey_h', $2, $3), ($1, 'key_c', 0, 'apikey_h', $3, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), handover)
	// $4 an hour, 200 000 output tokens an hour; the hours read complete
	// after the day ended, the day re-read since.
	read := day.Add(30 * time.Hour)
	for h := 0; h < 24; h++ {
		at := day.Add(time.Duration(h) * time.Hour)
		f.billedRow(t, "1h", at, "apikey_h", "", 400, read)
		f.exec(t, `INSERT INTO billed_usage (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, output,
				data_refreshed_at, fetched_at)
			VALUES ($1,'1h',$2,$3,'apikey_h','claude-opus-5-5',200000,$4,$4)`, f.s.orgID, at, at.Add(time.Hour), read)
	}
	f.billedRow(t, "1d", day, "apikey_h", "", 9600, now)
	f.exec(t, `INSERT INTO billed_usage (org_id, bucket_width, bucket_start, bucket_end, api_key_id, model, output,
			data_refreshed_at, fetched_at)
		VALUES ($1,'1d',$2,$3,'apikey_h','claude-opus-5-5',4800000,$4,$4)`, f.s.orgID, day, day.Add(24*time.Hour), now)
	// Opus output at $20 per million: the lines are $40 before the
	// hand-over and $56 after it, exactly the bill on each side.
	f.line(t, observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "key_b", Kind: daemon.CredAPIKey},
		day.Add(8*time.Hour), 2000000)
	f.line(t, observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "key_c", Kind: daemon.CredAPIKey},
		day.Add(14*time.Hour), 2800000)

	_, tile, _ := billedShown(t, admin, "days=5&bucket=hour")
	_, daily, _ := billedShown(t, admin, "days=5&bucket=day")
	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, day, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		usd    float64
		tokens int64
	}{"key_b": {40, 2000000}, "key_c": {56, 2800000}}
	for key, w := range want {
		if tile[key] != w.usd || daily[key] != w.usd {
			t.Errorf("tile, %s: hourly view %v, daily view %v — want %v in both", key, tile[key], daily[key], w.usd)
		}
		var models []observability.ReconcileModel
		for _, d := range rec.Days {
			if d.Day == day.Format("2006-01-02") && d.SecretKey == key {
				models = append(models, d.Models...)
			}
		}
		if len(models) != 1 {
			t.Errorf("reconciliation, %s: %d model rows %+v, want one", key, len(models), models)
			continue
		}
		m := models[0]
		billed, _ := strconv.ParseFloat(m.BilledUSD, 64)
		if billed != tile[key] || m.ResidualUSD != "0.00000000" || m.ResidualLabel != "match" {
			t.Errorf("reconciliation, %s: billed %s (tile %v), residual %s %s — want the tile's figure and a match",
				key, m.BilledUSD, tile[key], m.ResidualUSD, m.ResidualLabel)
		}
		for _, tok := range m.Tokens {
			if tok.Type == "output" && (tok.Billed != w.tokens || tok.Reported != w.tokens) {
				t.Errorf("reconciliation, %s, output tokens: %+v — want %d billed and reported", key, tok, w.tokens)
			}
		}
	}
}

// On a day still being read, the daily re-read lands seconds after the
// hourly one in the same sync, so its data_refreshed_at can be a few seconds
// later. The hours are complete up to the last whole hour the 1d row knows
// of, not up to its exact mark: measured against the mark, the day counted
// as incomplete, went to its fresher 1d row, and a hand-over that day showed
// as one midnight point given whole to the newer holder. The rule does not
// look at the clock, so a past day read as it stood at 13:30 stands in for
// today.
func TestBilledHourlyRuleTruncatesTheDailyMark(t *testing.T) {
	f := newBillingFixture(t, "rechnung-tagesstand-stunde")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-2 * 24 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_f', 0, 'apikey_t', $2, $3), ($1, 'key_g', 0, 'apikey_t', $3, NULL)`,
		f.s.orgID, now.Add(-10*24*time.Hour), day.Add(7*time.Hour))
	hourly := day.Add(13*time.Hour + 30*time.Minute)
	for h := 0; h < 14; h++ {
		f.billedRow(t, "1h", day.Add(time.Duration(h)*time.Hour), "apikey_t", "", 100, hourly)
	}
	f.billedRow(t, "1d", day, "apikey_t", "", 1400, hourly.Add(20*time.Second))

	_, tile, chart := billedShown(t, admin, "days=5&bucket=hour")
	for key, want := range map[string]float64{"key_f": 7, "key_g": 7} {
		if tile[key] != want || chart[key] != want {
			t.Errorf("%s: tile %v, chart %v — want %v in both, the day split at the hand-over", key, tile[key], chart[key], want)
		}
	}
}

// An hourly tile starting mid-day decides that day's width on the whole day,
// as the chart beside it does from the day's midnight. Where the day is read
// from its 1d row, that row starts before the tile and counts in neither the
// tile nor the chart's points from its start on; the tile then starts at the
// next midnight, so its bill and its lines cover the same span. The lines
// used to count from the hour the bill left out. Where the hours are the
// freshest, the tile starts at its first whole hour.
func TestBilledHourlyTileStartsWhereItsBillDoes(t *testing.T) {
	f := newBillingFixture(t, "rechnung-erster-tag")
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	day := today.Add(-24 * time.Hour)
	since := day.Add(12 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from)
		VALUES ($1, 'key_p', 0, 'apikey_p', $2)`, f.s.orgID, now.Add(-10*24*time.Hour))
	// The last hourly read of the day landed at 20:30; the day re-read since.
	for h := 0; h <= 20; h++ {
		f.billedRow(t, "1h", day.Add(time.Duration(h)*time.Hour), "apikey_p", "", 100, day.Add(20*time.Hour+30*time.Minute))
	}
	f.billedRow(t, "1d", day, "apikey_p", "", 3000, now)
	// A line after the tile's first whole hour, before the next midnight.
	f.line(t, observability.CredentialRef{RuntimeID: uuid.New(), SecretKey: "key_p", Kind: daemon.CredAPIKey},
		day.Add(15*time.Hour), 1000000)

	shown := func() (observability.BilledWindow, float64, float64) {
		t.Helper()
		w, err := f.s.obs.BilledWindowFor(f.ctx, f.s.orgID, since, time.Time{}, now)
		if err != nil {
			t.Fatal(err)
		}
		tile := 0.0
		for _, b := range w.Totals {
			if b.SecretKey == "key_p" {
				tile, _ = strconv.ParseFloat(b.BilledUSD, 64)
			}
		}
		series, err := f.s.obs.BilledSeries(f.ctx, f.s.orgID, "hour", since, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		chart := 0.0
		for _, p := range append(series.Points, series.WholeDays...) {
			if p.SecretKey == nil || *p.SecretKey != "key_p" || p.Bucket.Before(w.Start) {
				continue
			}
			usd, _ := strconv.ParseFloat(p.BilledUSD, 64)
			chart += usd
		}
		return w, tile, chart
	}
	reported := func(w observability.BilledWindow) float64 {
		for _, b := range w.Totals {
			if b.SecretKey == "key_p" {
				return b.ReportedUSD
			}
		}
		return 0
	}

	w, tile, chart := shown()
	if w.Width != "1h" || !w.Start.Equal(today) {
		t.Fatalf("window from %v, day read whole: width %s, start %v — want an hourly window from %v", since, w.Width, w.Start, today)
	}
	if tile != 0 || chart != 0 || reported(w) != 0 {
		t.Errorf("window from %v: tile %v, chart %v, reported %v — want nothing before it in any", w.Start, tile, chart, reported(w))
	}

	// An hourly read after the daily one: the hours are the freshest.
	f.exec(t, `UPDATE billed_cost SET fetched_at = $2 WHERE org_id = $1 AND bucket_width = '1h'`, f.s.orgID, now.Add(time.Second))
	w, tile, chart = shown()
	if !w.Start.Equal(since) {
		t.Fatalf("day read by its hours: start %v, want %v", w.Start, since)
	}
	if tile != 9 || chart != 9 || reported(w) != 1 {
		t.Errorf("window from %v: tile %v, chart %v, reported %v — want 9 (12:00–20:59) in both and the line beside them",
			w.Start, tile, chart, reported(w))
	}
}

// A window asked for as [from, to) is read to the next UTC midnight after to,
// and the answer says so; an open window has no until.
func TestReconcileNamesWhereItReadsTo(t *testing.T) {
	f := newBillingFixture(t, "abgleich-bis")
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-3 * 24 * time.Hour)
	rec, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, day.Add(2*time.Hour), day.Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if want := day.Add(24 * time.Hour); rec.Until == nil || !rec.Until.Equal(want) {
		t.Fatalf("until = %v, want %v (the midnight after to)", rec.Until, want)
	}
	if !rec.Since.Equal(day) {
		t.Fatalf("since = %v, want the midnight before from, %v", rec.Since, day)
	}
	open, err := f.s.obs.Reconcile(f.ctx, f.s.orgID, day, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if open.Until != nil {
		t.Fatalf("an open window names until %v, want none", open.Until)
	}
}
