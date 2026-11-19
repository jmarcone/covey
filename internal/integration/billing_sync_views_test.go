package integration

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"testing"
	"time"

	"covey/internal/secrets"
)

// usd reads an amount the API gives as a decimal string.
func usd(t *testing.T, v any) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	if err != nil {
		t.Fatalf("not an amount: %v", v)
	}
	return f
}

// billedViews is every view of the bill per credential: the hourly chart's
// points (hour × credential), the daily chart, the reconciliation (money and
// output tokens) per UTC day × credential, the weekly chart per credential,
// the key list per API key.
type billedViews struct {
	hourly   map[string]float64 // "2006-01-02T15 key" from ?days=2&bucket=hour
	daily    map[string]float64 // "2006-01-02 key"
	rec      map[string]float64
	recOut   map[string]int64
	weekly   map[string]float64 // key
	keysList map[string]float64 // api_key_id
}

// readBilledViews reads every view over the endpoints. Each call to
// billedShown also checks that the tile sums the chart's points from the
// window's start on, per credential.
func readBilledViews(t *testing.T, admin *apiClient, label string) billedViews {
	t.Helper()
	v := billedViews{hourly: map[string]float64{}, daily: map[string]float64{}, rec: map[string]float64{},
		recOut: map[string]int64{}, weekly: map[string]float64{}, keysList: map[string]float64{}}
	points := func(q, format string, into map[string]float64) {
		got := admin.expect(http.MethodGet, "/api/v1/cost/billed?"+q, nil, http.StatusOK)
		// A day read whole is keyed at its midnight: in the hourly chart
		// it shows as a midnight hour no bill row has.
		for _, x := range billedPoints(got) {
			p := x.(map[string]any)
			at, err := time.Parse(time.RFC3339Nano, p["bucket"].(string))
			if err != nil {
				t.Fatal(err)
			}
			key, _ := p["secret_key"].(string)
			into[at.UTC().Format(format)+" "+key] += usd(t, p["billed_usd"])
		}
		_, tile, chart := billedShown(t, admin, q)
		for key := range union(tile, chart) {
			if math.Abs(tile[key]-chart[key]) > 1e-9 {
				t.Errorf("%s, %s, %s: tile %v, chart from the window's start %v — want the same", label, q, key, tile[key], chart[key])
			}
		}
	}
	points("days=2&bucket=hour", "2006-01-02T15", v.hourly)
	points("days=5&bucket=day", "2006-01-02", v.daily)
	week := map[string]float64{}
	points("days=90&bucket=week", "2006-01-02", week)
	for k, usd := range week {
		v.weekly[k[len("2006-01-02 "):]] += usd
	}
	rec := admin.expect(http.MethodGet, "/api/v1/cost/reconcile?days=5", nil, http.StatusOK)
	for _, x := range rec["days"].([]any) {
		d := x.(map[string]any)
		k := d["day"].(string) + " " + d["secret_key"].(string)
		for _, y := range d["models"].([]any) {
			m := y.(map[string]any)
			v.rec[k] += usd(t, m["billed_usd"])
			for _, z := range m["tokens"].([]any) {
				if tok := z.(map[string]any); tok["type"] == "output" {
					v.recOut[k] += int64(tok["billed"].(float64))
				}
			}
		}
	}
	keys := admin.expect(http.MethodGet, "/api/v1/cost/billing-keys", nil, http.StatusOK)
	for _, x := range keys["keys"].([]any) {
		k := x.(map[string]any)
		v.keysList[k["api_key_id"].(string)] += usd(t, k["billed_usd"])
	}
	return v
}

func union(a, b map[string]float64) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// sameAmounts reports every key where got and want differ.
func sameAmounts(t *testing.T, label string, got, want map[string]float64) {
	t.Helper()
	keys := []string{}
	for k := range union(got, want) {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if math.Abs(got[k]-want[k]) > 1e-9 {
			t.Errorf("%s, %s: %v, want %v", label, k, got[k], want[k])
		}
	}
}

// billedWindow is GET /cost/billed's window: its start and width.
func billedWindow(t *testing.T, admin *apiClient, q string) (time.Time, string) {
	t.Helper()
	w := admin.expect(http.MethodGet, "/api/v1/cost/billed?"+q, nil, http.StatusOK)["window"].(map[string]any)
	start, err := time.Parse(time.RFC3339Nano, w["start"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return start.UTC(), w["width"].(string)
}

// One sync writes its hourly read and then, once a day, its 35-day daily
// re-read. Each write was stamped with the moment it was written, and the
// freshest width wins, so the daily re-read took over every day of the
// hourly reach until the next hourly sync: after an install's first sync,
// once a day and after every forced sync, the hourly chart showed one
// midnight point per day and the hourly tile started at the next midnight
// (?days=1 at 00:30 covered 30 minutes). Both reads are stamped with the
// sync's instant now, and the hours win a tie. After each kind of sync —
// the first, an hourly one, a forced daily one, and those where one of the
// reads fails or does not land — every view shows the same bill: the hours
// of every day in the reach, a hand-over split at its hour, and a day
// outside the reach as the daily re-read revised it.
//
// The syncs run on the fixture's clock (the client's Now). The endpoints
// read the wall clock; the test starts clear of a whole hour (it runs in
// about a second; it waits out the last 20 s before one), so neither a
// window's first hour nor its first day (midnight is a whole hour) moves
// against the fixture while it runs.
func TestBilledViewsAfterEverySync(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	if next := now.Truncate(time.Hour).Add(time.Hour); next.Sub(now) < 20*time.Second {
		time.Sleep(next.Sub(now) + time.Second)
		now = time.Now().UTC()
	}
	now = now.Truncate(time.Microsecond) // as Postgres stores it
	today := now.Truncate(24 * time.Hour)
	yesterday, twoAgo, outside := today.Add(-24*time.Hour), today.Add(-48*time.Hour), today.Add(-4*24*time.Hour)
	hour := func(at time.Time, apiKey string, cents int) billedHour {
		c := strconv.Itoa(cents)
		return billedHour{start: at, apiKey: apiKey, model: opus, tier: "standard",
			costs: map[string][2]string{"output_tokens": {c, c}}, out: int64(cents) * 1000}
	}
	fake := &fakeAnalytics{hours: []billedHour{
		hour(today, "apikey_s", 100),
		hour(yesterday.Add(3*time.Hour), "apikey_s", 200),
		hour(yesterday.Add(15*time.Hour), "apikey_s", 300),
		hour(yesterday.Add(8*time.Hour), "apikey_h", 400),  // key_b's
		hour(yesterday.Add(14*time.Hour), "apikey_h", 500), // key_c's
		hour(twoAgo.Add(5*time.Hour), "apikey_s", 600),
		hour(twoAgo.Add(20*time.Hour), "apikey_s", 700),
		hour(outside.Add(9*time.Hour), "apikey_s", 800), // revised to 900 below
	}}
	clock := func(at time.Time) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.at, fake.refreshed = at, at
	}
	s := billingStack(t, fake)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	if err := s.secrets.Put(ctx, s.orgID, secrets.AdminKey, fakeAdminKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_s', 0, 'apikey_s', $2, NULL), ($1, 'key_b', 0, 'apikey_h', $2, $3), ($1, 'key_c', 0, 'apikey_h', $3, NULL)`,
		s.orgID, now.Add(-10*24*time.Hour), yesterday.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The day outside the reach, read by its hours while it was inside, by
	// a sync on the day after it. The bill revises it since.
	clock(outside.Add(30 * time.Hour))
	if err := s.orch.SyncBillingOrg(ctx, s.orgID, false); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.hours[len(fake.hours)-1] = hour(outside.Add(9*time.Hour), "apikey_s", 900)
	fake.mu.Unlock()
	// (a) is an install's first sync: the daily re-read runs because none
	// has run yet.
	if _, err := s.pool.Exec(ctx, `UPDATE billing_sync SET last_daily_at = NULL WHERE org_id = $1`, s.orgID); err != nil {
		t.Fatal(err)
	}

	day := func(d time.Time) string { return d.Format("2006-01-02") }
	wantDaily := map[string]float64{
		day(today) + " key_s":     1,
		day(yesterday) + " key_s": 5, day(yesterday) + " key_b": 4, day(yesterday) + " key_c": 5,
		day(twoAgo) + " key_s":  13,
		day(outside) + " key_s": 9,
	}
	wantHourly := map[string]float64{}
	for _, h := range fake.hours[:len(fake.hours)-1] {
		key := map[string]string{"apikey_s": "key_s"}[h.apiKey]
		if h.apiKey == "apikey_h" {
			key = "key_b"
			if !h.start.Before(yesterday.Add(10 * time.Hour)) {
				key = "key_c"
			}
		}
		c, _ := strconv.ParseFloat(h.costs["output_tokens"][0], 64)
		wantHourly[h.start.Format("2006-01-02T15")+" "+key] += c / 100
	}
	wantOut := map[string]int64{}
	wantWeekly := map[string]float64{}
	for k, v := range wantDaily {
		wantOut[k] = int64(math.Round(v * 100000))
		wantWeekly[k[len("2006-01-02 "):]] += v
	}
	wantKeys := map[string]float64{"apikey_s": 28, "apikey_h": 9}
	firstHour := func(at time.Time) time.Time {
		if h := at.Truncate(time.Hour); !h.Equal(at) {
			return h.Add(time.Hour)
		}
		return at
	}

	check := func(label string, hoursRead bool) {
		t.Helper()
		v := readBilledViews(t, admin, label)
		if hoursRead {
			sameAmounts(t, label+", hourly chart", v.hourly, wantHourly)
		}
		sameAmounts(t, label+", daily chart", v.daily, wantDaily)
		sameAmounts(t, label+", reconciliation", v.rec, wantDaily)
		for k, out := range wantOut {
			if v.recOut[k] != out {
				t.Errorf("%s, reconciliation, %s: %d output tokens billed, want %d", label, k, v.recOut[k], out)
			}
		}
		if len(v.recOut) != len(wantOut) {
			t.Errorf("%s, reconciliation: tokens %v, want %v", label, v.recOut, wantOut)
		}
		sameAmounts(t, label+", weekly chart", v.weekly, wantWeekly)
		sameAmounts(t, label+", key list", v.keysList, wantKeys)
		for days, since := range map[int]time.Time{1: now.Add(-24 * time.Hour), 2: now.Add(-48 * time.Hour)} {
			start, width := billedWindow(t, admin, fmt.Sprintf("days=%d&bucket=hour", days))
			want := firstHour(since)
			if !hoursRead {
				// The fallback: the first day is read whole (its 1d rows are
				// the freshest), so the window starts at the next midnight.
				want = since.Truncate(24 * time.Hour).Add(24 * time.Hour)
			}
			if width != "1h" || !start.Equal(want) {
				t.Errorf("%s, ?days=%d: window %s from %v, want 1h from %v", label, days, width, start, want)
			}
		}
	}
	sync := func(label string, at time.Time, force bool) error {
		t.Helper()
		clock(at)
		return s.orch.SyncBillingOrg(ctx, s.orgID, force)
	}
	status := func() map[string]any {
		return admin.expect(http.MethodGet, "/api/v1/cost/billing-sync", nil, http.StatusOK)
	}

	if err := sync("(a)", now, false); err != nil {
		t.Fatal(err)
	}
	if st := status(); st["last_daily_at"] == nil || st["last_status"] != "ok" {
		t.Fatalf("(a): status %v, want the daily re-read run", st)
	}
	check("(a) first sync", true)

	if err := sync("(b)", now.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if st := status(); st["last_daily_at"] != now.Format(time.RFC3339Nano) {
		t.Fatalf("(b): last daily %v, want %v (hourly only)", st["last_daily_at"], now.Format(time.RFC3339Nano))
	}
	check("(b) hourly sync", true)

	if err := sync("(c)", now.Add(2*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	check("(c) forced sync", true)

	// The daily re-read fails after the hourly read landed: the hours of
	// this sync are the freshest.
	fake.mu.Lock()
	fake.fail = map[string]int{"1d": http.StatusServiceUnavailable}
	fake.mu.Unlock()
	if err := sync("(d)", now.Add(3*time.Minute), true); err == nil {
		t.Fatal("(d): the daily re-read failed, want an error")
	}
	check("(d) daily read failed", true)

	// The hourly read fails: the daily re-read is not even asked, nothing
	// is replaced.
	fake.mu.Lock()
	fake.fail = map[string]int{"1h": http.StatusServiceUnavailable}
	daily := fake.widthRequests["1d"]
	fake.mu.Unlock()
	if err := sync("(e)", now.Add(4*time.Minute), true); err == nil {
		t.Fatal("(e): the hourly read failed, want an error")
	}
	fake.mu.Lock()
	if fake.widthRequests["1d"] != daily {
		t.Errorf("(e): %d daily requests after a failed hourly read, want none", fake.widthRequests["1d"]-daily)
	}
	fake.fail = nil
	// The hourly read does not land, the daily re-read does: inside the
	// reach the 1d rows are now the freshest. The views fall back together
	// — the hourly window starts at the next midnight, the hand-over day
	// keeps its complete hours — until the next hourly read lands.
	fake.empty = map[string]bool{"1h": true}
	fake.mu.Unlock()
	check("(e) hourly read failed", true)
	if err := sync("(f)", now.Add(5*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	check("(f) hourly read not landed", false)

	fake.mu.Lock()
	fake.empty = nil
	fake.mu.Unlock()
	if err := sync("(g)", now.Add(6*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	check("(g) hourly sync after it", true)
}

// billedTotals is GET /cost/billed's window totals per credential
// ("secret#slot"), and the window's start.
func billedTotals(t *testing.T, admin *apiClient, q string) (time.Time, map[string]float64) {
	t.Helper()
	got := admin.expect(http.MethodGet, "/api/v1/cost/billed?"+q, nil, http.StatusOK)
	w := got["window"].(map[string]any)
	start, err := time.Parse(time.RFC3339Nano, w["start"].(string))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, x := range w["totals"].([]any) {
		b := x.(map[string]any)
		out[fmt.Sprintf("%s#%v", b["secret_key"], b["secret_slot"])] += usd(t, b["billed_usd"])
	}
	return start.UTC(), out
}

// usageRow stores one billed_usage row of the given width on an API key.
func (f billingFixture) usageRow(t *testing.T, width string, start time.Time, apiKey, workspace string, output int, fetched time.Time) {
	t.Helper()
	end := start.Add(time.Hour)
	if width == "1d" {
		end = start.Add(24 * time.Hour)
	}
	f.exec(t, `INSERT INTO billed_usage (org_id, bucket_width, bucket_start, bucket_end, api_key_id, workspace_id, model,
			output, data_refreshed_at, fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,'claude-opus-5-5',$7,$8,$8)`,
		f.s.orgID, width, start, end, apiKey, workspace, output, fetched)
}

// A day is pinned to its hours only where its key changes hands: where the
// key's mappings overlapping the day belong to more than one credential. Any
// mapping boundary inside the day pinned it — a value re-saved on the same
// credential, a key's first mapping, a change of workspace only — and a
// pinned day never took the daily re-read's revision, in any view. A
// hand-over exactly at midnight is no hand-over of either day. A real one
// still splits the day by its hours — also between two slots of one secret
// (key_sl: 10 + 14, where the day read whole would give 20). A zero-length
// mapping of another credential inside a held day holds nothing, so it is no
// hand-over either: the day takes the revision (key_zh).
func TestBilledHoursOnlyWhereTheHolderChanges(t *testing.T) {
	f := newBillingFixture(t, "rechnung-halterwechsel")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour).Add(-4 * 24 * time.Hour)
	at10, long := day.Add(10*time.Hour), now.Add(-10*24*time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, workspace_id, valid_from, valid_to)
		VALUES ($1, 'key_r', 0, 'apikey_r', '', $2, $3), ($1, 'key_r', 0, 'apikey_r', '', $3, NULL),
			($1, 'key_f', 0, 'apikey_f', '', $3, NULL),
			($1, 'key_w', 0, 'apikey_w', '', $2, $3), ($1, 'key_w', 0, 'apikey_w', 'wrkspc_a', $3, NULL),
			($1, 'key_m1', 0, 'apikey_m', '', $2, $4), ($1, 'key_m2', 0, 'apikey_m', '', $4, NULL),
			($1, 'key_hb', 0, 'apikey_h', '', $2, $3), ($1, 'key_hc', 0, 'apikey_h', '', $3, NULL),
			($1, 'key_sl', 0, 'apikey_l', '', $2, $3), ($1, 'key_sl', 1, 'apikey_l', '', $3, NULL),
			($1, 'key_zh', 0, 'apikey_z', '', $2, NULL), ($1, 'key_zz', 0, 'apikey_z', '', $5, $5)`,
		f.s.orgID, long, at10, day, day.Add(12*time.Hour))
	// The hours as read after the day ended, inside the hourly reach ($1 and
	// 100 000 output tokens an hour); the day re-read since, revised to $20
	// and 2 000 000 tokens.
	read := day.Add(30 * time.Hour)
	for _, k := range [][2]string{{"apikey_r", ""}, {"apikey_f", ""}, {"apikey_w", "wrkspc_a"}, {"apikey_m", ""}, {"apikey_h", ""},
		{"apikey_l", ""}, {"apikey_z", ""}} {
		for h := 0; h < 24; h++ {
			f.billedRow(t, "1h", day.Add(time.Duration(h)*time.Hour), k[0], k[1], 100, read)
			f.usageRow(t, "1h", day.Add(time.Duration(h)*time.Hour), k[0], k[1], 100000, read)
		}
		f.billedRow(t, "1d", day, k[0], k[1], 2000, now)
		f.usageRow(t, "1d", day, k[0], k[1], 2000000, now)
	}

	// Keyed by secret: key_sl's two slots add up.
	want := map[string]float64{"key_r": 20, "key_f": 20, "key_w": 20, "key_m2": 20, "key_hb": 10, "key_hc": 14,
		"key_sl": 24, "key_zh": 20}
	for _, q := range []string{"days=7&bucket=hour", "days=7&bucket=day", "days=90&bucket=week"} {
		_, tile, chart := billedShown(t, admin, q)
		sameAmounts(t, q+", tile", tile, want)
		sameAmounts(t, q+", chart", chart, want)
	}
	rec := admin.expect(http.MethodGet, "/api/v1/cost/reconcile?days=7", nil, http.StatusOK)
	money, tokens := map[string]float64{}, map[string]float64{}
	for _, x := range rec["days"].([]any) {
		d := x.(map[string]any)
		if d["day"] != day.Format("2006-01-02") {
			continue
		}
		for _, y := range d["models"].([]any) {
			m := y.(map[string]any)
			money[d["secret_key"].(string)] += usd(t, m["billed_usd"])
			for _, z := range m["tokens"].([]any) {
				if tok := z.(map[string]any); tok["type"] == "output" {
					tokens[d["secret_key"].(string)] += tok["billed"].(float64) / 100000
				}
			}
		}
	}
	sameAmounts(t, "reconciliation", money, want)
	sameAmounts(t, "reconciliation, output tokens (100 000)", tokens, want)
	keys := admin.expect(http.MethodGet, "/api/v1/cost/billing-keys", nil, http.StatusOK)
	listed := map[string]float64{}
	for _, x := range keys["keys"].([]any) {
		k := x.(map[string]any)
		listed[k["api_key_id"].(string)] += usd(t, k["billed_usd"])
	}
	sameAmounts(t, "key list", listed, map[string]float64{"apikey_r": 20, "apikey_f": 20, "apikey_w": 20, "apikey_m": 20, "apikey_h": 24,
		"apikey_l": 24, "apikey_z": 20})
	// The slot hand-over by slot: each slot its hours.
	_, perSlot := billedTotals(t, admin, "days=7&bucket=day")
	sameAmounts(t, "tile per slot", map[string]float64{"key_sl#0": perSlot["key_sl#0"], "key_sl#1": perSlot["key_sl#1"]},
		map[string]float64{"key_sl#0": 10, "key_sl#1": 14})
}
