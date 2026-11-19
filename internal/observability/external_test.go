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

// Acceptance test (A) of #126 finding 20, figures by cost-controller: each
// step split over its own span, placed at its reading; the desktop's stale
// watermark holds the last step unsplit; the parts' ranges are ½·TV of
// their shares (19); the agents' real share is per step (22).
func TestSplitSeatAcceptanceA(t *testing.T) {
	seat := meterSeat{"seat", 0}
	tot, steps := splitSeat(seat, []string{"operator: home"}, acceptanceA("18:30"))
	m := meterSums([]ExternalMeterTotal{tot}, steps)

	want := []struct {
		split           bool
		reason          string
		out, cov, unspl float64
	}{
		{true, "", 39.920160, 0.079840, 0},
		{true, "", 2, 2, 0},
		{true, "", 9, 3, 0},
		{false, SplitNotReported, 0, 0, 8},
	}
	for i, w := range want {
		st := m.Steps[i]
		if st.Split != w.split || st.Reason != w.reason || !near(st.UnsplitRealUSD, w.unspl) {
			t.Errorf("step %d = %+v, want split %v (%q), unsplit %v", i, st, w.split, w.reason, w.unspl)
			continue
		}
		if w.split && (!nearP(st.ExternalRealUSD, w.out) || !nearP(st.CoveyRealUSD, w.cov)) {
			t.Errorf("step %d: out %v cov %v, want %v / %v", i, *st.ExternalRealUSD, *st.CoveyRealUSD, w.out, w.cov)
		}
		if !w.split && (st.ExternalRealUSD != nil || st.CoveyRealUSD != nil) {
			t.Errorf("step %d is not split but has parts", i)
		}
	}
	if !near(m.RealUSD, 64) || !near(m.ExternalRealUSD, 50.920160) || !near(m.CoveyRealUSD, 5.079840) ||
		!near(m.UnsplitRealUSD, 8) || !nearP(m.Share, 0.909289) || m.SplitPoints != 14 || m.SplitRuns != 1 {
		t.Errorf("dent %v, outside %v, covey %v, unsplit %v, share %v, split points %v, runs %d",
			m.RealUSD, m.ExternalRealUSD, m.CoveyRealUSD, m.UnsplitRealUSD, *m.Share, m.SplitPoints, m.SplitRuns)
	}
	// The hours the steps are placed in.
	hours := map[int][4]float64{16: {44, 41.920160, 2.079840, 0}, 18: {12, 9, 3, 0}, 19: {8, 0, 0, 8}}
	if len(m.Hours) != len(hours) {
		t.Fatalf("hours = %+v", m.Hours)
	}
	for _, h := range m.Hours {
		w := hours[h.Hour.Hour()]
		if !nearP(h.RealUSD, w[0]) || !near(h.ExternalRealUSD, w[1]) || !near(h.CoveyRealUSD, w[2]) || !near(h.UnsplitRealUSD, w[3]) {
			t.Errorf("hour %v = %+v, want %v", h.Hour, h, w)
		}
	}
	// The multiples: list ÷ real, with their ranges.
	split := m.ExternalRealUSD + m.CoveyRealUSD
	list := m.SplitExternalListUSD + m.SplitCoveyListUSD
	check := func(name string, l, r, slack, value, low, high float64) {
		t.Helper()
		if !near(l/r, value) || math.Abs(l/(r+slack)-low) > 5e-6 || math.Abs(l/(r-slack)-high) > 5e-6 {
			t.Errorf("%s: %.5f (%.5f–%.5f), want %.5f (%.5f–%.5f)", name, l/r, l/(r+slack), l/(r-slack), value, low, high)
		}
	}
	check("together", list, split, m.SplitSlackUSD, 1.001786, 0.93500, 1.07885)
	check("outside", m.SplitExternalListUSD, m.ExternalRealUSD, m.SplitExternalSlackUSD, 1.060484, 0.96580, 1.17575)
	check("covey", m.SplitCoveyListUSD, m.CoveyRealUSD, m.SplitCoveySlackUSD, 0.413399, 0.29662, 0.68185)
	if !near(m.SplitExternalSlackUSD, 4.992016) || !near(m.SplitCoveySlackUSD, 2) || !near(m.SplitSlackUSD, 4) {
		t.Errorf("slack outside %v covey %v whole %v, want 4.992016, 2, 4", m.SplitExternalSlackUSD, m.SplitCoveySlackUSD, m.SplitSlackUSD)
	}
	// The agents: a1 has the first step's covey part and $6 of list in the
	// unsplit step; a2 the second and third steps' covey parts.
	agents := map[string]ExternalMeterAgent{}
	for _, a := range m.Credentials[0].Agents {
		agents[a.AgentID.String()[34:]] = a
	}
	if a := agents["a1"]; !near(a.RealUSD, 0.079840) || !near(a.SplitListUSD, 0.10) || !near(a.UnsplitListUSD, 6) {
		t.Errorf("a1 = %+v, want real 0.079840, split list 0.10, unsplit list 6", a)
	}
	if a := agents["a2"]; !near(a.RealUSD, 5) || !near(a.SplitListUSD, 2) || a.UnsplitListUSD != 0 {
		t.Errorf("a2 = %+v, want real 5, split list 2", a)
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

// Acceptance test (B), finding 19 alone: a part's range is ½·TV of its
// share, not its share of the whole's. One step all outside, one all covey,
// two points each: each part ±1 point, the whole ±1 too.
func TestSplitSeatPartRanges(t *testing.T) {
	steps := []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 2, Rate: rate(1), LOut: 5, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 2, Rate: rate(1), LCov: 5, Reported: true},
	}
	tot, _ := splitSeat(meterSeat{"seat", 0}, nil, steps)
	if tot.SplitExternalSlackPoints != 1 || tot.SplitCoveySlackPoints != 1 || tot.SplitSlackPoints != 1 || tot.SplitRuns != 1 {
		t.Errorf("slack outside %v covey %v whole %v (runs %d), want 1, 1, 1 (1)",
			tot.SplitExternalSlackPoints, tot.SplitCoveySlackPoints, tot.SplitSlackPoints, tot.SplitRuns)
	}
}

// Acceptance test (C): the web fixture of #126 (HOURS in
// SubscriptionDent.test.tsx) as steps: 4 points, L_out 300 / L_cov 151 (a1
// 101, a2 50); 2 points, 155 / 152 (a2); 3 points not reported (a1 40). The
// agents and the parts' slack by cost-controller's figures.
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
	if !near(got[a1].RealUSD, 0.413440) || !near(got[a2].RealUSD, 0.661702) || !near(got[a1].UnsplitListUSD, 40) {
		t.Errorf("a1 %+v, a2 %+v; want 0.413440 (unsplit list 40) and 0.661702", got[a1], got[a2])
	}
	if !near(tot.SplitExternalSlackUSD, 0.307010) || !near(tot.SplitCoveySlackUSD, 0.228514) {
		t.Errorf("slack outside %v covey %v, want 0.307010 and 0.228514", tot.SplitExternalSlackUSD, tot.SplitCoveySlackUSD)
	}
	if !near(tot.ExternalRealUSD, 1.694089) || !near(tot.CoveyRealUSD, 1.075142) || !near(tot.UnsplitRealUSD, 1.384615) {
		t.Errorf("outside %v covey %v unsplit %v", tot.ExternalRealUSD, tot.CoveyRealUSD, tot.UnsplitRealUSD)
	}
}

// Acceptance test (D), finding 21: a reported step with no list on either
// side that moved the meter is nobody's, not covey's whole — since F3b it
// is unattributed whole, no longer "not split". A reading that did not
// move either is idle: no money, and the run goes on through it.
func TestSplitSeatMovedWithoutACall(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 1, Rate: rate(4), Reported: true},
	})
	if s := steps[0]; s.Split || s.Reason != SplitNoReportedCall || s.UnattributedRealUSD != 4 || s.UnattributedPoints != 1 ||
		s.UnsplitRealUSD != 0 || tot.CoveyRealUSD != 0 || tot.UnsplitRealUSD != 0 || tot.UnsplitSteps != 0 ||
		tot.UnattributedRealUSD != 4 || tot.UnattributedSteps != 1 {
		t.Errorf("step %+v, covey %v, unsplit %v (%d steps), unattributed %v (%d steps); want unattributed 4 in 1 (%s)",
			s, tot.CoveyRealUSD, tot.UnsplitRealUSD, tot.UnsplitSteps, tot.UnattributedRealUSD, tot.UnattributedSteps, SplitNoReportedCall)
	}

	// Idle readings between two split ones: one run, the same range as
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

// unattributedFixture is F3b's case at $1 a point. Run A, the clean one: 5
// and 4 points over $50 of list each (L_out 30, L_cov 20), 9 points over
// $100. A reading the outside sessions had not reported (1 point) ends it.
// Run B: 20 points over L_out 50 / L_cov 30, then 10 points over L_cov 20 —
// 30 points over $100, three times run A's rate: another machine on the
// account. Then 2 points with no call on either side. Agent x ran in A1 ($20)
// and B1 ($30), agent y in A2 ($20), the unreported step ($5) and B2 ($20).
func unattributedFixture() []shareStep {
	x, y := uuid.MustParse("00000000-0000-0000-0000-0000000000a1"), uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	return []shareStep{
		{From: tm("08:00"), At: tm("08:30"), Points: 5, Rate: rate(1), LOut: 30, LCov: 20, Agents: map[uuid.UUID]float64{x: 20}, Reported: true},
		{From: tm("08:30"), At: tm("09:30"), Points: 4, Rate: rate(1), LOut: 30, LCov: 20, Agents: map[uuid.UUID]float64{y: 20}, Reported: true},
		{From: tm("09:30"), At: tm("10:30"), Points: 1, Rate: rate(1), LCov: 5, Agents: map[uuid.UUID]float64{y: 5}},
		{From: tm("10:30"), At: tm("11:30"), Points: 20, Rate: rate(1), LOut: 50, LCov: 30, Agents: map[uuid.UUID]float64{x: 30}, Reported: true},
		{From: tm("11:30"), At: tm("12:30"), Points: 10, Rate: rate(1), LCov: 20, Agents: map[uuid.UUID]float64{y: 20}, Reported: true},
		{From: tm("12:30"), At: tm("13:30"), Points: 2, Rate: rate(1), Reported: true},
	}
}

// F3b (#120): k = min over the runs of 5 points or more of (P_r + S_r) / L_r
// = min(10/100, 31/100) = 0.1 point per list $, at least the clean run's
// true 9/100. Run B's unattributed points U = 30 − 1 − 0.1 × 100 = 19, spread
// by points (12⅔ and 6⅓); the rest by list share. Run A, k's own, keeps
// U = 0. The step with no call is unattributed whole; the unreported one
// stays not split. Every step: covey + outside + unattributed + unsplit = R.
func TestSplitSeatUnattributed(t *testing.T) {
	for _, sources := range [][]string{{"operator: home"}, nil} {
		// A seat no source names gets the same unattributed part: another
		// machine moves its meter all the same.
		tot, steps := splitSeat(meterSeat{"seat", 0}, sources, unattributedFixture())
		if tot.PointsPerListUSD == nil || !near(*tot.PointsPerListUSD, 0.1) || *tot.PointsPerListUSD < 9.0/100 {
			t.Fatalf("sources %v: k = %v, want 0.1 (≥ the clean run's true 0.09)", sources, tot.PointsPerListUSD)
		}
		want := []struct {
			reason                    string
			out, cov, unattr, unsplit float64
		}{
			{"", 3, 2, 0, 0},
			{"", 2.4, 1.6, 0, 0},
			{SplitNotReported, 0, 0, 0, 1},
			{"", 7.333333 * 0.625, 7.333333 * 0.375, 12.666667, 0},
			{"", 0, 3.666667, 6.333333, 0},
			{SplitNoReportedCall, 0, 0, 2, 0},
		}
		for i, w := range want {
			s := steps[i]
			var out, cov float64
			if s.ExternalRealUSD != nil {
				out, cov = *s.ExternalRealUSD, *s.CoveyRealUSD
			}
			if s.Reason != w.reason || s.Split != (w.reason == "") || math.Abs(out-w.out) > 1e-5 || math.Abs(cov-w.cov) > 1e-5 ||
				math.Abs(s.UnattributedRealUSD-w.unattr) > 1e-5 || !near(s.UnsplitRealUSD, w.unsplit) {
				t.Errorf("sources %v, step %d = %+v (out %v cov %v), want %+v", sources, i, s, out, cov, w)
			}
			if !near(out+cov+s.UnattributedRealUSD+s.UnsplitRealUSD, *s.RealUSD) || !near(s.UnattributedPoints, s.UnattributedRealUSD) {
				t.Errorf("sources %v, step %d: the parts do not add up to R = %v", sources, i, *s.RealUSD)
			}
		}
		if !near(tot.RealUSD, 42) || !near(tot.UnattributedRealUSD, 21) || !near(tot.UnattributedPoints, 21) ||
			tot.UnattributedSteps != 3 || !near(tot.UnsplitRealUSD, 1) || tot.UnsplitSteps != 1 ||
			!near(tot.CoveyRealUSD, 10.016667) || !near(tot.ExternalRealUSD, 9.983333) ||
			!near(tot.CoveyRealUSD+tot.ExternalRealUSD+tot.UnattributedRealUSD+tot.UnsplitRealUSD, tot.RealUSD) {
			t.Errorf("sources %v: real %v covey %v outside %v unattributed %v (%v points, %d steps) unsplit %v (%d)", sources,
				tot.RealUSD, tot.CoveyRealUSD, tot.ExternalRealUSD, tot.UnattributedRealUSD, tot.UnattributedPoints,
				tot.UnattributedSteps, tot.UnsplitRealUSD, tot.UnsplitSteps)
		}
		// The split points: all four steps' whole, and the attributed points
		// by share; the lists stay the multiples' numerators.
		if tot.SplitPoints != 39 || !near(tot.SplitExternalPoints, 9.983333) || !near(tot.SplitCoveyPoints, 39-19-9.983333) ||
			tot.SplitExternalListUSD != 110 || tot.SplitCoveyListUSD != 90 || tot.SplitRuns != 2 {
			t.Errorf("sources %v: split points %v (outside %v covey %v), lists %v / %v, runs %d", sources, tot.SplitPoints,
				tot.SplitExternalPoints, tot.SplitCoveyPoints, tot.SplitExternalListUSD, tot.SplitCoveyListUSD, tot.SplitRuns)
		}
		// The agents follow covey's new part: x 2 + 2.75, y 1.6 + 3⅔; y's
		// $5 in the unreported step is unsplit list.
		agents := map[string]ExternalMeterAgent{}
		for _, a := range tot.Agents {
			agents[a.AgentID.String()[34:]] = a
		}
		if x, y := agents["a1"], agents["a2"]; !near(x.RealUSD, 4.75) || !near(y.RealUSD, 5.266667) ||
			!near(x.RealUSD+y.RealUSD, tot.CoveyRealUSD) || y.UnsplitListUSD != 5 {
			t.Errorf("sources %v: agents x %+v y %+v, want real 4.75 and 5.266667", sources, x, y)
		}
		m := meterSums([]ExternalMeterTotal{tot}, steps)
		if !near(m.UnattributedRealUSD, 21) || m.UnattributedSteps != 3 || !near(m.UnattributedPoints, 21) {
			t.Errorf("sums: unattributed %v in %d steps", m.UnattributedRealUSD, m.UnattributedSteps)
		}
		seen := false
		for _, h := range m.Hours {
			if h.Hour.Hour() == 11 {
				seen = true
				if !near(h.UnattributedRealUSD, 12.666667) || h.UnattributedSteps != 1 || h.SplitSteps != 1 {
					t.Errorf("hour 11 = %+v, want 12.67 unattributed in its one split step", h)
				}
			}
		}
		if !seen {
			t.Errorf("no hour 11 in %+v", m.Hours)
		}
	}
}

// F3b beside finding 26: an idle reading inside run B (no plan, no movement,
// no list) adds a range entry at its own price (0) with the share before
// it, but no points and no list: k, U and the parts are as without it,
// the points ranges too, and the run stays one run.
func TestSplitSeatUnattributedIdleInARun(t *testing.T) {
	plain := unattributedFixture()
	idle := append(append(append([]shareStep{}, plain[:4]...),
		shareStep{From: tm("11:30"), At: tm("11:45"), Reported: true}), plain[4:]...)
	idle[5].From = tm("11:45")
	a, _ := splitSeat(meterSeat{"seat", 0}, []string{"src"}, plain)
	b, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, idle)
	if steps[4].Reason != SplitIdle || steps[4].UnattributedRealUSD != 0 {
		t.Fatalf("the idle step = %+v", steps[4])
	}
	if b.PointsPerListUSD == nil || !near(*b.PointsPerListUSD, *a.PointsPerListUSD) || !near(b.UnattributedRealUSD, a.UnattributedRealUSD) ||
		!near(b.CoveyRealUSD, a.CoveyRealUSD) || !near(b.ExternalRealUSD, a.ExternalRealUSD) || b.SplitRuns != a.SplitRuns ||
		!near(b.SplitSlackPoints, a.SplitSlackPoints) || !near(b.SplitExternalSlackPoints, a.SplitExternalSlackPoints) {
		t.Errorf("with the idle step: k %v unattributed %v covey %v outside %v runs %d slack %v / %v; without: k %v %v %v %v %d %v / %v",
			b.PointsPerListUSD, b.UnattributedRealUSD, b.CoveyRealUSD, b.ExternalRealUSD, b.SplitRuns, b.SplitSlackPoints,
			b.SplitExternalSlackPoints, *a.PointsPerListUSD, a.UnattributedRealUSD, a.CoveyRealUSD, a.ExternalRealUSD, a.SplitRuns,
			a.SplitSlackPoints, a.SplitExternalSlackPoints)
	}
	// Its $ coefficient is 0 inside run B ($1 a point around it): the whole's
	// $ range for that run grows from ½(1 + 0 + 1) to ½(1 + 1 + 1 + 1).
	if !near(b.SplitSlackUSD, a.SplitSlackUSD+1) {
		t.Errorf("$ slack %v, want %v + 1", b.SplitSlackUSD, a.SplitSlackUSD)
	}
}

// Without a run of 5 points k is unknown (nil): only a step that moved with
// no call is unattributed, and every split step is split by list share
// alone, as F3 had it.
func TestSplitSeatUnattributedWithoutARate(t *testing.T) {
	tot, steps := splitSeat(meterSeat{"seat", 0}, []string{"src"}, []shareStep{
		{From: tm("10:00"), At: tm("11:00"), Points: 3, Rate: rate(1), LOut: 1, LCov: 1, Reported: true},
		{From: tm("11:00"), At: tm("12:00"), Points: 1, Rate: rate(1), LCov: 1, Reported: true},
		{From: tm("12:00"), At: tm("13:00"), Points: 3, Rate: rate(1), Reported: true},
		{From: tm("13:00"), At: tm("14:00"), Points: 4, Rate: rate(1), LOut: 9, LCov: 1, Reported: true},
	})
	if tot.PointsPerListUSD != nil {
		t.Errorf("k = %v, want nil: the runs move 4 points each", *tot.PointsPerListUSD)
	}
	if !near(tot.UnattributedRealUSD, 3) || tot.UnattributedSteps != 1 || steps[2].Reason != SplitNoReportedCall ||
		!nearP(steps[0].ExternalRealUSD, 1.5) || !nearP(steps[3].ExternalRealUSD, 3.6) || !near(tot.CoveyRealUSD+tot.ExternalRealUSD, 8) {
		t.Errorf("unattributed %v in %d steps, steps %+v; want 3 in the step with no call, the rest by list share",
			tot.UnattributedRealUSD, tot.UnattributedSteps, steps)
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
// coefficient in the run's range is 0, not the price of the reading before
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

// #126 finding 26: an idle reading inside a run is seen through at its own
// price, not its neighbours'. 2 pts @ $4 (L_out 3, L_cov 1), 0 pts without a
// plan and no list, 2 pts @ $4 (L_out 1, L_cov 3): the idle step keeps the
// share before it (0.75) at rate 0, so rates 4, 0, 4 → ½(4 + 4 + 4 + 4) = 8,
// the outside part's 3, 0, 1 → ½(3 + 3 + 1 + 1) = 4, covey's 1, 0, 3 → 4;
// the points ranges stay 1 and 0.75. With a plan of its own the idle step is
// priced at it: rates 4, 6, 4 → ½(4 + 2 + 2 + 4) = 6, outside 3, 4.5, 1 →
// ½(3 + 1.5 + 3.5 + 1) = 4.5, covey 1, 1.5, 3 → ½(1 + 0.5 + 1.5 + 3) = 3. At
// the run's own price it changes nothing: 4, 3, 3, as if skipped.
func TestSplitSeatIdleInARunAtItsPrice(t *testing.T) {
	for _, c := range []struct {
		name                string
		rate                *float64
		whole, outside, cov float64
	}{
		{"no plan", nil, 8, 4, 4},
		{"a plan of its own", rate(6), 6, 4.5, 3},
		{"the run's plan", rate(4), 4, 3, 3},
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
		if !near(tot.SplitSlackPoints, 1) || !near(tot.SplitExternalSlackPoints, 0.75) || !near(tot.SplitCoveySlackPoints, 0.75) {
			t.Errorf("%s: slack points %v outside %v covey %v, want 1, 0.75, 0.75", c.name,
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
// with outside calls of no price: nothing to split, and the run of split
// steps goes on through it (one run, not two).
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
