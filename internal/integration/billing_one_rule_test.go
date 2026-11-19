package integration

import (
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"
)

// billPiece is one billed row the rule is to read: an hour, or a day read
// whole, with the credential it goes to.
type billPiece struct {
	start         time.Time
	whole         bool
	apiKey, model string
	cents         int
	key           string
}

func (p billPiece) usd() float64 { return float64(p.cents) / 100 }

// One rule for every reader (#82, #98 merged with #126). The bill is stored
// twice — hourly (1h) and daily (1d) rows, read at different times — and the
// two disagree: Anthropic revises a day, or a read misses hours. The chart
// at every bucket (hour, 10hour, day, week), the tile and the reconciliation
// pick the rows by one rule (chosenRows): a hand-over day by its complete
// hours, every other day by the width read last, the hours on a tie. So for
// every window the page offers (?days=1, 2, 7, 30) the tile is the sum of
// the chart from the window's start, per credential, and the chart's and the
// reconciliation's bill per UTC day × credential × model is the rule's.
//
// The fixture, every day's 1h and 1d sums differing:
//   - D−10: a 1d row only ($30), as the 35-day backfill leaves it;
//   - D−5: hours read complete ($24), the 1d re-read newer ($20): $20, whole;
//   - D−4: the reverse, the hours newer ($24) than the 1d read ($22): $24;
//   - D−2: inside the reach, hours newer ($24) than the 1d read ($20): $24;
//   - D−1: inside the reach, the 1d read newer — apikey_a's day whole ($25
//     opus, $25 sonnet against $24 + $24 of hours); apikey_h changes hands
//     at 10:00, so its complete hours split $10 / $14 against a 1d row of $30;
//   - today: hours only.
//
// At hour and 10hour a day read whole is set apart (whole_days), never put
// into an hour or a 10-hour bucket: those hold the hours of the rule and
// nothing else, and a 10-hour bucket that overlaps a whole day of its key is
// not complete.
func TestBilledOneRuleEveryViewAndWindow(t *testing.T) {
	f := newBillingFixture(t, "rechnung-eine-regel")
	admin := login(t, f.s, "admin@test.local", "admin-passwort")
	now := time.Now().UTC().Truncate(time.Microsecond)
	today := now.Truncate(24 * time.Hour)
	d := func(n int) time.Time { return today.Add(-time.Duration(n) * 24 * time.Hour) }
	const opus, sonnet = "claude-opus-5-5", "claude-sonnet-5-5"
	handOver := d(1).Add(10 * time.Hour)
	f.exec(t, `INSERT INTO credential_billing_keys (org_id, secret_key, secret_slot, api_key_id, valid_from, valid_to)
		VALUES ($1, 'key_a', 0, 'apikey_a', $2, NULL), ($1, 'key_hb', 0, 'apikey_h', $2, $3), ($1, 'key_hc', 0, 'apikey_h', $3, NULL)`,
		f.s.orgID, now.Add(-60*24*time.Hour), handOver)

	// row stores one row in billed_cost and the same shape in billed_usage
	// (1 000 output tokens a cent).
	row := func(width string, start time.Time, apiKey, model string, cents int, read time.Time) {
		t.Helper()
		end := start.Add(time.Hour)
		if width == "1d" {
			end = start.Add(24 * time.Hour)
		}
		f.exec(t, `INSERT INTO billed_cost (org_id, bucket_width, bucket_start, bucket_end, api_key_id, workspace_id, model,
				cost_type, token_type, amount_cents, list_amount_cents, data_refreshed_at, fetched_at)
			VALUES ($1,$2,$3,$4,$5,'',$6,'tokens','output_tokens',$7,$7,$8,$8)`,
			f.s.orgID, width, start, end, apiKey, model, cents, read)
		f.exec(t, `INSERT INTO billed_usage (org_id, bucket_width, bucket_start, bucket_end, api_key_id, workspace_id, model,
				output, data_refreshed_at, fetched_at)
			VALUES ($1,$2,$3,$4,$5,'',$6,$7,$8,$8)`,
			f.s.orgID, width, start, end, apiKey, model, cents*1000, read)
	}
	var truth []billPiece
	hours := func(day time.Time, n int, apiKey, model string, cents int, read time.Time, holder func(time.Time) string, counted bool) {
		for h := 0; h < n; h++ {
			at := day.Add(time.Duration(h) * time.Hour)
			row("1h", at, apiKey, model, cents, read)
			if counted {
				truth = append(truth, billPiece{start: at, apiKey: apiKey, model: model, cents: cents, key: holder(at)})
			}
		}
	}
	wholeDay := func(day time.Time, apiKey, model string, cents int, read time.Time, key string, counted bool) {
		row("1d", day, apiKey, model, cents, read)
		if counted {
			truth = append(truth, billPiece{start: day, whole: true, apiKey: apiKey, model: model, cents: cents, key: key})
		}
	}
	isA := func(time.Time) string { return "key_a" }
	isH := func(at time.Time) string {
		if at.Before(handOver) {
			return "key_hb"
		}
		return "key_hc"
	}

	wholeDay(d(10), "apikey_a", opus, 3000, d(10).Add(48*time.Hour), "key_a", true)
	hours(d(5), 24, "apikey_a", opus, 100, d(5).Add(30*time.Hour), isA, false)
	wholeDay(d(5), "apikey_a", opus, 2000, now.Add(-time.Hour), "key_a", true)
	wholeDay(d(4), "apikey_a", opus, 2200, d(4).Add(26*time.Hour), "key_a", false)
	hours(d(4), 24, "apikey_a", opus, 100, d(4).Add(40*time.Hour), isA, true)
	wholeDay(d(2), "apikey_a", opus, 2000, now.Add(-time.Hour), "key_a", false)
	hours(d(2), 24, "apikey_a", opus, 100, now, isA, true)
	hours(d(1), 24, "apikey_a", opus, 100, now, isA, false)
	hours(d(1), 24, "apikey_a", sonnet, 100, now, isA, false)
	wholeDay(d(1), "apikey_a", opus, 2500, now.Add(time.Second), "key_a", true)
	wholeDay(d(1), "apikey_a", sonnet, 2500, now.Add(time.Second), "key_a", true)
	hours(d(1), 24, "apikey_h", opus, 100, now, isH, true)
	wholeDay(d(1), "apikey_h", opus, 3000, now.Add(time.Second), "key_hc", false)
	hours(today, now.Hour(), "apikey_a", opus, 100, now, isA, true)

	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	// The chart's points and whole days of one answer.
	type point struct {
		at            time.Time
		apiKey, model string
		key           string
		usd           float64
		complete      bool
	}
	parse := func(list any) []point {
		var out []point
		xs, _ := list.([]any)
		for _, x := range xs {
			p := x.(map[string]any)
			at, err := time.Parse(time.RFC3339Nano, p["bucket"].(string))
			if err != nil {
				t.Fatal(err)
			}
			key, _ := p["secret_key"].(string)
			out = append(out, point{at: at.UTC(), apiKey: p["api_key_id"].(string), model: p["model"].(string), key: key,
				usd: usd(t, p["billed_usd"]), complete: p["complete"] == true})
		}
		return out
	}

	for _, days := range []int{1, 2, 7, 30} {
		since := now.Add(-time.Duration(days) * 24 * time.Hour)
		from := since.Truncate(24 * time.Hour) // the series' first day
		for _, bucket := range []string{"hour", "10hour", "day"} {
			q := fmt.Sprintf("days=%d&bucket=%s", days, bucket)
			got := admin.expect(http.MethodGet, "/api/v1/cost/billed?"+q, nil, http.StatusOK)
			w := got["window"].(map[string]any)
			start, err := time.Parse(time.RFC3339Nano, w["start"].(string))
			if err != nil {
				t.Fatal(err)
			}
			points, whole := parse(got["points"]), parse(got["whole_days"])

			// The tile = the chart from the window's start = the rule's rows
			// from there, per credential.
			tile, chart, want := map[string]float64{}, map[string]float64{}, map[string]float64{}
			for _, x := range w["totals"].([]any) {
				b := x.(map[string]any)
				tile[b["secret_key"].(string)] += usd(t, b["billed_usd"])
			}
			for _, p := range append(append([]point{}, points...), whole...) {
				if p.key != "" && !p.at.Before(start) {
					chart[p.key] += p.usd
				}
			}
			for _, p := range truth {
				if !p.start.Before(start) {
					want[p.key] += p.usd()
				}
			}
			sameAmounts(t, q+", tile", tile, want)
			// The page asks for the bucket its window is cut in
			// (billedFetchBucket in web/src/pages/costFigures.ts): hours for
			// an hour, 10-hour or 10-minute chart and for a window of up to
			// two days, else days. A 10-hour bucket, or a day in an hourly
			// window, can begin before the window's start; those are summed
			// over the series' whole span below.
			if bucket == "hour" || (bucket == "day" && days > 2) {
				sameAmounts(t, q+", chart from the window's start", chart, want)
			}
			all, wantAll := map[string]float64{}, map[string]float64{}
			for _, p := range append(append([]point{}, points...), whole...) {
				all[p.key] += p.usd
			}
			for _, p := range truth {
				if !p.start.Before(from) {
					wantAll[p.key] += p.usd()
				}
			}
			sameAmounts(t, q+", chart over the series", all, wantAll)

			// Per UTC day × credential × model, whole days and buckets
			// together: the rule's rows of the series' days.
			perDay, wantDay := map[string]float64{}, map[string]float64{}
			for _, p := range append(append([]point{}, points...), whole...) {
				perDay[p.at.Truncate(24*time.Hour).Format("2006-01-02")+" "+p.key+" "+p.model] += p.usd
			}
			for _, p := range truth {
				if !p.start.Before(from) {
					wantDay[p.start.Truncate(24*time.Hour).Format("2006-01-02")+" "+p.key+" "+p.model] += p.usd()
				}
			}
			if bucket != "10hour" { // a 10-hour bucket can straddle midnight
				sameAmounts(t, q+", chart per day", perDay, wantDay)
			}
			if bucket == "day" {
				if len(whole) != 0 {
					t.Errorf("%s: whole_days %v, want none at a daily bucket", q, whole)
				}
				continue
			}

			// At hour and 10hour: a bucket holds the rule's hours only, and
			// each day read whole is set apart at its midnight.
			width := time.Hour
			if bucket == "10hour" {
				width = 10 * time.Hour
			}
			bin := func(at time.Time) time.Time {
				return time.Unix(at.Unix()/int64(width/time.Second)*int64(width/time.Second), 0).UTC()
			}
			gotBins, wantBins, gotWhole, wantWhole := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
			for _, p := range points {
				gotBins[p.at.Format(time.RFC3339)+" "+p.apiKey+" "+p.model+" "+p.key] += p.usd
			}
			for _, p := range whole {
				if !p.at.Equal(p.at.Truncate(24 * time.Hour)) {
					t.Errorf("%s: whole day at %v, want a midnight", q, p.at)
				}
				gotWhole[p.at.Format(time.RFC3339)+" "+p.apiKey+" "+p.model+" "+p.key] += p.usd
			}
			for _, p := range truth {
				if p.start.Before(from) {
					continue
				}
				if p.whole {
					wantWhole[p.start.Format(time.RFC3339)+" "+p.apiKey+" "+p.model+" "+p.key] += p.usd()
				} else {
					wantBins[bin(p.start).Format(time.RFC3339)+" "+p.apiKey+" "+p.model+" "+p.key] += p.usd()
				}
			}
			sameAmounts(t, q+", buckets", gotBins, wantBins)
			sameAmounts(t, q+", whole days", gotWhole, wantWhole)
			if bucket == "10hour" {
				for _, p := range points {
					overlaps := false
					for _, x := range truth {
						if x.whole && x.apiKey == p.apiKey && x.start.Before(p.at.Add(width)) && x.start.Add(24*time.Hour).After(p.at) {
							overlaps = true
						}
					}
					if overlaps && p.complete {
						t.Errorf("%s: 10-hour bucket %v of %s overlaps a day read whole, want it not complete", q, p.at, p.apiKey)
					}
				}
			}
		}
	}

	// The week chart (the page never asks for one: a week can begin before
	// the window): the same total per credential.
	for _, days := range []int{30, 90} {
		q := fmt.Sprintf("days=%d&bucket=week", days)
		_, tile, chart := billedShown(t, admin, q)
		want := map[string]float64{}
		for _, p := range truth {
			want[p.key] += p.usd()
		}
		sameAmounts(t, q+", tile", tile, want)
		sameAmounts(t, q+", chart", chart, want)
	}

	// The reconciliation: per UTC day × credential × model, the same bill
	// and the same output tokens.
	rec := admin.expect(http.MethodGet, "/api/v1/cost/reconcile?days=30", nil, http.StatusOK)
	money, tokens, want := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, x := range rec["days"].([]any) {
		dd := x.(map[string]any)
		for _, y := range dd["models"].([]any) {
			m := y.(map[string]any)
			k := dd["day"].(string) + " " + dd["secret_key"].(string) + " " + m["model"].(string)
			money[k] += usd(t, m["billed_usd"])
			for _, z := range m["tokens"].([]any) {
				if tok := z.(map[string]any); tok["type"] == "output" {
					tokens[k] += tok["billed"].(float64) / 100000
				}
			}
		}
	}
	for _, p := range truth {
		want[p.start.Format("2006-01-02")+" "+p.key+" "+p.model] += p.usd()
	}
	sameAmounts(t, "reconciliation", money, want)
	sameAmounts(t, "reconciliation, output tokens (100 000)", tokens, want)
	if !near(money[d(1).Format("2006-01-02")+" key_hb "+opus], 10) {
		t.Errorf("the hand-over day: key_hb %v, want its 10 hours", money[d(1).Format("2006-01-02")+" key_hb "+opus])
	}
}
