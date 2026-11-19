package observability

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
		// Postgres refuses 0x00 in text: a 400 here, not a 500 at the insert.
		{"a NUL in the host", func(r *ExternalRow) { r.Host = "lap\x00top" }, "host must not contain a NUL"},
		// Above the bound, two rows in one cell overflowed the cells' sums.
		{"a count above the bound", func(r *ExternalRow) { v := int64(externalMaxTokens + 1); r.Input = &v }, "input must be at most"},
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

	big := validRow("m-big", now)
	b := int64(externalMaxTokens)
	big.Input, big.Output, big.CacheRead, big.CacheWrite5m, big.CacheWrite1h = &b, &b, &b, &b, &b
	if err := ValidateExternalRows([]ExternalRow{big}, now); err != nil {
		t.Errorf("counts at the bound: %v", err)
	}

	for _, src := range []string{"", strings.Repeat("ü", 65), "a\x00b"} {
		if ValidateExternalSource(src) == nil {
			t.Errorf("source %q passed", src)
		}
	}
	if err := ValidateExternalSource(strings.Repeat("ü", 64)); err != nil {
		t.Errorf("a 64-character source: %v", err)
	}
}

// A push's watermarks: per host, a time; the host follows the rows' rules.
func TestValidateExternalReported(t *testing.T) {
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if err := ValidateExternalReported(map[string]time.Time{"laptop": at, "desktop": at.Add(time.Hour)}); err != nil {
		t.Errorf("two hosts: %v", err)
	}
	for name, m := range map[string]map[string]time.Time{
		"an empty host": {"": at}, "a NUL": {"a\x00b": at}, "no time": {"laptop": {}},
	} {
		if ValidateExternalReported(m) == nil {
			t.Errorf("%s passed", name)
		}
	}
	many := map[string]time.Time{}
	for i := 0; i <= externalMaxHosts; i++ {
		many[fmt.Sprintf("h%d", i)] = at
	}
	if ValidateExternalReported(many) == nil {
		t.Errorf("%d hosts passed", len(many))
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

// The meter share's tests below build steps by hand: tm is a clock time on
// 2026-11-18 UTC, rate a plan price per point.
func tm(hhmm string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", "2026-11-18 "+hhmm)
	if err != nil {
		panic(err)
	}
	return t
}

func rate(v float64) *float64 { return &v }

func near(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

func nearP(got *float64, want float64) bool { return got != nil && near(*got, want) }

// Which steps are reported (#126 finding 20): every machine of the linked
// sources that pushed or had a call in the week before a reading must have
// reported through it. Without an active machine, or without watermarks,
// nothing is: the share is never split on the absence of calls.
func TestStepReported(t *testing.T) {
	at := tm("19:30")
	w := func(hhmm string) *time.Time { v := tm(hhmm); return &v }
	laptop := hostMark{ReportedThrough: w("19:40"), PushedAt: tm("19:41")}
	desktop := hostMark{ReportedThrough: w("18:30"), PushedAt: tm("18:31"), LastCall: w("17:00")}
	if !stepReported(at, []hostMark{laptop}) {
		t.Error("the laptop alone reported through 19:40: the 19:30 step is reported")
	}
	if stepReported(at, []hostMark{laptop, desktop}) {
		t.Error("the desktop, active, reported only through 18:30: the 19:30 step is not reported")
	}
	if !stepReported(tm("18:05"), []hostMark{laptop, desktop}) {
		t.Error("both reported through 18:05")
	}
	// A machine asleep for more than a week is not waited for.
	old := hostMark{ReportedThrough: w("00:00"), PushedAt: at.Add(-8 * 24 * time.Hour)}
	*old.ReportedThrough = old.PushedAt
	if !stepReported(at, []hostMark{laptop, old}) {
		t.Error("a machine idle for eight days held the step")
	}
	// A call in the week makes it active again.
	old.LastCall = w("12:00")
	if stepReported(at, []hostMark{laptop, old}) {
		t.Error("a machine with a call that day did not hold the step")
	}
	// No watermark: active by its calls, never reported.
	if stepReported(at, []hostMark{{PushedAt: tm("19:35"), LastCall: w("19:00")}}) {
		t.Error("a machine without a watermark reported the step")
	}
	// No active machine at all: nothing to say the calls were sent.
	if stepReported(at, nil) || stepReported(at, []hostMark{{ReportedThrough: w("20:00"), PushedAt: at.Add(-8 * 24 * time.Hour)}}) {
		t.Error("a step without an active machine is reported")
	}
}

// acceptanceA is cost-controller's fixture (#126, finding 20): one seat at $4
// a point, source "operator: home" with a laptop (watermark 19:40) and a
// desktop (watermark 18:30, a call at 17:00).
func acceptanceA(desktopW string) []shareStep {
	a1, a2 := uuid.MustParse("00000000-0000-0000-0000-0000000000a1"), uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	w := func(hhmm string) *time.Time { v := tm(hhmm); return &v }
	marks := []hostMark{
		{ReportedThrough: w("19:40"), PushedAt: tm("19:41")},
		{ReportedThrough: w(desktopW), PushedAt: tm(desktopW), LastCall: w("17:00")},
	}
	steps := []shareStep{
		{From: tm("14:10"), At: tm("16:10"), Points: 10, LOut: 50, LCov: 0.10, Agents: map[uuid.UUID]float64{a1: 0.10}},
		{From: tm("16:10"), At: tm("16:40"), Points: 1, LOut: 1, LCov: 1, Agents: map[uuid.UUID]float64{a2: 1}},
		{From: tm("16:40"), At: tm("18:05"), Points: 3, LOut: 3, LCov: 1, Agents: map[uuid.UUID]float64{a2: 1}},
		{From: tm("18:05"), At: tm("19:30"), Points: 2, LOut: 2, LCov: 6, Agents: map[uuid.UUID]float64{a1: 6}},
	}
	for i := range steps {
		steps[i].Rate = rate(4)
		steps[i].Reported = stepReported(steps[i].At, marks)
	}
	return steps
}

// Acceptance test (A) of #126 finding 20, figures by cost-controller,
// recomputed for F3c: the desktop's stale watermark holds the last step
// unsplit and closes the chunk before it. Chunk 1 is the first step (10
// points, closes at once): L = 50.10, k = (10 + 2) / 50.10 = 0.239521 points
// per list $, U = 0. Chunk 2 is the second and third steps: 4 points over
// L = 6 (Lo 4, Lc 2), U = 4 − 2 − 0.239521 × 6 = 0.562874 points ($2.251497,
// spread 1 : 3), the other $13.748503 split by the chunk's share 2/3 —
// not 1/2 and 3/4 per step. The parts' ranges are ½·TV of their share per
// chunk (19); the agents' real share follows the chunk's L_cov (22).
func TestSplitSeatAcceptanceA(t *testing.T) {
	seat := meterSeat{"seat", 0}
	tot, steps := splitSeat(seat, []string{"operator: home"}, acceptanceA("18:30"))
	m := meterSums([]ExternalMeterTotal{tot}, steps)

	want := []struct {
		split                   bool
		reason                  string
		out, cov, unattr, unspl float64
	}{
		{true, "", 39.920160, 0.079840, 0, 0},
		{true, "", 2.291417, 1.145709, 0.562874, 0},
		{true, "", 6.874251, 3.437126, 1.688623, 0},
		{false, SplitNotReported, 0, 0, 0, 8},
	}
	for i, w := range want {
		st := m.Steps[i]
		if st.Split != w.split || st.Reason != w.reason || !near(st.UnsplitRealUSD, w.unspl) || !near(st.UnattributedRealUSD, w.unattr) {
			t.Errorf("step %d = %+v, want split %v (%q), unattributed %v, unsplit %v", i, st, w.split, w.reason, w.unattr, w.unspl)
			continue
		}
		if w.split && (!nearP(st.ExternalRealUSD, w.out) || !nearP(st.CoveyRealUSD, w.cov)) {
			t.Errorf("step %d: out %v cov %v, want %v / %v", i, *st.ExternalRealUSD, *st.CoveyRealUSD, w.out, w.cov)
		}
		if !w.split && (st.ExternalRealUSD != nil || st.CoveyRealUSD != nil) {
			t.Errorf("step %d is not split but has parts", i)
		}
	}
	if !nearP(tot.PointsPerListUSD, 12/50.1) || !near(m.UnattributedRealUSD, 2.251497) {
		t.Errorf("k %v, unattributed %v; want 0.239521, 2.251497", tot.PointsPerListUSD, m.UnattributedRealUSD)
	}
	if !near(m.RealUSD, 64) || !near(m.ExternalRealUSD, 49.085828) || !near(m.CoveyRealUSD, 4.662675) ||
		!near(m.UnsplitRealUSD, 8) || !nearP(m.Share, 0.913250) || m.SplitPoints != 14 || m.SplitRuns != 2 {
		t.Errorf("dent %v, outside %v, covey %v, unsplit %v, share %v, split points %v, chunks %d",
			m.RealUSD, m.ExternalRealUSD, m.CoveyRealUSD, m.UnsplitRealUSD, *m.Share, m.SplitPoints, m.SplitRuns)
	}
	// The hours the steps are placed in.
	hours := map[int][5]float64{16: {44, 42.211577, 1.225549, 0.562874, 0}, 18: {12, 6.874251, 3.437126, 1.688623, 0}, 19: {8, 0, 0, 0, 8}}
	if len(m.Hours) != len(hours) {
		t.Fatalf("hours = %+v", m.Hours)
	}
	for _, h := range m.Hours {
		w := hours[h.Hour.Hour()]
		if !nearP(h.RealUSD, w[0]) || !near(h.ExternalRealUSD, w[1]) || !near(h.CoveyRealUSD, w[2]) ||
			!near(h.UnattributedRealUSD, w[3]) || !near(h.UnsplitRealUSD, w[4]) {
			t.Errorf("hour %v = %+v, want %v", h.Hour, h, w)
		}
	}
	// The multiples: list ÷ real, and their floor list ÷ (real + slack).
	split := m.ExternalRealUSD + m.CoveyRealUSD
	list := m.SplitExternalListUSD + m.SplitCoveyListUSD
	check := func(name string, l, r, slack, value, low float64) {
		t.Helper()
		if !near(l/r, value) || math.Abs(l/(r+slack)-low) > 5e-6 {
			t.Errorf("%s: %.5f (≥ %.5f), want %.5f (≥ %.5f)", name, l/r, l/(r+slack), value, low)
		}
	}
	check("together", list, split, m.SplitSlackUSD, 1.04375, 0.90852)
	check("outside", m.SplitExternalListUSD, m.ExternalRealUSD, m.SplitExternalSlackUSD, 1.100114, 0.96871)
	check("covey", m.SplitCoveyListUSD, m.CoveyRealUSD, m.SplitCoveySlackUSD, 0.450385, 0.34977)
	// Per chunk at $4 a point: chunk 1 4 × (1, 0.998004, 0.001996), chunk 2
	// 4 × (1, 2/3, 1/3).
	if !near(m.SplitExternalSlackUSD, 6.658683) || !near(m.SplitCoveySlackUSD, 1.341317) || !near(m.SplitSlackUSD, 8) {
		t.Errorf("slack outside %v covey %v whole %v, want 6.658683, 1.341317, 8", m.SplitExternalSlackUSD, m.SplitCoveySlackUSD, m.SplitSlackUSD)
	}
	// The agents: a1 has chunk 1's covey part and $6 of list in the
	// unsplit step; a2 chunk 2's.
	agents := map[string]ExternalMeterAgent{}
	for _, a := range m.Credentials[0].Agents {
		agents[a.AgentID.String()[34:]] = a
	}
	if a := agents["a1"]; !near(a.RealUSD, 0.079840) || !near(a.SplitListUSD, 0.10) || !near(a.UnsplitListUSD, 6) {
		t.Errorf("a1 = %+v, want real 0.079840, split list 0.10, unsplit list 6", a)
	}
	if a := agents["a2"]; !near(a.RealUSD, 4.582834) || !near(a.SplitListUSD, 2) || a.UnsplitListUSD != 0 {
		t.Errorf("a2 = %+v, want real 4.582834, split list 2", a)
	}
	if !near(agents["a1"].RealUSD+agents["a2"].RealUSD, m.CoveyRealUSD) {
		t.Error("the agents do not add up to covey's part")
	}

	// Variant: the desktop's watermark at 18:00 holds the third step too.
	tot, steps = splitSeat(seat, []string{"operator: home"}, acceptanceA("18:00"))
	if !near(tot.UnsplitRealUSD, 20) || tot.SplitPoints != 11 {
		t.Errorf("desktop at 18:00: unsplit %v, split points %v; want 20 and 11", tot.UnsplitRealUSD, tot.SplitPoints)
	}
	// Variant: without the desktop (no 17:00 call, no watermark), the
	// laptop's 19:40 covers all four steps.
	in := acceptanceA("18:30")
	laptop := []hostMark{{ReportedThrough: func() *time.Time { v := tm("19:40"); return &v }(), PushedAt: tm("19:41")}}
	for i := range in {
		in[i].Reported = stepReported(in[i].At, laptop)
	}
	if tot, _ := splitSeat(seat, nil, in); tot.SplitSteps != 4 || tot.UnsplitRealUSD != 0 {
		t.Errorf("laptop only: %d split steps, unsplit %v; want 4 and 0", tot.SplitSteps, tot.UnsplitRealUSD)
	}
}

// Acceptance test (B), finding 19 alone, under F3c: a part's range is ½·TV
// of its share over each chunk. One step all outside, one all covey, two
// points each, are one chunk at share ½ (it closes at 5 points): each part
// ±0.5 point, the whole ±1 (it was ±1 each with the per-step shares 1 and
// 0). With five points each they are two chunks of shares 1 and 0: each
// part ±1, the whole ±1 per chunk.
func TestSplitSeatPartRanges(t *testing.T) {
	for _, c := range []struct {
		points                      float64
		outside, covey, whole, runs float64
	}{{2, 0.5, 0.5, 1, 1}, {5, 1, 1, 2, 2}} {
		steps := []shareStep{
			{From: tm("10:00"), At: tm("11:00"), Points: c.points, Rate: rate(1), LOut: 5, Reported: true},
			{From: tm("11:00"), At: tm("12:00"), Points: c.points, Rate: rate(1), LCov: 5, Reported: true},
		}
		tot, _ := splitSeat(meterSeat{"seat", 0}, nil, steps)
		if !near(tot.SplitExternalSlackPoints, c.outside) || !near(tot.SplitCoveySlackPoints, c.covey) ||
			!near(tot.SplitSlackPoints, c.whole) || float64(tot.SplitRuns) != c.runs {
			t.Errorf("%v points each: slack outside %v covey %v whole %v (chunks %d), want %v, %v, %v (%v)", c.points,
				tot.SplitExternalSlackPoints, tot.SplitCoveySlackPoints, tot.SplitSlackPoints, tot.SplitRuns,
				c.outside, c.covey, c.whole, c.runs)
		}
	}
}

// Acceptance test (C): the web fixture of #126 (HOURS in
// SubscriptionDent.test.tsx) as steps: 4 points, L_out 300 / L_cov 151 (a1
// 101, a2 50); 2 points, 155 / 152 (a2); 3 points not reported (a1 40).
// Under F3c the first two are one chunk: 6 points over L = 758 (Lo 455, Lc
// 303), no rate (fewer than 10 points), so R = 6 × $0.461538 splits at
// 455/758: outside 1.662269, covey 1.106962; a1 gets 101/303 of covey's,
// a2 202/303. The parts' slack: one chunk at $0.461538 a point, times the
// share.
func TestSplitSeatWebFixture(t *testing.T) {
	pp := 200.0 * 12 / 52 / 100
	a1, a2 := uuid.New(), uuid.New()
	steps := []shareStep{
		{From: tm("17:40"), At: tm("18:40"), Points: 4, Rate: rate(pp), LOut: 300, LCov: 151,
			Agents: map[uuid.UUID]float64{a1: 101, a2: 50}, Reported: true},
		{From: tm("18:40"), At: tm("19:40"), Points: 2, Rate: rate(pp), LOut: 155, LCov: 152,
			Agents: map[uuid.UUID]float64{a2: 152}, Reported: true},
		{From: tm("19:40"), At: tm("20:40"), Points: 3, Rate: rate(pp), LCov: 40, Agents: map[uuid.UUID]float64{a1: 40}},
	}
	tot, _ := splitSeat(meterSeat{"seat", 0}, []string{"operator: home"}, steps)
	got := map[uuid.UUID]ExternalMeterAgent{}
	for _, a := range tot.Agents {
		got[a.AgentID] = a
	}
	if !near(got[a1].RealUSD, 0.368987) || !near(got[a2].RealUSD, 0.737974) || !near(got[a1].UnsplitListUSD, 40) {
		t.Errorf("a1 %+v, a2 %+v; want 0.368987 (unsplit list 40) and 0.737974", got[a1], got[a2])
	}
	if !near(tot.SplitExternalSlackUSD, 0.277045) || !near(tot.SplitCoveySlackUSD, 0.184494) {
		t.Errorf("slack outside %v covey %v, want 0.277045 and 0.184494", tot.SplitExternalSlackUSD, tot.SplitCoveySlackUSD)
	}
	if !near(tot.ExternalRealUSD, 1.662269) || !near(tot.CoveyRealUSD, 1.106962) || !near(tot.UnsplitRealUSD, 1.384615) {
		t.Errorf("outside %v covey %v unsplit %v", tot.ExternalRealUSD, tot.CoveyRealUSD, tot.UnsplitRealUSD)
	}
}

// Acceptance test (D), finding 21: a reported step with no list on either
// side that moved the meter is nobody's, not covey's whole. Alone (a chunk
// with no list, F3c) it is unattributed whole, not "not split". A reading
// that did not move either is idle: no money, and the chunk goes on
// through it.
func TestSplitSeatMovedWithoutACall(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 1, Rate: rate(4), Reported: true},
	})
	if s := steps[0]; s.Split || s.Reason != SplitNoReportedCall || s.UnattributedRealUSD != 4 || s.UnattributedPoints != 1 ||
		s.UnsplitRealUSD != 0 || tot.CoveyRealUSD != 0 || tot.UnsplitRealUSD != 0 || tot.UnsplitSteps != 0 ||
		tot.UnattributedRealUSD != 4 || tot.UnattributedSteps != 1 || tot.UnattributedNoCallRealUSD != 4 || tot.SplitRuns != 0 {
		t.Errorf("step %+v, covey %v, unsplit %v (%d steps), unattributed %v (%d steps); want unattributed 4 in 1 (%s)",
			s, tot.CoveyRealUSD, tot.UnsplitRealUSD, tot.UnsplitSteps, tot.UnattributedRealUSD, tot.UnattributedSteps, SplitNoReportedCall)
	}

	// Idle readings between two split ones: one chunk, the same range as
	// without them.
	run := func(idle bool) ExternalMeterTotal {
		in := []shareStep{{From: tm("10:00"), At: tm("11:00"), Points: 2, Rate: rate(1), LOut: 1, LCov: 1, Reported: true}}
		if idle {
			in = append(in, shareStep{From: tm("11:00"), At: tm("11:30"), Rate: rate(1), Reported: true})
		}
		in = append(in, shareStep{From: tm("11:30"), At: tm("12:00"), Points: 2, Rate: rate(1), LOut: 3, LCov: 1, Reported: true})
		tot, _ := splitSeat(meterSeat{"seat", 0}, []string{"src"}, in)
		return tot
	}
	with, without := run(true), run(false)
	if with.SplitRuns != 1 || with.SplitExternalSlackPoints != without.SplitExternalSlackPoints || with.SplitSteps != 2 ||
		with.UnsplitSteps != 0 {
		t.Errorf("with an idle reading: %d runs, slack %v (%v without), %d split steps, %d unsplit", with.SplitRuns,
			with.SplitExternalSlackPoints, without.SplitExternalSlackPoints, with.SplitSteps, with.UnsplitSteps)
	}
}

// chunkFixture is F3c's case at $1 a point. Chunk 1: 4 points over L_out 40
// / L_cov 10 (agent x), then 6 points over 20 / 30 (agent y) — 10 points
// over $100 (Lo 60, Lc 40), the clean chunk. A reading the outside sessions
// had not reported (1 point, y $5) closes it. Chunk 2: 20 points over 50 /
// 30 (x). Chunk 3: 10 points over L_cov 20 (y). Another unreported reading
// (1 point, y $5), then chunk 4: 2 points with no call on either side, the
// last — with no chunk next to it, so not folded into chunk 3.
func chunkFixture() []shareStep {
	x, y := uuid.MustParse("00000000-0000-0000-0000-0000000000a1"), uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	return []shareStep{
		{From: tm("08:00"), At: tm("08:30"), Points: 4, Rate: rate(1), LOut: 40, LCov: 10, Agents: map[uuid.UUID]float64{x: 10}, Reported: true},
		{From: tm("08:30"), At: tm("09:30"), Points: 6, Rate: rate(1), LOut: 20, LCov: 30, Agents: map[uuid.UUID]float64{y: 30}, Reported: true},
		{From: tm("09:30"), At: tm("10:30"), Points: 1, Rate: rate(1), LCov: 5, Agents: map[uuid.UUID]float64{y: 5}},
		{From: tm("10:30"), At: tm("11:30"), Points: 20, Rate: rate(1), LOut: 50, LCov: 30, Agents: map[uuid.UUID]float64{x: 30}, Reported: true},
		{From: tm("11:30"), At: tm("12:30"), Points: 10, Rate: rate(1), LCov: 20, Agents: map[uuid.UUID]float64{y: 20}, Reported: true},
		{From: tm("12:30"), At: tm("13:00"), Points: 1, Rate: rate(1), LCov: 5, Agents: map[uuid.UUID]float64{y: 5}},
		{From: tm("13:00"), At: tm("13:30"), Points: 2, Rate: rate(1), Reported: true},
	}
}

// F3c (#120): k = min over the complete chunks of 10 points or more of
// (P_c + 2) / L_c = min(12/100, 22/80, 12/20) = 0.12 point per list $.
// Chunk 1 (k's own) keeps U = 0 and splits at its share 0.6 — 2.4 / 1.6 and
// 3.6 / 2.4, where the readings' own shares (0.8 and 0.4) would give 3.2 /
// 0.8 and 2.4 / 3.6. Chunk 2: U = 20 − 2 − 0.12 × 80 = 8.4, the other 11.6
// at 0.625. Chunk 3: U = 10 − 2 − 2.4 = 5.6, the other 4.4 covey's. Chunk 4
// has no list: its 2 points are unattributed whole (no call). The
// unreported reading stays not split. Every step: covey + outside +
// unattributed + unsplit = R.
func TestSplitSeatUnattributed(t *testing.T) {
	for _, sources := range [][]string{{"operator: home"}, nil} {
		// A seat no source names gets the same unattributed part: another
		// machine moves its meter all the same.
		tot, steps := splitSeat(meterSeat{"seat", 0}, sources, chunkFixture())
		if !nearP(tot.PointsPerListUSD, 0.12) || len(tot.PlanRates) != 1 || !nearP(tot.PlanRates[0].PointsPerListUSD, 0.12) ||
			tot.PlanRates[0].USDPerPoint != 1 {
			t.Fatalf("sources %v: k = %v, plan rates %+v; want 0.12 at $1 a point", sources, tot.PointsPerListUSD, tot.PlanRates)
		}
		want := []struct {
			reason                    string
			split                     bool
			out, cov, unattr, unsplit float64
		}{
			{"", true, 2.4, 1.6, 0, 0},
			{"", true, 3.6, 2.4, 0, 0},
			{SplitNotReported, false, 0, 0, 0, 1},
			{"", true, 7.25, 4.35, 8.4, 0},
			{"", true, 0, 4.4, 5.6, 0},
			{SplitNotReported, false, 0, 0, 0, 1},
			{SplitNoReportedCall, false, 0, 0, 2, 0},
		}
		for i, w := range want {
			s := steps[i]
			var out, cov float64
			if s.ExternalRealUSD != nil {
				out, cov = *s.ExternalRealUSD, *s.CoveyRealUSD
			}
			if s.Reason != w.reason || s.Split != w.split || !near(out, w.out) || !near(cov, w.cov) ||
				!near(s.UnattributedRealUSD, w.unattr) || !near(s.UnsplitRealUSD, w.unsplit) {
				t.Errorf("sources %v, step %d = %+v (out %v cov %v), want %+v", sources, i, s, out, cov, w)
			}
			if !near(out+cov+s.UnattributedRealUSD+s.UnsplitRealUSD, *s.RealUSD) || !near(s.UnattributedPoints, s.UnattributedRealUSD) {
				t.Errorf("sources %v, step %d: the parts do not add up to R = %v", sources, i, *s.RealUSD)
			}
		}
		if !near(tot.RealUSD, 44) || !near(tot.UnattributedRealUSD, 16) || !near(tot.UnattributedPoints, 16) ||
			tot.UnattributedSteps != 3 || !near(tot.UnattributedNoCallRealUSD, 2) || !near(tot.UnattributedNoCallPoints, 2) ||
			!near(tot.UnsplitRealUSD, 2) || tot.UnsplitSteps != 2 || !near(tot.CoveyRealUSD, 12.75) || !near(tot.ExternalRealUSD, 13.25) ||
			!near(tot.CoveyRealUSD+tot.ExternalRealUSD+tot.UnattributedRealUSD+tot.UnsplitRealUSD, tot.RealUSD) {
			t.Errorf("sources %v: real %v covey %v outside %v unattributed %v (%v points, %d steps, no call %v) unsplit %v (%d)", sources,
				tot.RealUSD, tot.CoveyRealUSD, tot.ExternalRealUSD, tot.UnattributedRealUSD, tot.UnattributedPoints,
				tot.UnattributedSteps, tot.UnattributedNoCallRealUSD, tot.UnsplitRealUSD, tot.UnsplitSteps)
		}
		// The split points: the four split steps' whole, the attributed points
		// by their chunk's share; the lists stay the multiples' numerators;
		// SplitRuns counts the three chunks with list.
		if tot.SplitPoints != 40 || !near(tot.SplitExternalPoints, 13.25) || !near(tot.SplitCoveyPoints, 12.75) ||
			tot.SplitExternalListUSD != 110 || tot.SplitCoveyListUSD != 90 || tot.SplitRuns != 3 || tot.SplitSteps != 4 {
			t.Errorf("sources %v: split points %v (outside %v covey %v), lists %v / %v, chunks %d, split steps %d", sources, tot.SplitPoints,
				tot.SplitExternalPoints, tot.SplitCoveyPoints, tot.SplitExternalListUSD, tot.SplitCoveyListUSD, tot.SplitRuns, tot.SplitSteps)
		}
		// The ranges, one term per chunk at $1 a point: whole 3, outside
		// 0.6 + 0.625 + 0, covey 0.4 + 0.375 + 1.
		if !near(tot.SplitSlackPoints, 3) || !near(tot.SplitExternalSlackPoints, 1.225) || !near(tot.SplitCoveySlackPoints, 1.775) ||
			!near(tot.SplitSlackUSD, 3) || !near(tot.SplitExternalSlackUSD, 1.225) || !near(tot.SplitCoveySlackUSD, 1.775) {
			t.Errorf("sources %v: slack %v / %v / %v points, %v / %v / %v $", sources, tot.SplitSlackPoints, tot.SplitExternalSlackPoints,
				tot.SplitCoveySlackPoints, tot.SplitSlackUSD, tot.SplitExternalSlackUSD, tot.SplitCoveySlackUSD)
		}
		// The agents follow the chunks' L_cov: in chunk 1 x has 10/40 and y
		// 30/40 of covey's $4 (per step it would be 2.4 and 1.6), then x
		// chunk 2's 4.35 and y chunk 3's 4.4; y's $5 in each unreported step
		// is unsplit list.
		agents := map[string]ExternalMeterAgent{}
		for _, a := range tot.Agents {
			agents[a.AgentID.String()[34:]] = a
		}
		if x, y := agents["a1"], agents["a2"]; !near(x.RealUSD, 5.35) || !near(y.RealUSD, 7.4) ||
			!near(x.RealUSD+y.RealUSD, tot.CoveyRealUSD) || y.UnsplitListUSD != 10 || x.SplitListUSD != 40 || y.SplitListUSD != 50 {
			t.Errorf("sources %v: agents x %+v y %+v, want real 5.35 and 7.4", sources, x, y)
		}
		m := meterSums([]ExternalMeterTotal{tot}, steps)
		if !near(m.UnattributedRealUSD, 16) || m.UnattributedSteps != 3 || !near(m.UnattributedPoints, 16) || !near(m.UnattributedNoCallRealUSD, 2) {
			t.Errorf("sums: unattributed %v in %d steps (no call %v)", m.UnattributedRealUSD, m.UnattributedSteps, m.UnattributedNoCallRealUSD)
		}
		seen := false
		for _, h := range m.Hours {
			if h.Hour.Hour() == 11 {
				seen = true
				if !near(h.UnattributedRealUSD, 8.4) || h.UnattributedSteps != 1 || h.SplitSteps != 1 {
					t.Errorf("hour 11 = %+v, want 8.4 unattributed in its one split step", h)
				}
			}
		}
		if !seen {
			t.Errorf("no hour 11 in %+v", m.Hours)
		}
	}
}

// F3c beside finding 26: an idle reading inside chunk 1 (no plan, no
// movement, no list) adds a range entry at its own price (0) with the
// chunk's share, but no points and no list: k, U and the parts are as
// without it, the points ranges too, and the chunk stays one chunk.
func TestSplitSeatUnattributedIdleInARun(t *testing.T) {
	plain := chunkFixture()
	idle := append(append(append([]shareStep{}, plain[:1]...),
		shareStep{From: tm("08:30"), At: tm("08:45"), Reported: true}), plain[1:]...)
	idle[2].From = tm("08:45")
	a, _ := splitSeat(meterSeat{"seat", 0}, []string{"src"}, plain)
	b, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, idle)
	if steps[1].Reason != SplitIdle || steps[1].UnattributedRealUSD != 0 || steps[1].Split {
		t.Fatalf("the idle step = %+v", steps[1])
	}
	if b.PointsPerListUSD == nil || !near(*b.PointsPerListUSD, *a.PointsPerListUSD) || !near(b.UnattributedRealUSD, a.UnattributedRealUSD) ||
		!near(b.CoveyRealUSD, a.CoveyRealUSD) || !near(b.ExternalRealUSD, a.ExternalRealUSD) || b.SplitRuns != a.SplitRuns ||
		!near(b.SplitSlackPoints, a.SplitSlackPoints) || !near(b.SplitExternalSlackPoints, a.SplitExternalSlackPoints) {
		t.Errorf("with the idle step: k %v unattributed %v covey %v outside %v chunks %d slack %v / %v; without: k %v %v %v %v %d %v / %v",
			b.PointsPerListUSD, b.UnattributedRealUSD, b.CoveyRealUSD, b.ExternalRealUSD, b.SplitRuns, b.SplitSlackPoints,
			b.SplitExternalSlackPoints, *a.PointsPerListUSD, a.UnattributedRealUSD, a.CoveyRealUSD, a.ExternalRealUSD, a.SplitRuns,
			a.SplitSlackPoints, a.SplitExternalSlackPoints)
	}
	// Its $ coefficient is 0 inside chunk 1 ($1 a point around it): the
	// whole's $ range for that chunk grows from ½(1 + 0 + 1) to
	// ½(1 + 1 + 1 + 1).
	if !near(b.SplitSlackUSD, a.SplitSlackUSD+1) {
		t.Errorf("$ slack %v, want %v + 1", b.SplitSlackUSD, a.SplitSlackUSD)
	}
}

// Without a chunk of 10 points k is unknown (nil): nothing with list is
// unattributed. The step that moved with no call is folded into its chunk
// (3 + 1 + 3 points over L = 3, Lo 1: share 1/3) and split with it; the
// last chunk (4 points over 9 / 1) splits at 0.9.
func TestSplitSeatUnattributedWithoutARate(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 3, Rate: rate(1), LOut: 1, LCov: 1, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 1, Rate: rate(1), LCov: 1, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 3, Rate: rate(1), Reported: true},
		{From: tm("13:00"), At: tm("14:00"), Points: 4, Rate: rate(1), LOut: 9, LCov: 1, Reported: true},
	})
	if tot.PointsPerListUSD != nil || len(tot.PlanRates) != 1 || tot.PlanRates[0].PointsPerListUSD != nil {
		t.Errorf("k = %v, plan rates %+v; want nil: the chunks move 7 and 4 points", tot.PointsPerListUSD, tot.PlanRates)
	}
	if tot.UnattributedRealUSD != 0 || tot.UnattributedSteps != 0 || steps[2].Reason != SplitNoReportedCall || !steps[2].Split ||
		!nearP(steps[0].ExternalRealUSD, 1) || !nearP(steps[2].ExternalRealUSD, 1) || !nearP(steps[2].CoveyRealUSD, 2) ||
		!nearP(steps[3].ExternalRealUSD, 3.6) || !near(tot.CoveyRealUSD+tot.ExternalRealUSD, 11) || tot.SplitRuns != 2 || tot.SplitSteps != 4 {
		t.Errorf("unattributed %v in %d steps, chunks %d, steps %+v; want none, the no-call step split with its chunk",
			tot.UnattributedRealUSD, tot.UnattributedSteps, tot.SplitRuns, steps)
	}
}

// The truncation bias F3c removes: ten readings at $1 a point, covey's list
// ($10 each) in the five that moved 0 points, the outside sessions' ($10
// each) in the five that ticked +1. Split per reading, covey's part is 0
// and the outside's all 5 points; per chunk (one of 5 points, L = 100 at
// share ½) each part is 2.5, as the list says.
func TestSplitSeatChunkShareNotReadingShare(t *testing.T) {
	x := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	var in []shareStep
	at := tm("10:00")
	for i := 0; i < 10; i++ {
		st := shareStep{From: at, At: at.Add(10 * time.Minute), Rate: rate(1), Reported: true}
		if i%2 == 0 {
			st.LCov, st.Agents = 10, map[uuid.UUID]float64{x: 10}
		} else {
			st.Points, st.LOut = 1, 10
		}
		in = append(in, st)
		at = st.At
	}
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, in)
	if !near(tot.CoveyRealUSD, 2.5) || !near(tot.ExternalRealUSD, 2.5) || tot.SplitRuns != 1 || tot.SplitSteps != 10 ||
		tot.UnattributedRealUSD != 0 || len(tot.Agents) != 1 || !near(tot.Agents[0].RealUSD, 2.5) {
		t.Errorf("covey %v outside %v (chunks %d, split steps %d, unattributed %v, agents %+v); want 2.5 / 2.5 in one chunk",
			tot.CoveyRealUSD, tot.ExternalRealUSD, tot.SplitRuns, tot.SplitSteps, tot.UnattributedRealUSD, tot.Agents)
	}
	// Each reading that ticked carries ½ point of covey's.
	if !nearP(steps[1].CoveyRealUSD, 0.5) || !nearP(steps[1].ExternalRealUSD, 0.5) || !nearP(steps[0].CoveyRealUSD, 0) {
		t.Errorf("steps %+v / %+v", steps[0], steps[1])
	}
}

// Meter lag: a +1 reading with no call right after use is folded into the
// chunk (2 + 2 + 1 points over L = 4 at share ½) and split with it, not
// unattributed whole as F3b had it; it keeps its reason.
func TestSplitSeatLagFoldedIntoChunk(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("10:10"), Points: 2, Rate: rate(1), LOut: 1, LCov: 1, Reported: true},
		{From: tm("10:10"), At: tm("10:20"), Points: 2, Rate: rate(1), LOut: 1, LCov: 1, Reported: true},
		{From: tm("10:20"), At: tm("10:30"), Points: 1, Rate: rate(1), Reported: true},
	})
	if s := steps[2]; !s.Split || s.Reason != SplitNoReportedCall || s.UnattributedRealUSD != 0 ||
		!nearP(s.ExternalRealUSD, 0.5) || !nearP(s.CoveyRealUSD, 0.5) {
		t.Errorf("the lagging reading = %+v, want split ½ / ½ (%s)", s, SplitNoReportedCall)
	}
	if tot.UnattributedRealUSD != 0 || tot.UnattributedSteps != 0 || tot.SplitSteps != 3 || tot.SplitPoints != 5 ||
		!near(tot.CoveyRealUSD, 2.5) || !near(tot.ExternalRealUSD, 2.5) || tot.SplitRuns != 1 {
		t.Errorf("unattributed %v (%d steps), split steps %d points %v, covey %v outside %v, chunks %d", tot.UnattributedRealUSD,
			tot.UnattributedSteps, tot.SplitSteps, tot.SplitPoints, tot.CoveyRealUSD, tot.ExternalRealUSD, tot.SplitRuns)
	}
}

// The meter's lag after a chunk that closed at its size (on staging, lone
// +1 readings with no call right after use): a run of readings with no list
// that follows it directly joins it, rather than standing as a chunk with
// no list, unattributed whole. At $1 a point:
//
//   - 5 points over 2 / 2 close the chunk; +1 with no call, an idle reading,
//     +1 with no call fold into it: 7 points at share ½, nothing
//     unattributed (16739dd4 and 616d6c88: 2 unattributed).
//   - with an unreported reading between, the two no-call points have no
//     chunk next to them: unattributed whole;
//   - at another plan price ($2) the no-call point is not that chunk's:
//     unattributed whole ($2);
//   - k is taken before the fold: 10 points over $100 (k 12/100), +3 with no
//     call folded in (k would be 15/100), an unreported reading, 10 points
//     over $50: U = 10 − 2 − 0.12 × 50 = 2 (0.5 with k after the fold); the
//     folded chunk has U = max(0, 13 − 2 − 12) = 0, its no-call points
//     split ½ / ½.
func TestSplitSeatLagAfterClosedChunk(t *testing.T) {
	at := tm("10:00")
	mk := func(specs ...shareStep) []shareStep {
		for i := range specs {
			specs[i].From, specs[i].At = at.Add(time.Duration(i)*10*time.Minute), at.Add(time.Duration(i+1)*10*time.Minute)
			if specs[i].Rate == nil {
				specs[i].Rate = rate(1)
			}
		}
		return specs
	}
	used := shareStep{Points: 5, LOut: 2, LCov: 2, Reported: true}
	noCall := shareStep{Points: 1, Reported: true}
	idle := shareStep{Reported: true}
	unreported := shareStep{Points: 1, LCov: 1}

	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, mk(used, noCall, idle, noCall))
	if tot.UnattributedRealUSD != 0 || !near(tot.CoveyRealUSD, 3.5) || !near(tot.ExternalRealUSD, 3.5) || tot.SplitRuns != 1 ||
		tot.SplitSteps != 3 || !steps[3].Split || steps[3].Reason != SplitNoReportedCall || !nearP(steps[3].CoveyRealUSD, 0.5) {
		t.Errorf("folded: unattributed %v, covey %v outside %v, chunks %d, split steps %d, last %+v; want 0, 3.5, 3.5, 1, 3",
			tot.UnattributedRealUSD, tot.CoveyRealUSD, tot.ExternalRealUSD, tot.SplitRuns, tot.SplitSteps, steps[3])
	}

	tot, _ = splitSeat(meterSeat{"seat", 0}, []string{"src"}, mk(used, unreported, noCall, noCall))
	if !near(tot.UnattributedRealUSD, 2) || !near(tot.UnattributedNoCallRealUSD, 2) || !near(tot.CoveyRealUSD, 2.5) {
		t.Errorf("after an unreported reading: unattributed %v (no call %v), covey %v; want 2, 2, 2.5",
			tot.UnattributedRealUSD, tot.UnattributedNoCallRealUSD, tot.CoveyRealUSD)
	}

	otherPlan := noCall
	otherPlan.Rate = rate(2)
	tot, _ = splitSeat(meterSeat{"seat", 0}, []string{"src"}, mk(used, otherPlan))
	if !near(tot.UnattributedNoCallRealUSD, 2) || !near(tot.CoveyRealUSD, 2.5) {
		t.Errorf("at another plan price: unattributed with no call %v, covey %v; want 2, 2.5", tot.UnattributedNoCallRealUSD, tot.CoveyRealUSD)
	}

	tot, steps = splitSeat(meterSeat{"seat", 0}, []string{"src"}, mk(
		shareStep{Points: 10, LOut: 50, LCov: 50, Reported: true}, shareStep{Points: 3, Reported: true}, unreported,
		shareStep{Points: 10, LOut: 25, LCov: 25, Reported: true}))
	if !nearP(tot.PointsPerListUSD, 0.12) || !near(steps[3].UnattributedPoints, 2) || steps[1].UnattributedPoints != 0 ||
		!nearP(steps[1].ExternalRealUSD, 1.5) || !near(tot.UnattributedPoints, 2) {
		t.Errorf("k %v, U %v / %v, folded step %+v; want 0.12, 2 in the last chunk, the folded 3 points split ½ / ½",
			tot.PointsPerListUSD, steps[1].UnattributedPoints, steps[3].UnattributedPoints, steps[1])
	}
}

// A reset closes the chunk, and the reset step's points and list stay out
// of k: its span holds calls whose points went with the old week. At $1 a
// point: chunk 1, 10 points over $100 (k 12/100); the reset step, 3 points
// over $200, opens chunk 2, which closes with the next 10 points over $50
// (k 12/50 without the reset step, 15/250 = 0.06 with it); chunk 3, 10
// points over $50. k = 0.12, so chunk 3 has U = 10 − 2 − 6 = 2 (5 at 0.06).
// The reset step still takes chunk 2's share.
func TestSplitSeatResetOutOfRate(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 10, Rate: rate(1), LOut: 50, LCov: 50, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 3, Rate: rate(1), LOut: 100, LCov: 100, Reset: true, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 10, Rate: rate(1), LOut: 25, LCov: 25, Reported: true},
		{From: tm("13:00"), At: tm("14:00"), Points: 10, Rate: rate(1), LOut: 25, LCov: 25, Reported: true},
	})
	if !nearP(tot.PointsPerListUSD, 0.12) || !near(tot.UnattributedPoints, 2) || !near(steps[3].UnattributedPoints, 2) {
		t.Errorf("k %v, unattributed %v points (chunk 3: %v); want 0.12 and 2", tot.PointsPerListUSD, tot.UnattributedPoints,
			steps[3].UnattributedPoints)
	}
	if s := steps[1]; !s.Split || !nearP(s.ExternalRealUSD, 1.5) || tot.SplitRuns != 3 {
		t.Errorf("the reset step = %+v (chunks %d), want split 1.5 / 1.5 in chunk 2", s, tot.SplitRuns)
	}
}

// k per plan price: points are percent of the plan's weekly limit, so a
// change of plan closes the chunk and each price has its own k. $1 a point:
// 10 points over $100 (k 0.12), later 10 over $50 (U = 10 − 2 − 6 = 2, $2).
// $2 a point: 10 over $200 (k 0.06), later 10 over $100 (U = 10 − 2 − 6 =
// 2, $4). One k for the seat (0.06) would give the $1 chunk U = 5. Then 2
// points at each price: two chunks, not one of 4 points.
func TestSplitSeatRatePerPlan(t *testing.T) {
	tot, _ := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 10, Rate: rate(1), LOut: 50, LCov: 50, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 10, Rate: rate(2), LOut: 100, LCov: 100, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 10, Rate: rate(1), LOut: 25, LCov: 25, Reported: true},
		{From: tm("13:00"), At: tm("14:00"), Points: 10, Rate: rate(2), LOut: 50, LCov: 50, Reported: true},
		{From: tm("14:00"), At: tm("15:00"), Points: 2, Rate: rate(1), LOut: 10, LCov: 10, Reported: true},
		{From: tm("15:00"), At: tm("16:00"), Points: 2, Rate: rate(2), LOut: 10, LCov: 10, Reported: true},
	})
	if len(tot.PlanRates) != 2 || tot.PlanRates[0].USDPerPoint != 1 || !nearP(tot.PlanRates[0].PointsPerListUSD, 0.12) ||
		tot.PlanRates[1].USDPerPoint != 2 || !nearP(tot.PlanRates[1].PointsPerListUSD, 0.06) || !nearP(tot.PointsPerListUSD, 0.06) {
		t.Errorf("plan rates %+v, k %v; want $1: 0.12, $2: 0.06, the seat's (latest) 0.06", tot.PlanRates, tot.PointsPerListUSD)
	}
	if !near(tot.UnattributedPoints, 4) || !near(tot.UnattributedRealUSD, 6) || tot.SplitRuns != 6 {
		t.Errorf("unattributed %v points $%v, chunks %d; want 4, 6, 6", tot.UnattributedPoints, tot.UnattributedRealUSD, tot.SplitRuns)
	}
}

// A chunk whose use is not all in its list — outside calls without a price
// beside priced ones, or a covey booking without a line — sets no rate and
// has no unattributed part: that use would land in U. At $1 a point: 10
// points over $100 (k 0.12); 10 points over $200 with a booking without a
// line (it would set k 0.06); 20 points over $50 with unpriced outside calls
// (U would be 20 − 2 − 6 = 12). Unreported readings between them close the
// chunks. Nothing is unattributed, and the last chunk splits at ½.
func TestSplitSeatIncompleteChunks(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 10, Rate: rate(1), LOut: 50, LCov: 50, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 1, Rate: rate(1), LCov: 1},
		{From: tm("12:00"), At: tm("13:00"), Points: 10, Rate: rate(1), LOut: 100, LCov: 100, UnlinedBookings: 1, Reported: true},
		{From: tm("13:00"), At: tm("14:00"), Points: 1, Rate: rate(1), LCov: 1},
		{From: tm("14:00"), At: tm("15:00"), Points: 20, Rate: rate(1), LOut: 25, LCov: 25, UnpricedCalls: 5, Reported: true},
	})
	if !nearP(tot.PointsPerListUSD, 0.12) || tot.UnattributedRealUSD != 0 || tot.IncompleteChunks != 2 {
		t.Errorf("k %v, unattributed %v, incomplete chunks %d; want 0.12, 0, 2", tot.PointsPerListUSD, tot.UnattributedRealUSD,
			tot.IncompleteChunks)
	}
	if !nearP(steps[4].ExternalRealUSD, 10) || !nearP(steps[4].CoveyRealUSD, 10) {
		t.Errorf("the chunk with unpriced calls = %+v, want 10 / 10", steps[4])
	}
	m := meterSums([]ExternalMeterTotal{tot}, steps)
	if m.IncompleteChunks != 2 {
		t.Errorf("sums: %d incomplete chunks", m.IncompleteChunks)
	}
}

// S_c = 2 (truncation and one point of lag), and k only from 10 points. At
// $1 a point: 6 points over $100 sets no rate (it would set 7/100 at 5
// points and S 1); an unreported reading closes it; 10 points over $50 set
// k = 12/50 = 0.24 (11/50 with S 1); 10 points over $30 then have U = 10 −
// 2 − 7.2 = 0.8 (1.8 with S 1).
func TestSplitSeatRateSlack(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 6, Rate: rate(1), LOut: 50, LCov: 50, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 1, Rate: rate(1), LCov: 1},
		{From: tm("12:00"), At: tm("13:00"), Points: 10, Rate: rate(1), LOut: 25, LCov: 25, Reported: true},
		{From: tm("13:00"), At: tm("14:00"), Points: 10, Rate: rate(1), LOut: 15, LCov: 15, Reported: true},
	})
	if !nearP(tot.PointsPerListUSD, 0.24) || !near(tot.UnattributedPoints, 0.8) || !near(steps[3].UnattributedPoints, 0.8) ||
		steps[0].UnattributedPoints != 0 || steps[2].UnattributedPoints != 0 {
		t.Errorf("k %v, unattributed %v points (steps %v, %v, %v); want 0.24 and 0.8 in the last", tot.PointsPerListUSD,
			tot.UnattributedPoints, steps[0].UnattributedPoints, steps[2].UnattributedPoints, steps[3].UnattributedPoints)
	}
}

// #126 finding 25: an idle reading is no split step. A seat whose only
// reported reading is idle, followed by one the outside sessions had not
// reported, has no split at all, so the page keeps covey-only bounds for it.
func TestSplitSeatIdleIsNoSplitStep(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 0, Rate: rate(4), Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 3, Rate: rate(4), LCov: 2, Reported: false},
	})
	if s := steps[0]; s.Split || s.Reason != SplitIdle || !nearP(s.RealUSD, 0) || s.ExternalRealUSD != nil || s.CoveyRealUSD != nil {
		t.Errorf("the idle step = %+v, want not split (%s), R 0", s, SplitIdle)
	}
	if s := steps[1]; s.Split || s.Reason != SplitNotReported || !near(s.UnsplitRealUSD, 12) {
		t.Errorf("the unreported step = %+v, want unsplit 12 (%s)", s, SplitNotReported)
	}
	if tot.SplitSteps != 0 || tot.UnsplitSteps != 1 || tot.SplitPoints != 0 || tot.CoveyRealUSD != 0 ||
		tot.SplitRuns != 0 || !near(tot.UnsplitRealUSD, 12) || tot.Share != nil || tot.StepCount != 2 {
		t.Errorf("split steps %d, unsplit %d, split points %v, covey %v, runs %d, unsplit $%v, share %v, steps %d; "+
			"want 0, 1, 0, 0, 0, 12, nil, 2", tot.SplitSteps, tot.UnsplitSteps, tot.SplitPoints, tot.CoveyRealUSD,
			tot.SplitRuns, tot.UnsplitRealUSD, tot.Share, tot.StepCount)
	}
	m := meterSums([]ExternalMeterTotal{tot}, steps)
	if len(m.Hours) != 2 || m.Hours[0].SplitSteps != 0 || m.Hours[0].Steps != 1 || m.SplitSteps != 0 {
		t.Errorf("hours %+v, split steps %d; want two hours, none split", m.Hours, m.SplitSteps)
	}
}

// #126 finding 24: a reading without a plan has no price, so its $
// coefficient in the chunk's range is 0, not the price of the reading before
// it. 2 pts @ $4, 0 pts without a plan, 2 pts @ $4, each with L_out 1 and
// L_cov 1: rates 4, 0, 4 → ½(4 + 4 + 4 + 4) = 8; the outside part's
// coefficients 2, 0, 2 → ½(2 + 2 + 2 + 2) = 4.
func TestSplitSeatRangeWithoutAPlan(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 2, Rate: rate(4), LOut: 1, LCov: 1, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 0, Rate: nil, LOut: 1, LCov: 1, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 2, Rate: rate(4), LOut: 1, LCov: 1, Reported: true},
	})
	if !steps[1].Split || tot.SplitSteps != 3 || tot.SplitRuns != 1 {
		t.Errorf("middle step %+v, %d split steps, %d runs; want split, 3, 1", steps[1], tot.SplitSteps, tot.SplitRuns)
	}
	if !near(tot.SplitSlackUSD, 8) || !near(tot.SplitExternalSlackUSD, 4) || !near(tot.SplitCoveySlackUSD, 4) {
		t.Errorf("slack whole %v outside %v covey %v, want 8, 4, 4", tot.SplitSlackUSD, tot.SplitExternalSlackUSD, tot.SplitCoveySlackUSD)
	}
	if !near(tot.SplitSlackPoints, 1) || !near(tot.SplitExternalSlackPoints, 0.5) {
		t.Errorf("slack points %v outside %v, want 1, 0.5", tot.SplitSlackPoints, tot.SplitExternalSlackPoints)
	}
}

// #126 finding 26: an idle reading inside a chunk is seen through at its own
// price, not its neighbours'. 2 pts @ $4 (L_out 3, L_cov 1), 0 pts without a
// plan and no list, 2 pts @ $4 (L_out 1, L_cov 3): one chunk (an idle step
// does not close it, whatever its price) of share 4/8 = ½, so rates 4, 0, 4
// → ½(4 + 4 + 4 + 4) = 8, each part ½ of it, 4; the points ranges 1 and ½.
// With a plan of its own the idle step is priced at it: rates 4, 6, 4 →
// ½(4 + 2 + 2 + 4) = 6, each part 3. At the chunk's own price it changes
// nothing: 4, 2, 2, as if skipped. (Per reading's share, F3 and F3b, the
// parts were 4 / 4, 4.5 / 3 and 3 / 3, the points 0.75.)
func TestSplitSeatIdleInARunAtItsPrice(t *testing.T) {
	for _, c := range []struct {
		name                string
		rate                *float64
		whole, outside, cov float64
	}{
		{"no plan", nil, 8, 4, 4},
		{"a plan of its own", rate(6), 6, 3, 3},
		{"the chunk's plan", rate(4), 4, 2, 2},
	} {
		tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
			{From: tm("10:00"), At: tm("11:00"), Points: 2, Rate: rate(4), LOut: 3, LCov: 1, Reported: true},
			{From: tm("11:00"), At: tm("12:00"), Points: 0, Rate: c.rate, Reported: true},
			{From: tm("12:00"), At: tm("13:00"), Points: 2, Rate: rate(4), LOut: 1, LCov: 3, Reported: true},
		})
		if s := steps[1]; s.Split || s.Reason != SplitIdle {
			t.Errorf("%s: the idle step = %+v, want not split (%s)", c.name, s, SplitIdle)
		}
		if tot.SplitSteps != 2 || tot.SplitRuns != 1 || tot.UnsplitSteps != 0 {
			t.Errorf("%s: %d split steps, %d runs, %d unsplit; want 2, 1, 0", c.name, tot.SplitSteps, tot.SplitRuns, tot.UnsplitSteps)
		}
		if !near(tot.SplitSlackUSD, c.whole) || !near(tot.SplitExternalSlackUSD, c.outside) || !near(tot.SplitCoveySlackUSD, c.cov) {
			t.Errorf("%s: slack whole %v outside %v covey %v, want %v, %v, %v", c.name,
				tot.SplitSlackUSD, tot.SplitExternalSlackUSD, tot.SplitCoveySlackUSD, c.whole, c.outside, c.cov)
		}
		if !near(tot.SplitSlackPoints, 1) || !near(tot.SplitExternalSlackPoints, 0.5) || !near(tot.SplitCoveySlackPoints, 0.5) {
			t.Errorf("%s: slack points %v outside %v covey %v, want 1, 0.5, 0.5", c.name,
				tot.SplitSlackPoints, tot.SplitExternalSlackPoints, tot.SplitCoveySlackPoints)
		}
	}
}

// Decision 4 of part A and the lists' edges (cost-controller, part A finding
// 2): a reading that did not move gives R = 0 even without a plan, split as
// 0 / 0 — not unpriced — when outside calls ran; a step with outside calls
// and no covey line gives all of R to the outside sessions.
func TestSplitSeatZeroMovementAndOneSidedLists(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 0, Rate: nil, LOut: 3, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 2, Rate: rate(4), LOut: 3, Reported: true},
	})
	if s := steps[0]; !s.Split || !nearP(s.RealUSD, 0) || !nearP(s.ExternalRealUSD, 0) || !nearP(s.CoveyRealUSD, 0) {
		t.Errorf("the unmoved step = %+v, want R 0 split 0 / 0", s)
	}
	if s := steps[1]; !nearP(s.ExternalRealUSD, 8) || !nearP(s.CoveyRealUSD, 0) {
		t.Errorf("L_cov 0: %+v, want R_out = R = 8", s)
	}
	if tot.HoursWithReading != 2 || tot.UnpricedSteps != 0 || tot.StepCount != 2 {
		t.Errorf("hours with reading %d, unpriced steps %d, steps %d; want 2, 0, 2", tot.HoursWithReading, tot.UnpricedSteps, tot.StepCount)
	}
}

// Part A finding 1: L_out takes a model the list does not know at the
// call's reported_usd, the counterpart of L_cov's engine figure; a call
// with neither is left out and counted.
func TestExternalStepUSD(t *testing.T) {
	tok := ModelTokens{Output: 100_000}
	if usd, ok := externalStepUSD("claude-opus-5-5", tok, true, 99); !ok || !near(usd, 2) {
		t.Errorf("a known model: %v %v, want the list's $2 whatever it reported", usd, ok)
	}
	if usd, ok := externalStepUSD("claude-sonnet-5-7", tok, true, 0.7); !ok || !near(usd, 0.7) {
		t.Errorf("an unknown model with reported_usd: %v %v, want 0.70", usd, ok)
	}
	if _, ok := externalStepUSD("claude-sonnet-5-7", tok, false, 0); ok {
		t.Error("an unknown model without reported_usd was priced")
	}
	// In the split: $2 opus + $0.70 reported = L_out 2.70 beside L_cov 0.30;
	// 3 points at $1, so R_out = 3 × 2.7 / 3 = 2.70. The call with neither
	// is counted, not priced.
	_, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{{From: tm("10:00"), At: tm("11:00"),
		Points: 3, Rate: rate(1), LOut: 2 + 0.7, LCov: 0.3, UnpricedCalls: 1, UnpricedTokens: 100_000, Reported: true}})
	if !nearP(steps[0].ExternalRealUSD, 2.7) || !nearP(steps[0].CoveyRealUSD, 0.3) {
		t.Errorf("split %+v, want 2.70 / 0.30", steps[0])
	}
}

// A step whose outside calls all lack a price has an unknown L_out, not 0:
// R = 3 × $0.77 = $2.31, L_cov $0.10 and 200 outside calls of a model the
// list does not know, none with reported_usd. Booked to covey it would be
// R_cov = $2.31; it is unsplit (outside_unpriced), split to nobody. With no
// covey line either it is the same reason, not moved_without_reported_call;
// a step that was not reported keeps not_reported.
func TestSplitSeatOutsideUnpriced(t *testing.T) {
	agent := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 3, Rate: rate(0.77), LCov: 0.10,
			Agents: map[uuid.UUID]float64{agent: 0.10}, UnpricedCalls: 200, UnpricedTokens: 4_000_000, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 1, Rate: rate(0.77), UnpricedCalls: 5, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 1, Rate: rate(0.77), LCov: 0.10, UnpricedCalls: 5},
	})
	if s := steps[0]; s.Split || s.Reason != SplitOutsideUnpriced || !near(s.UnsplitRealUSD, 2.31) || s.CoveyRealUSD != nil {
		t.Errorf("step 1 = %+v, want unsplit $2.31 (%s)", s, SplitOutsideUnpriced)
	}
	if s := steps[1]; s.Split || s.Reason != SplitOutsideUnpriced {
		t.Errorf("step 2 (no covey line) = %+v, want %s", s, SplitOutsideUnpriced)
	}
	if s := steps[2]; s.Split || s.Reason != SplitNotReported {
		t.Errorf("step 3 (not reported) = %+v, want %s", s, SplitNotReported)
	}
	if tot.CoveyRealUSD != 0 || tot.ExternalRealUSD != 0 || !near(tot.UnsplitRealUSD, 5*0.77) || tot.UnsplitSteps != 3 {
		t.Errorf("covey %v outside %v unsplit %v in %d steps; want 0, 0, 3.85 in 3", tot.CoveyRealUSD,
			tot.ExternalRealUSD, tot.UnsplitRealUSD, tot.UnsplitSteps)
	}
	if tot.OutsideUnpricedSteps != 2 || !near(tot.OutsideUnpricedRealUSD, 4*0.77) || tot.UnpricedCalls != 210 {
		t.Errorf("outside unpriced %d steps $%v, %d unpriced calls; want 2, 3.08, 210", tot.OutsideUnpricedSteps,
			tot.OutsideUnpricedRealUSD, tot.UnpricedCalls)
	}
	if len(tot.Agents) != 1 || tot.Agents[0].RealUSD != 0 || !near(tot.Agents[0].UnsplitListUSD, 0.10) {
		t.Errorf("agents %+v, want no real share and $0.10 unsplit list", tot.Agents)
	}
	m := meterSums([]ExternalMeterTotal{tot}, steps)
	if m.OutsideUnpricedSteps != 2 || !near(m.OutsideUnpricedRealUSD, 3.08) || m.CoveyRealUSD != 0 {
		t.Errorf("sums: %d outside-unpriced steps $%v, covey %v", m.OutsideUnpricedSteps, m.OutsideUnpricedRealUSD, m.CoveyRealUSD)
	}
}

// Acceptance test (E), finding 23: points no plan prices stay out of the
// dent, counted; a seat with only such points has no dent of its own.
func TestMeterSumsUnpricedMovement(t *testing.T) {
	a, _ := splitSeat(meterSeat{"seat_a", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 1, Rate: rate(4), LOut: 1, LCov: 1, Reported: true}})
	cTot, cSteps := splitSeat(meterSeat{"seat_c", 0}, nil, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 20},
		{From: tm("11:00"), At: tm("12:00"), Points: 10}})
	m := meterSums([]ExternalMeterTotal{a, cTot}, cSteps)
	if m.RealUSD != 4 || m.UnpricedPoints != 30 || m.UnpricedSteps != 2 || cTot.RealUSD != 0 || cTot.Points != 0 {
		t.Errorf("dent %v, unpriced %v points in %d steps; seat C real %v points %v", m.RealUSD, m.UnpricedPoints,
			m.UnpricedSteps, cTot.RealUSD, cTot.Points)
	}
	if cSteps[0].RealUSD != nil || cSteps[0].Reason != SplitUnpriced {
		t.Errorf("an unpriced step: %+v", cSteps[0])
	}
}

// A seat billed by usage for part of the range (0151): its readings then are
// usage points, out of the dent and of every chunk, moved or idle, and not
// unpriced. At $1 a point: 3 points over 1 / 1, an idle reading and 4 points
// under usage (with list on both sides), then 2 points over 1 / 3. The usage
// readings close the first chunk, so the two seat chunks keep their own
// shares (½ and ¼), and the usage readings' list is in no split figure.
func TestSplitSeatUsageBasisOutOfTheSplit(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("10:10"), Points: 3, Rate: rate(1), LOut: 1, LCov: 1, Reported: true},
		{From: tm("10:10"), At: tm("10:20"), Usage: true, LOut: 1, LCov: 1, Reported: true},
		{From: tm("10:20"), At: tm("10:30"), Points: 4, Usage: true, LOut: 2, LCov: 2, Reported: true},
		{From: tm("10:30"), At: tm("10:40"), Points: 2, Rate: rate(1), LOut: 1, LCov: 3, Reported: true},
	})
	for _, i := range []int{1, 2} {
		if s := steps[i]; s.Reason != SplitUsage || s.RealUSD != nil || s.Split || s.UnattributedRealUSD != 0 || s.UnsplitRealUSD != 0 {
			t.Errorf("step %d under usage = %+v, want reason %q, no R, no part", i, s, SplitUsage)
		}
	}
	if !nearP(steps[0].ExternalRealUSD, 1.5) || !nearP(steps[3].ExternalRealUSD, 0.5) || !nearP(steps[3].CoveyRealUSD, 1.5) {
		t.Errorf("seat steps: outside %v and %v, covey %v; want 1.5, 0.5, 1.5", steps[0].ExternalRealUSD, steps[3].ExternalRealUSD, steps[3].CoveyRealUSD)
	}
	if tot.UsageSteps != 2 || tot.UsagePoints != 4 || tot.UnpricedSteps != 0 || tot.UnpricedPoints != 0 ||
		tot.RealUSD != 5 || tot.Points != 5 || tot.SplitSteps != 2 || tot.SplitRuns != 2 ||
		tot.SplitExternalListUSD != 2 || tot.SplitCoveyListUSD != 4 || tot.StepCount != 4 {
		t.Errorf("seat = %+v; want 2 usage steps of 4 points, none unpriced, $5 over 5 points in 2 split steps and 2 chunks", tot)
	}
	m := meterSums([]ExternalMeterTotal{tot}, steps)
	if m.UsageSteps != 2 || m.UsagePoints != 4 || m.UnpricedPoints != 0 || len(m.Hours) != 1 ||
		m.Hours[0].UsagePoints != 4 || m.Hours[0].UnpricedPoints != 0 || m.Hours[0].Points != 5 {
		t.Errorf("sums: usage %d steps %v points, unpriced %v, hours %+v", m.UsageSteps, m.UsagePoints, m.UnpricedPoints, m.Hours)
	}
	// Without the basis the same moved readings are unpriced, as before.
	_, plain := splitSeat(meterSeat{"seat", 0}, nil, []shareStep{
		{From: tm("10:20"), At: tm("10:30"), Points: 4, LOut: 2, LCov: 2, Reported: true}})
	if plain[0].Reason != SplitUnpriced {
		t.Errorf("without a basis: reason %q, want %q", plain[0].Reason, SplitUnpriced)
	}
}

// One list of seats for every part of the share (#126 nit 18): the linked
// ones and every subscription seat with weekly readings, never an API key.
func TestMeterSeats(t *testing.T) {
	links := map[string]MeterCredential{"b": {SecretKey: "seat_a", SecretSlot: 0}, "a": {SecretKey: "seat_a", SecretSlot: 0}}
	coverage := []MeterCoverage{
		{SecretKey: "seat_b", SecretSlot: 1, CredentialKind: "subscription", WindowKind: WindowSevenDay},
		{SecretKey: "seat_c", SecretSlot: 0, CredentialKind: "subscription", WindowKind: "five_hour"},
		{SecretKey: "api", SecretSlot: 0, CredentialKind: "api_key", WindowKind: WindowSevenDay},
	}
	seats, linked := meterSeats(links, coverage)
	if len(seats) != 2 || seats[0] != (meterSeat{"seat_a", 0}) || seats[1] != (meterSeat{"seat_b", 1}) {
		t.Errorf("seats = %v", seats)
	}
	if strings.Join(linked[seats[0]], ",") != "a,b" || len(linked[seats[1]]) != 0 {
		t.Errorf("linked = %v", linked)
	}
}

// A reading that did not move and has no list on either side is idle even
// with outside calls of no price: nothing to split, and the chunk goes
// on through it (one chunk, not two).
func TestSplitSeatUnmovedUnpricedIsIdle(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 2, Rate: rate(4), LOut: 1, LCov: 1, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 0, Rate: rate(4), UnpricedCalls: 3, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 2, Rate: rate(4), LOut: 1, LCov: 1, Reported: true},
	})
	if s := steps[1]; s.Split || s.Reason != SplitIdle {
		t.Errorf("unmoved step with unpriced calls = %+v, want %s", s, SplitIdle)
	}
	if tot.SplitRuns != 1 || tot.SplitSteps != 2 || tot.UnsplitSteps != 0 || tot.OutsideUnpricedSteps != 0 || tot.UnpricedCalls != 3 {
		t.Errorf("runs %d, split %d, unsplit %d, outside unpriced %d, unpriced calls %d; want 1, 2, 0, 0, 3",
			tot.SplitRuns, tot.SplitSteps, tot.UnsplitSteps, tot.OutsideUnpricedSteps, tot.UnpricedCalls)
	}
}
