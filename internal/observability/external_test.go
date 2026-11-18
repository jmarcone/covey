package observability

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// validRow is an upload row that passes every rule; the cases below break
// one at a time.
func validRow(id string, ts time.Time) ExternalRow {
	n := func(v int64) *int64 { return &v }
	return ExternalRow{Host: "laptop", SessionID: "s-1", MessageID: id, TS: &ts, Model: "claude-opus-5-5",
		Input: n(10), Output: n(20), CacheRead: n(1000), CacheWrite5m: n(0), CacheWrite1h: n(50)}
}

// Every rule of #120's upload refuses on its own, and says which row broke it.
func TestValidateExternalRows(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	neg := int64(-1)
	bad := "assistant"
	usd := -0.5
	cases := []struct {
		name  string
		spoil func(*ExternalRow)
		want  string
	}{
		{"a negative input", func(r *ExternalRow) { r.Input = &neg }, "input must not be negative"},
		{"a negative cache write", func(r *ExternalRow) { r.CacheWrite1h = &neg }, "cache_write_1h must not be negative"},
		{"a missing output", func(r *ExternalRow) { r.Output = nil }, "output is required"},
		{"a missing ts", func(r *ExternalRow) { r.TS = nil }, "ts is required"},
		{"ts beyond now + 5 min", func(r *ExternalRow) { t := now.Add(5*time.Minute + time.Second); r.TS = &t }, "later than now + 5m0s"},
		{"no model", func(r *ExternalRow) { r.Model = "" }, "model is required"},
		{"no host", func(r *ExternalRow) { r.Host = "" }, "host is required"},
		{"an unknown origin", func(r *ExternalRow) { r.Origin = &bad }, "origin must be"},
		{"a negative reported_usd", func(r *ExternalRow) { r.ReportedUSD = &usd }, "reported_usd"},
		{"an overlong session id", func(r *ExternalRow) { r.SessionID = strings.Repeat("x", 257) }, "session_id must be"},
	}
	for _, c := range cases {
		rows := []ExternalRow{validRow("m-0", now), validRow("m-1", now)}
		c.spoil(&rows[1])
		err := ValidateExternalRows(rows, now)
		if err == nil || !strings.Contains(err.Error(), "row 1: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want row 1 and %q", c.name, err, c.want)
		}
	}

	// The edges that pass: exactly now + 5 min, counts of 0, every origin.
	edge := now.Add(5 * time.Minute)
	ok := validRow("m-edge", edge)
	for i, o := range []string{"main", "subagent", "workflow"} {
		r := validRow("m-"+o, now)
		r.Origin = &[]string{"main", "subagent", "workflow"}[i]
		if err := ValidateExternalRows([]ExternalRow{ok, r}, now); err != nil {
			t.Errorf("origin %s: %v", o, err)
		}
	}

	// One message twice in a request: which one should win is the sender's
	// to decide, not the server's.
	if err := ValidateExternalRows([]ExternalRow{validRow("m", now), validRow("m", now)}, now); err == nil ||
		!strings.Contains(err.Error(), "already is row 0") {
		t.Errorf("a repeated message_id: err = %v", err)
	}

	// 5,000 rows pass, 5,001 do not.
	many := make([]ExternalRow, ExternalMaxRows+1)
	for i := range many {
		many[i] = validRow(fmt.Sprintf("m-%d", i), now)
	}
	if err := ValidateExternalRows(many[:ExternalMaxRows], now); err != nil {
		t.Errorf("%d rows: %v", ExternalMaxRows, err)
	}
	if err := ValidateExternalRows(many, now); err == nil || !strings.Contains(err.Error(), "at most 5000") {
		t.Errorf("%d rows: err = %v", len(many), err)
	}

	for _, src := range []string{"", strings.Repeat("ü", 65)} {
		if ValidateExternalSource(src) == nil {
			t.Errorf("source %q passed", src)
		}
	}
	if err := ValidateExternalSource(strings.Repeat("ü", 64)); err != nil {
		t.Errorf("a 64-character source: %v", err)
	}
}

// F1: a model the list knows is priced with ListPrice(model).Cost, the
// agents' formula; one it does not know has no figure — nil, never $0.
func TestExternalListUSD(t *testing.T) {
	tok := ModelTokens{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000}
	got := ExternalListUSD("claude-opus-5-5", tok)
	if got == nil || math.Abs(*got-(4+20+0.20+5+8)) > 1e-9 {
		t.Errorf("opus 5.5 at a million of each: %v, want 37.20", got)
	}
	if got := ExternalListUSD("claude-opus-5-5[1m]", tok); got == nil {
		t.Error("a suffixed id was not resolved to its base")
	}
	if got := ExternalListUSD("claude-sonnet-5-7", tok); got != nil {
		t.Errorf("an unknown model was priced at %v", *got)
	}
}

// The 10-minute and 10-hour buckets are date_bin from the epoch: fixed
// boundaries, not ones that move with the window.
func TestPeriodSQL(t *testing.T) {
	for in, want := range map[string]string{
		"10min":  "date_bin('10 minutes', ts, TIMESTAMPTZ '1970-01-01 00:00 UTC')",
		"10hour": "date_bin('10 hours', ts, TIMESTAMPTZ '1970-01-01 00:00 UTC')",
		"hour":   "date_trunc('hour', ts, 'UTC')",
		"week":   "date_trunc('week', ts, 'UTC')",
		"'; --":  "date_trunc('day', ts, 'UTC')",
	} {
		if got := periodSQL(normalizeBucket(in), "ts"); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

// F3 per hour and credential: the meter's real cost split by list-price
// share; no outside calls is a share of 0; an hour without a reading has no
// figure; and the two shares always add up to the meter's cost.
func TestMeterShare(t *testing.T) {
	h1 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	h2, h3, h4 := h1.Add(time.Hour), h1.Add(2*time.Hour), h1.Add(3*time.Hour)
	usd := func(v float64) *float64 { return &v }
	links := map[string]MeterCredential{"operator: home": {SecretKey: "claude_oauth", SecretSlot: 0}}
	coverage := []MeterCoverage{
		{SecretKey: "claude_oauth", SecretSlot: 0, Source: "usage_cmd", WindowKind: WindowSevenDay, Readings: 5},
		// The same meter read in-band, less often: never counted beside it.
		{SecretKey: "claude_oauth", SecretSlot: 0, Source: "inband", WindowKind: WindowSevenDay, Readings: 2},
	}
	step := func(h time.Time, src string, points float64, u *float64, unpriced float64) MeterStep {
		return MeterStep{Period: h, SecretKey: "claude_oauth", SecretSlot: 0, Source: src, WindowKind: WindowSevenDay,
			Points: points, Readings: 1, USD: u, UnpricedPoints: unpriced}
	}
	steps := []MeterStep{
		step(h1, "usage_cmd", 2, usd(10), 0),
		step(h2, "usage_cmd", 1, usd(4), 0),
		step(h1, "inband", 3, usd(15), 0),
		// Moved, but no plan prices the points: R has no figure.
		step(h4, "usage_cmd", 1, nil, 1),
	}
	key := func(h time.Time) credHour { return credHour{h, "claude_oauth", 0} }
	lOut := map[credHour]float64{key(h1): 3, key(h3): 5, key(h4): 2}
	lCov := map[credHour]float64{key(h1): 1, key(h2): 2, key(h4): 1}

	// The source reported h1 through h4.
	spans := map[string]reportedSpan{"operator: home": {from: h1, to: h4.Add(5 * time.Minute)}}

	m := meterShare(links, spans, steps, coverage, lOut, lCov)
	byHour := map[time.Time]ExternalMeterHour{}
	for _, h := range m.Hours {
		byHour[h.Hour] = h
	}
	if len(byHour) != 4 {
		t.Fatalf("hours = %+v, want h1..h4", m.Hours)
	}
	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }

	// h1: R = 10 (the usage_cmd reading, not the in-band one), L_out 3 of 4.
	if h := byHour[h1]; !near(h.RealUSD, 10) || !near(h.ExternalRealUSD, 7.5) || !near(h.CoveyRealUSD, 2.5) {
		t.Errorf("h1 = %+v, want R 10 split 7.5 / 2.5", h)
	}
	// h2: no outside calls, so R_out = 0 and covey keeps all of it.
	if h := byHour[h2]; !near(h.ExternalRealUSD, 0) || !near(h.CoveyRealUSD, 4) {
		t.Errorf("h2 = %+v, want R_out 0, R_cov 4", h)
	}
	// h3 and h4: no figure, not 0.
	for _, h := range []time.Time{h3, h4} {
		if eh := byHour[h]; eh.RealUSD != nil || eh.ExternalRealUSD != nil || eh.CoveyRealUSD != nil {
			t.Errorf("%v = %+v, want no figure", h, eh)
		}
	}
	for _, h := range byHour {
		if h.RealUSD != nil && math.Abs(*h.ExternalRealUSD+*h.CoveyRealUSD-*h.RealUSD) > 1e-9 {
			t.Errorf("%v: R_out + R_cov = %v, R = %v", h.Hour, *h.ExternalRealUSD+*h.CoveyRealUSD, *h.RealUSD)
		}
	}
	// The tile: Σ R_out over the hours with a reading, over Σ R of the same.
	if m.RealUSD != 14 || math.Abs(m.ExternalRealUSD-7.5) > 1e-9 || !near(m.Share, 7.5/14) || m.UnmeteredListUSD != 7 {
		t.Errorf("totals = real %v, outside %v, share %v, unmetered %v; want 14, 7.5, %.4f, 7",
			m.RealUSD, m.ExternalRealUSD, m.Share, m.UnmeteredListUSD, 7.5/14)
	}
	if len(m.Credentials) != 1 || m.Credentials[0].MeterSource != "usage_cmd" ||
		m.Credentials[0].HoursWithReading != 2 || m.Credentials[0].HoursWithoutReading != 2 {
		t.Errorf("credentials = %+v", m.Credentials)
	}

	// No link, no share: a source billed per use has no meter.
	if m := meterShare(map[string]MeterCredential{}, spans, steps, coverage, lOut, lCov); len(m.Hours) != 0 || m.Share != nil {
		t.Errorf("without a link: %+v", m)
	}
}

// #120, the headline: an hour the outside sessions did not report is not
// split — its movement is neither covey's nor theirs — and a seat no source
// names is all unsplit. Covey's share never takes an hour's whole movement
// for want of outside data. The three parts add up to the dent, and the
// split hours carry the list prices and points the multiples need.
func TestMeterShareLeavesUnreportedHoursUnsplit(t *testing.T) {
	h0 := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	hr := func(i int) time.Time { return h0.Add(time.Duration(i) * time.Hour) }
	usd := func(v float64) *float64 { return &v }
	links := map[string]MeterCredential{"operator: home": {SecretKey: "seat_a", SecretSlot: 0}}
	coverage := []MeterCoverage{
		{SecretKey: "seat_a", SecretSlot: 0, CredentialKind: "subscription", Source: "usage_cmd", WindowKind: WindowSevenDay, Readings: 5},
		// A second seat no source names, and an API key: the key has no meter
		// to split.
		{SecretKey: "seat_b", SecretSlot: 0, CredentialKind: "subscription", Source: "inband", WindowKind: WindowSevenDay, Readings: 2},
		{SecretKey: "api", SecretSlot: 0, CredentialKind: "api_key", Source: "inband", WindowKind: WindowSevenDay, Readings: 2},
	}
	step := func(key, src string, h time.Time, points, u float64, resets int) MeterStep {
		return MeterStep{Period: h, SecretKey: key, SecretSlot: 0, Source: src, WindowKind: WindowSevenDay,
			Points: points, Readings: 1, Resets: resets, USD: usd(u)}
	}
	// seat_a at $4 a point: hour 0 before the uploads, hours 1-3 inside
	// them, hour 4 after the last outside call.
	steps := []MeterStep{
		step("seat_a", "usage_cmd", hr(0), 1, 4, 0),
		step("seat_a", "usage_cmd", hr(1), 2, 8, 0),
		step("seat_a", "usage_cmd", hr(2), 1, 4, 0),
		step("seat_a", "usage_cmd", hr(3), 3, 12, 1),
		step("seat_a", "usage_cmd", hr(4), 2, 8, 0),
		step("seat_b", "inband", hr(1), 1, 5, 0),
	}
	key := func(k string, i int) credHour { return credHour{hr(i), k, 0} }
	lOut := map[credHour]float64{key("seat_a", 1): 6, key("seat_a", 3): 1}
	lCov := map[credHour]float64{key("seat_a", 0): 3, key("seat_a", 1): 2, key("seat_a", 2): 5, key("seat_a", 3): 3,
		key("seat_a", 4): 9, key("seat_b", 1): 7}
	spans := map[string]reportedSpan{"operator: home": {from: hr(1), to: hr(3).Add(20 * time.Minute)}}

	m := meterShare(links, spans, steps, coverage, lOut, lCov)
	at := map[credHour]ExternalMeterHour{}
	for _, h := range m.Hours {
		at[credHour{h.Hour, h.SecretKey, h.SecretSlot}] = h
	}
	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }
	for _, c := range []struct {
		k        credHour
		reported bool
		out, cov *float64
	}{
		{key("seat_a", 0), false, nil, nil},
		{key("seat_a", 1), true, usd(8 * 6.0 / 8), usd(8 * 2.0 / 8)},
		{key("seat_a", 2), true, usd(0), usd(4)},
		{key("seat_a", 3), true, usd(12 * 1.0 / 4), usd(12 * 3.0 / 4)},
		{key("seat_a", 4), false, nil, nil},
		{key("seat_b", 1), false, nil, nil},
	} {
		h, ok := at[c.k]
		if !ok || h.RealUSD == nil || h.OutsideReported != c.reported {
			t.Errorf("%v: %+v (present %v), want a reading, reported %v", c.k, h, ok, c.reported)
			continue
		}
		if c.out == nil {
			if h.ExternalRealUSD != nil || h.CoveyRealUSD != nil {
				t.Errorf("%v: split %v / %v, want no split", c.k, h.ExternalRealUSD, h.CoveyRealUSD)
			}
		} else if !near(h.ExternalRealUSD, *c.out) || !near(h.CoveyRealUSD, *c.cov) {
			t.Errorf("%v: split %v / %v, want %v / %v", c.k, h.ExternalRealUSD, h.CoveyRealUSD, *c.out, *c.cov)
		}
	}
	if _, ok := at[key("api", 1)]; ok {
		t.Errorf("the API key has meter hours")
	}

	// The dent: $36 on seat_a and $5 on seat_b. Split: hours 1-3, $24, of
	// it $9 outside. Unsplit: hours 0 and 4 of seat_a and seat_b's hour.
	if math.Abs(m.RealUSD-41) > 1e-9 || math.Abs(m.ExternalRealUSD-9) > 1e-9 || math.Abs(m.CoveyRealUSD-15) > 1e-9 ||
		math.Abs(m.UnsplitRealUSD-17) > 1e-9 || m.UnsplitHours != 3 {
		t.Errorf("dent = real %v outside %v covey %v unsplit %v (%d hours), want 41, 9, 15, 17 (3)",
			m.RealUSD, m.ExternalRealUSD, m.CoveyRealUSD, m.UnsplitRealUSD, m.UnsplitHours)
	}
	if math.Abs(m.ExternalRealUSD+m.CoveyRealUSD+m.UnsplitRealUSD-m.RealUSD) > 1e-9 {
		t.Errorf("the parts do not add up to the dent")
	}
	if !near(m.Share, 9.0/24) {
		t.Errorf("share = %v, want 9/24 of the split hours", m.Share)
	}
	// The multiples' inputs: the list prices and points of the split hours
	// only; one run of readings plus the reset in hour 3; $4 a point.
	if m.SplitExternalListUSD != 7 || m.SplitCoveyListUSD != 10 || m.SplitPoints != 6 || m.SplitRuns != 2 || m.PointRate != 4 {
		t.Errorf("split list %v / %v, points %v, runs %d, rate %v; want 7 / 10, 6, 2, 4",
			m.SplitExternalListUSD, m.SplitCoveyListUSD, m.SplitPoints, m.SplitRuns, m.PointRate)
	}
	if len(m.Credentials) != 2 || len(m.Credentials[1].Sources) != 0 || m.Credentials[1].UnsplitRealUSD != 5 ||
		m.Credentials[1].Share != nil {
		t.Errorf("credentials = %+v, want seat_b with no source, all unsplit, no share", m.Credentials)
	}

	// A reading in an unsplit hour between two split ones breaks the run:
	// one source reported hour 1, another hours 3 and 4.
	links["operator: late"] = MeterCredential{SecretKey: "seat_a", SecretSlot: 0}
	gap := meterShare(links, map[string]reportedSpan{"operator: home": {from: hr(1), to: hr(1).Add(time.Minute)},
		"operator: late": {from: hr(3), to: hr(4)}}, steps, coverage, lOut, lCov)
	// Split: hours 1, 3, 4 (two runs, broken by hour 2) plus the reset.
	if gap.SplitRuns != 3 || gap.Credentials[0].UnsplitHours != 2 {
		t.Errorf("runs = %d, unsplit hours %d; want 3 and 2", gap.SplitRuns, gap.Credentials[0].UnsplitHours)
	}
}
