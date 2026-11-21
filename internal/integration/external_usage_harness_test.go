package integration

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"covey/internal/identity"
	"covey/internal/observability"
)

// Criterion 4 of #120: F1 and F2 against the operator's measurement.
//
// testdata/harness-home-2026-11-17 holds home's Claude Code model calls of
// 2026-11-17 15:03 to 2026-11-18 18:34:36 UTC as token counts (rows.json.gz,
// in the intake's row format), and the measurement script's figures for the
// same calls per 10-minute and 10-hour bucket and model (harness_10min.csv,
// harness_10h.csv; scripts/measure/harness_usage.py in covey-experiment).
// The test loads the rows through POST /cost/external-usage and holds
// covey's /cost/cells against the script, every expected figure computed
// from the CSVs; only the script's headline figures are written down here.
//
// The script prices any model id containing "opus", "sonnet" or "haiku".
// covey's list knows claude-opus-5-5, claude-sonnet-5-5 and claude-haiku-5-5
// only, so claude-sonnet-5 (≈ $4.11 in the script), claude-haiku-4-5-20251001
// (≈ $0.005) and Claude Code's zero-token "<synthetic>" rows get no figure
// from covey — null, not $0 — and their tokens count as unpriced. Covey is
// therefore compared with the script restricted to the models it prices, and
// the script's own figures for the other three are added back to reach the
// headline $455.0736.

const harnessFixture = "testdata/harness-home-2026-11-17"

// The models covey's list does not know (#154 priced the rest).
var harnessUnpriced = map[string]bool{"<synthetic>": true}

// harnessScriptDiffers: models covey now prices (#154) that the measurement
// script priced differently. harness_usage.py matched every "sonnet" model to
// Sonnet 5.5's rates (cache read $0.10), while Sonnet 5 reads at 0.1x input
// ($0.20). The script's figure for them is left out of the comparisons, as an
// unpriced model's is, and covey must price them.
var harnessScriptDiffers = map[string]bool{"claude-sonnet-5": true, "claude-haiku-4-5-20251001": true}

// harnessCacheReadGap: covey's cache-read rate minus the script's, $/MTok, for
// the harnessScriptDiffers models. Their input, output and cache-write rates
// are the same in both (Sonnet 5: 2 / 10 / 2.5 / 4), so the script's figure
// plus cache_read × this gap must equal covey's to the cent.
var harnessCacheReadGap = map[string]float64{"claude-sonnet-5": 0.20 - 0.10}

// harnessScriptUnpriced: models the script left unpriced (its figure is $0), so
// covey's whole figure is the gap: priced here from the tokens at covey's rates.
var harnessScriptUnpriced = map[string]bool{"claude-haiku-4-5-20251001": true}

// harnessDiffersWant is covey's expected figure for a harnessScriptDiffers model:
// the script's dollars plus the rate gap on its cache-read tokens.
func harnessDiffersWant(model string, scriptUSD float64, tokens map[string]int64) float64 {
	if harnessScriptUnpriced[model] {
		p, _ := observability.ListPrice(model)
		return (float64(tokens["input"])*p.Input + float64(tokens["output"])*p.Output + float64(tokens["cache_read"])*p.CacheRead +
			float64(tokens["cache_write_5m"])*p.CacheWrite5m + float64(tokens["cache_write_1h"])*p.CacheWrite1h) / 1e6
	}
	return scriptUSD + float64(tokens["cache_read"])*harnessCacheReadGap[model]/1e6
}

// harnessNotCompared: the script's figure is not a reference for these models
// as they stand; the harnessScriptDiffers ones are compared via harnessDiffersWant.
func harnessNotCompared(m string) bool { return harnessUnpriced[m] || harnessScriptDiffers[m] }

// The measurement's headline figures (results/2026-11-18-harness-home).
const (
	harnessTotalUSD   = 455.0736
	harnessSubUSD     = 220.3994
	harnessMedianUSD  = 2.12
	harnessPeakUSD    = 14.51
	harnessCalls      = 4392
	harnessSourceBase = "operator: home / "
)

// harnessFigure is one line of the script's CSVs.
type harnessFigure struct {
	calls  int64
	tokens map[string]int64
	usd    float64
	sub    float64
}

type harnessKey struct {
	period time.Time
	model  string
}

var harnessTokenTypes = []string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h"}

func readHarnessCSV(t *testing.T, name string, shift time.Duration) map[harnessKey]harnessFigure {
	t.Helper()
	f, err := os.Open(filepath.Join(harnessFixture, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	out := map[harnessKey]harnessFigure{}
	for _, r := range recs[1:] {
		p, err := time.Parse("2006-01-02T15:04Z", r[col["bucket_utc"]])
		if err != nil {
			t.Fatal(err)
		}
		num := func(c string) int64 {
			n, err := strconv.ParseInt(r[col[c]], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		usd := func(c string) float64 {
			v, err := strconv.ParseFloat(r[col[c]], 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		fig := harnessFigure{calls: num("messages"), tokens: map[string]int64{},
			usd: usd("list_usd"), sub: usd("of_which_subagents_usd")}
		for _, k := range harnessTokenTypes {
			fig.tokens[k] = num(k)
		}
		out[harnessKey{p.Add(shift).UTC(), r[col["model"]]}] = fig
	}
	return out
}

// readHarnessRows reads the fixture's rows as they will be sent: numbers
// stay json.Number, so they go out exactly as they came in.
func readHarnessRows(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(harnessFixture, "rows.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(zr)
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func cents(v float64) float64 { return math.Round(v * 100) }

// sameCents: covey's exact figure and the script's, as the script prints it
// (4 places), agree in the 4th place (±1 for a tie the two round apart) and,
// both taken to 4 places first, to the cent. Rounding covey's exact figure
// straight to cents would part from the CSV on a half-cent tie the CSV's own
// 4-place rounding made (e.g. 8.354989 → 8.3550 → $8.36 against $8.35).
func sameCents(covey, script float64) bool {
	c4, s4 := int64(math.Round(covey*1e4)), int64(math.Round(script*1e4))
	d := c4 - s4
	return d >= -1 && d <= 1 && (c4+50)/100 == (s4+50)/100
}

// upperMedian is the measurement's median: the upper middle of the sorted
// buckets (138 buckets with calls; $2.12 is the 70th).
func upperMedian(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func maxOf(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		m = math.Max(m, x)
	}
	return m
}

func TestExternalUsageMatchesHarnessMeasurement(t *testing.T) {
	rows := readHarnessRows(t)
	if len(rows) != harnessCalls {
		t.Fatalf("the fixture holds %d rows, want %d", len(rows), harnessCalls)
	}
	tsOf := func(r map[string]any) time.Time {
		ts, err := time.Parse(time.RFC3339Nano, r["ts"].(string))
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	first, last := tsOf(rows[0]), tsOf(rows[0])
	for _, r := range rows {
		ts := tsOf(r)
		if ts.Before(first) {
			first = ts
		}
		if ts.After(last) {
			last = ts
		}
	}

	// /cost/cells looks back ?days= from now, and at ?bucket=10min two days
	// at most. Once the fixture's first call is more than 40 hours old, the
	// rows move forward by whole 10-hour steps until it is 30 to 40 hours old
	// (its last call then lies 2.5 to 12.5 hours back): every 10-minute and
	// 10-hour boundary is epoch-fixed, so each stays where it was relative to
	// the calls and every figure is the same.
	var shift time.Duration
	const reach = 40 * time.Hour
	if age := time.Since(first); age > reach {
		step := 10 * time.Hour
		shift = (age - reach + step - 1) / step * step
		for _, r := range rows {
			r["ts"] = tsOf(r).Add(shift).Format(time.RFC3339Nano)
		}
		first, last = first.Add(shift), last.Add(shift)
	}
	const days = 2

	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	s.mitglied(t, "security@test.local", "Security", identity.RoleSecurity, "security-passwort")
	key, _ := createKey(t, login(t, s, "security@test.local", "security-passwort"), "harness uploader", 30)

	// One source per model, so that covey's cells (bucket × source × origin)
	// can be held against the script's per-model buckets.
	byModel := map[string][]map[string]any{}
	for _, r := range rows {
		m := r["model"].(string)
		byModel[m] = append(byModel[m], r)
	}
	post := func(source string, batch []map[string]any) {
		t.Helper()
		resp := bearer(t, s, key, http.MethodPost, externalPath, extUpload(source, batch...))
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("upload of %d rows (%s): HTTP %d: %s", len(batch), source, resp.StatusCode, body)
		}
		var res observability.ExternalUploadResult
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatal(err)
		}
		if res.Received != len(batch) || res.Inserted != len(batch) {
			t.Fatalf("upload of %d rows (%s): %s", len(batch), source, body)
		}
	}
	batches := 0
	for model, mrows := range byModel {
		// Batches within the intake's limits: ≤ 5,000 rows and ≤ 2 MiB
		// (with room for the envelope).
		var batch []map[string]any
		size := 0
		for _, r := range mrows {
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch) == observability.ExternalMaxRows || size+len(b)+1 > observability.ExternalMaxBodyBytes-4096 {
				post(harnessSourceBase+model, batch)
				batches++
				batch, size = nil, 0
			}
			batch = append(batch, r)
			size += len(b) + 1
		}
		post(harnessSourceBase+model, batch)
		batches++
	}
	if n := s.externalCount(t, s.orgID); n != harnessCalls {
		t.Fatalf("%d rows stored, want %d", n, harnessCalls)
	}
	t.Logf("%d rows in %d batches over %d models; window days=%d, shift %s", len(rows), batches, len(byModel), days, shift)

	want10m := readHarnessCSV(t, "harness_10min.csv", shift)
	want10h := readHarnessCSV(t, "harness_10h.csv", shift)
	modelOf := func(source string) string { return source[len(harnessSourceBase):] }

	// covey's cells, summed over origin into (bucket, model).
	type coveyFigure struct {
		calls          int64
		tokens         map[string]int64
		usd, sub       float64
		priced         bool
		unpricedTokens int64
		unpricedCalls  int64
	}
	collect := func(bucket string) map[harnessKey]*coveyFigure {
		ext := cellsExternal(t, admin, fmt.Sprintf("days=%d&bucket=%s", days, bucket))
		out := map[harnessKey]*coveyFigure{}
		for _, c := range ext.Cells {
			k := harnessKey{c.Period.UTC(), modelOf(c.Source)}
			f := out[k]
			if f == nil {
				f = &coveyFigure{tokens: map[string]int64{}}
				out[k] = f
			}
			f.calls += c.Calls
			for _, tt := range harnessTokenTypes {
				f.tokens[tt] += c.Tokens[tt]
			}
			f.unpricedTokens += c.UnpricedTokens
			f.unpricedCalls += c.UnpricedCalls
			if c.ListUSD != nil {
				f.priced = true
				f.usd += *c.ListUSD
				if c.Origin == nil || *c.Origin != "main" {
					f.sub += *c.ListUSD
				}
			}
		}
		return out
	}
	got10m, got10h := collect("10min"), collect("10hour")

	// Every 10-minute bucket per model: the same calls and tokens; for a
	// priced model the script's $ to the cent (both rounded), in all and of
	// it subagents + workflows; for an unknown one no $ and every token
	// unpriced.
	checkBuckets := func(name string, want map[harnessKey]harnessFigure, got map[harnessKey]*coveyFigure) {
		t.Helper()
		for k, w := range want {
			g := got[k]
			if g == nil {
				t.Errorf("%s %s %s: no cell in covey, the script has %d calls / $%.4f", name, k.period.Format(time.RFC3339), k.model, w.calls, w.usd)
				continue
			}
			if g.calls != w.calls {
				t.Errorf("%s %s %s: %d calls, the script %d", name, k.period.Format(time.RFC3339), k.model, g.calls, w.calls)
			}
			var total int64
			for _, tt := range harnessTokenTypes {
				total += w.tokens[tt]
				if g.tokens[tt] != w.tokens[tt] {
					t.Errorf("%s %s %s: %s %d, the script %d", name, k.period.Format(time.RFC3339), k.model, tt, g.tokens[tt], w.tokens[tt])
				}
			}
			if harnessScriptDiffers[k.model] {
				if !g.priced || g.unpricedTokens != 0 {
					t.Errorf("%s %s %s: priced=%v, %d unpriced tokens; #154 prices it", name, k.period.Format(time.RFC3339), k.model, g.priced, g.unpricedTokens)
				}
				// Still compared: the script's figure plus its known rate gap.
				if want := harnessDiffersWant(k.model, w.usd, w.tokens); !sameCents(g.usd, want) {
					t.Errorf("%s %s %s: covey $%.6f, the script $%.4f + its rate gap = $%.6f", name, k.period.Format(time.RFC3339), k.model, g.usd, w.usd, want)
				}
				continue
			}
			if harnessUnpriced[k.model] {
				if g.priced || g.unpricedTokens != total || g.unpricedCalls != w.calls {
					t.Errorf("%s %s %s: priced=%v $%.4f, unpriced %d tokens / %d calls; want no $ and %d / %d unpriced",
						name, k.period.Format(time.RFC3339), k.model, g.priced, g.usd, g.unpricedTokens, g.unpricedCalls, total, w.calls)
				}
				continue
			}
			if !g.priced || g.unpricedTokens != 0 {
				t.Errorf("%s %s %s: priced=%v, %d unpriced tokens; want a figure", name, k.period.Format(time.RFC3339), k.model, g.priced, g.unpricedTokens)
			}
			if !sameCents(g.usd, w.usd) {
				t.Errorf("%s %s %s: covey $%.6f, the script $%.4f", name, k.period.Format(time.RFC3339), k.model, g.usd, w.usd)
			}
			if !sameCents(g.sub, w.sub) {
				t.Errorf("%s %s %s: subagents+workflows covey $%.6f, the script $%.4f", name, k.period.Format(time.RFC3339), k.model, g.sub, w.sub)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("%s %s %s: a cell in covey the script does not have", name, k.period.Format(time.RFC3339), k.model)
			}
		}
	}
	checkBuckets("10min", want10m, got10m)
	checkBuckets("10hour", want10h, got10h)

	// F2: every 10-hour bucket is the sum of its sixty 10-minute buckets
	// (the same F1 figures, summed in another order: to 1e-9).
	for k, h := range got10h {
		var usd float64
		var calls int64
		for k10, m := range got10m {
			if k10.model == k.model && !k10.period.Before(k.period) && k10.period.Before(k.period.Add(10*time.Hour)) {
				usd += m.usd
				calls += m.calls
			}
		}
		if math.Abs(h.usd-usd) > 1e-9 || h.calls != calls {
			t.Errorf("10hour %s %s: $%.9f / %d calls, Σ of its 10-minute buckets $%.9f / %d", k.period.Format(time.RFC3339), k.model, h.usd, h.calls, usd, calls)
		}
		if k.period.Unix()%36000 != 0 {
			t.Errorf("10hour bucket %s is not on an epoch-fixed boundary", k.period.Format(time.RFC3339))
		}
	}

	// Range totals. covey over the models it prices against the script over
	// the same models (each CSV line is rounded to 4 places: ±$0.01); then
	// the script's figures for the unknown models added back give the
	// measurement's headline.
	ext := cellsExternal(t, admin, fmt.Sprintf("days=%d&bucket=10min", days))
	var coveyTotal, wantPriced, scriptUnpriced, scriptAll float64
	var coveySub, wantPricedSub, scriptUnpricedSub, scriptAllSub float64
	var scriptDiffers, scriptDiffersSub, coveyDiffers, wantDiffers float64
	for k, f := range got10m {
		if !harnessScriptDiffers[k.model] {
			coveySub += f.sub
		}
	}
	unpricedWant := map[string]int64{}
	unpricedCalls := map[string]int64{}
	for k, w := range want10m {
		scriptAll += w.usd
		scriptAllSub += w.sub
		if harnessScriptDiffers[k.model] {
			scriptDiffers += w.usd
			scriptDiffersSub += w.sub
			wantDiffers += harnessDiffersWant(k.model, w.usd, w.tokens)
			continue
		}
		if harnessUnpriced[k.model] {
			scriptUnpriced += w.usd
			scriptUnpricedSub += w.sub
			for _, tt := range harnessTokenTypes {
				unpricedWant[k.model] += w.tokens[tt]
			}
			unpricedCalls[k.model] += w.calls
			continue
		}
		wantPriced += w.usd
		wantPricedSub += w.sub
	}
	if len(ext.Sources) != len(byModel) {
		t.Fatalf("%d sources, want one per model (%d)", len(ext.Sources), len(byModel))
	}
	var coveyUnpricedTokens int64
	for _, src := range ext.Sources {
		m := modelOf(src.Source)
		if harnessScriptDiffers[m] {
			if src.ListUSD == nil || src.UnpricedTokens != 0 {
				t.Errorf("%s: list $%v, %d unpriced tokens; #154 prices it", src.Source, src.ListUSD, src.UnpricedTokens)
			} else {
				coveyDiffers += *src.ListUSD
			}
			continue
		}
		if harnessUnpriced[m] {
			if src.ListUSD != nil || src.UnpricedTokens != unpricedWant[m] || src.UnpricedCalls != unpricedCalls[m] ||
				len(src.UnpricedModels) != 1 || src.UnpricedModels[0] != m {
				t.Errorf("%s: list $%v, unpriced %d tokens / %d calls %v; want null and %d / %d [%s]",
					src.Source, src.ListUSD, src.UnpricedTokens, src.UnpricedCalls, src.UnpricedModels, unpricedWant[m], unpricedCalls[m], m)
			}
			coveyUnpricedTokens += src.UnpricedTokens
			continue
		}
		if src.ListUSD == nil || src.UnpricedTokens != 0 {
			t.Errorf("%s: list $%v, %d unpriced tokens; want a figure and none", src.Source, src.ListUSD, src.UnpricedTokens)
			continue
		}
		coveyTotal += *src.ListUSD
	}
	if first.Before(time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC).Add(shift)) || !last.Before(time.Date(2026, 11, 18, 18, 40, 0, 0, time.UTC).Add(shift)) {
		t.Errorf("the fixture runs %s .. %s, outside the measurement", first, last)
	}
	t.Logf("covey (priced models): $%.4f, subagents+workflows $%.4f; script over the same models $%.4f / $%.4f",
		coveyTotal, coveySub, wantPriced, wantPricedSub)
	t.Logf("script for the unknown models: $%.4f / $%.4f; covey's unpriced tokens %d %v", scriptUnpriced, scriptUnpricedSub, coveyUnpricedTokens, unpricedWant)
	if math.Abs(scriptAll-harnessTotalUSD) > 0.01 || math.Abs(scriptAllSub-harnessSubUSD) > 0.01 {
		t.Errorf("the CSV sums to $%.4f / $%.4f, the measurement says $%.4f / $%.4f", scriptAll, scriptAllSub, harnessTotalUSD, harnessSubUSD)
	}
	if math.Abs(coveyTotal-wantPriced) > 0.01 {
		t.Errorf("range total over the priced models: covey $%.4f, the script $%.4f", coveyTotal, wantPriced)
	}
	if math.Abs(coveySub-wantPricedSub) > 0.01 {
		t.Errorf("subagents+workflows over the priced models: covey $%.4f, the script $%.4f", coveySub, wantPricedSub)
	}
	t.Logf("models the script priced differently (#154): covey $%.4f, the script $%.4f, the script + its rate gap $%.4f", coveyDiffers, scriptDiffers, wantDiffers)
	if math.Abs(coveyDiffers-wantDiffers) > 0.01 {
		t.Errorf("models the script priced differently: covey $%.4f, the script + its cache-read rate gap $%.4f", coveyDiffers, wantDiffers)
	}
	if v := coveyTotal + scriptUnpriced + scriptDiffers; math.Abs(v-harnessTotalUSD) > 0.01 {
		t.Errorf("covey + the script's unknown models = $%.4f, the measurement $%.4f", v, harnessTotalUSD)
	}
	if v := coveySub + scriptUnpricedSub + scriptDiffersSub; math.Abs(v-harnessSubUSD) > 0.01 {
		t.Errorf("subagents+workflows: covey + the script's unknown models = $%.4f, the measurement $%.4f", v, harnessSubUSD)
	}

	// Per 10-minute bucket over every model: covey's figure plus the
	// script's for the unknown models; and both over the priced models only.
	coveyAll, coveyPriced, scriptPriced, scriptEvery := map[time.Time]float64{}, map[time.Time]float64{}, map[time.Time]float64{}, map[time.Time]float64{}
	for k, g := range got10m {
		if harnessScriptDiffers[k.model] {
			continue // the script's figure stands in below, as for an unpriced model
		}
		coveyAll[k.period] += g.usd
		if !harnessUnpriced[k.model] {
			coveyPriced[k.period] += g.usd
		}
	}
	for k, w := range want10m {
		scriptEvery[k.period] += w.usd
		if harnessNotCompared(k.model) {
			coveyAll[k.period] += w.usd
		} else {
			scriptPriced[k.period] += w.usd
		}
	}
	vals := func(m map[time.Time]float64) []float64 {
		var v []float64
		for _, x := range m {
			v = append(v, x)
		}
		return v
	}
	if a, b := upperMedian(vals(scriptEvery)), maxOf(vals(scriptEvery)); cents(a) != cents(harnessMedianUSD) || cents(b) != cents(harnessPeakUSD) {
		t.Errorf("the CSV's median/peak $%.4f / $%.4f, the measurement $%.2f / $%.2f", a, b, harnessMedianUSD, harnessPeakUSD)
	}
	med, peak := upperMedian(vals(coveyAll)), maxOf(vals(coveyAll))
	t.Logf("10-minute buckets: %d; median $%.4f, peak $%.4f (covey + the script's unknown models); priced only: covey $%.4f / $%.4f, script $%.4f / $%.4f",
		len(coveyAll), med, peak, upperMedian(vals(coveyPriced)), maxOf(vals(coveyPriced)), upperMedian(vals(scriptPriced)), maxOf(vals(scriptPriced)))
	if cents(med) != cents(harnessMedianUSD) || cents(peak) != cents(harnessPeakUSD) {
		t.Errorf("median/peak 10-minute bucket $%.4f / $%.4f, the measurement $%.2f / $%.2f", med, peak, harnessMedianUSD, harnessPeakUSD)
	}
	if a, b := upperMedian(vals(coveyPriced)), upperMedian(vals(scriptPriced)); !sameCents(a, b) {
		t.Errorf("median over the priced models: covey $%.4f, the script $%.4f", a, b)
	}
	if a, b := maxOf(vals(coveyPriced)), maxOf(vals(scriptPriced)); !sameCents(a, b) {
		t.Errorf("peak over the priced models: covey $%.4f, the script $%.4f", a, b)
	}

	// The three 10-hour slices as sums of 10-minute buckets (against sums of
	// the CSV's 4-place figures: ±$0.01). The script's
	// $154 / $205 / $96 are its 10-hour buckets, which are epoch-fixed
	// (bucket() floors the epoch seconds): [14:00, 00:00) — calls start at
	// 15:03 —, [00:00, 10:00) and [10:00, 20:00) — calls end at 18:34.
	// Slices cut from 15:00 ([15:00, 01:00), [01:00, 11:00), [11:00, 18:40))
	// sum to other figures; they are held against the script's 10-minute
	// buckets over the same spans.
	sum := func(m map[time.Time]float64, from, to time.Time) float64 {
		var v float64
		for p, x := range m {
			if !p.Before(from.Add(shift)) && p.Before(to.Add(shift)) {
				v += x
			}
		}
		return v
	}
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	for _, sl := range []struct {
		from, to time.Time
		dollars  float64
	}{{at(17, 14), at(18, 0), 154}, {at(18, 0), at(18, 10), 205}, {at(18, 10), at(18, 20), 96}} {
		v := sum(coveyAll, sl.from, sl.to)
		if math.Round(v) != sl.dollars {
			t.Errorf("slice %s..%s: $%.4f, the measurement $%.0f", sl.from.Format("02T15:04"), sl.to.Format("02T15:04"), v, sl.dollars)
		}
		if w := sum(scriptEvery, sl.from, sl.to); math.Abs(v-w) > 0.01 {
			t.Errorf("slice %s..%s: $%.4f, the script's 10-minute buckets $%.4f", sl.from.Format("02T15:04"), sl.to.Format("02T15:04"), v, w)
		}
	}
	for _, sl := range [][2]time.Time{{at(17, 15), at(18, 1)}, {at(18, 1), at(18, 11)}, {at(18, 11), at(18, 18).Add(40 * time.Minute)}} {
		v, w := sum(coveyAll, sl[0], sl[1]), sum(scriptEvery, sl[0], sl[1])
		t.Logf("slice %s..%s: $%.4f (script $%.4f)", sl[0].Format("02T15:04"), sl[1].Format("02T15:04"), v, w)
		if math.Abs(v-w) > 0.01 {
			t.Errorf("slice %s..%s: $%.4f, the script's 10-minute buckets $%.4f", sl[0].Format("02T15:04"), sl[1].Format("02T15:04"), v, w)
		}
	}
}
