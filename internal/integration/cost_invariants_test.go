package integration

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"testing"
	"time"

	"covey/internal/observability"
)

// Invariants of the cost figures against the real database (#165), over
// random input from a fixed seed so a failure repeats. Each one was shown to
// fail on a broken copy of the code (see the PR).

// A meter that drops by more than one point inside a running period — with
// no reset time to say so — begins a new week: the window splits there, the
// week after counts from 0, and a drop of one point is a lag that adds
// nothing. Over random readings of several credentials, the coverage's
// segments, points and readings, and the cells' steps, are those of a model
// of the rule written out here, the rise of the running maximum within the
// week; and the steps add up to the coverage.
func TestMeterDropInsideAPeriodSplitsTheWindow(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("zaehler-abfall")
	since := time.Now().UTC().Add(-8 * time.Hour).Truncate(time.Hour)
	r := rand.New(rand.NewSource(165))

	type model struct {
		readings, segments int
		points             float64
	}
	want := map[string]model{}
	for c := 0; c < 12; c++ {
		key := fmt.Sprintf("drop-%d", c)
		rt := observability.CredentialRef{RuntimeID: agent.ID, Ord: 0, SecretKey: key, Kind: "subscription"}
		n := 3 + r.Intn(8)
		pct := float64(r.Intn(60))
		prev, top := pct, pct
		m := model{readings: n, segments: 1}
		for i := 0; i < n; i++ {
			if i > 0 {
				switch r.Intn(5) {
				case 0:
					pct = max(0, prev-1) // a lagging reading
				case 1:
					pct = float64(r.Intn(int(max(prev-1, 1)))) // a drop: a new week unless it stays within one point
				default:
					pct = min(100, prev+float64(r.Intn(6)))
				}
				switch {
				case pct < prev-1:
					m.segments++
					top = pct
					m.points += pct
				default:
					m.points += max(0, pct-top)
					top = max(top, pct)
				}
			}
			prev = pct
			if err := s.obs.AddUsageReading(ctx, agent.OrgID, observability.MeterReading{Credential: rt,
				Source: "usage_cmd", Supported: true, WindowPercent: -1, WeekPercent: pct,
				FetchedAt: since.Add(time.Duration(i+1) * 20 * time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
		want[key] = m
	}

	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "hour", since)
	if err != nil {
		t.Fatal(err)
	}
	stepPoints := map[string]float64{}
	for _, st := range cells.Subscription {
		stepPoints[st.SecretKey] += st.Points
	}
	seen := 0
	for _, cov := range cells.Meter {
		m, ok := want[cov.SecretKey]
		if !ok {
			continue
		}
		seen++
		var segPoints float64
		for _, sg := range cov.Segments {
			segPoints += sg.Points
		}
		if cov.Readings != m.readings || len(cov.Segments) != m.segments || cov.Points != m.points ||
			segPoints != m.points || stepPoints[cov.SecretKey] != m.points {
			t.Errorf("%s: coverage %d readings, %d segments, %v points (segments %v, steps %v); want %d, %d, %v", cov.SecretKey,
				cov.Readings, len(cov.Segments), cov.Points, segPoints, stepPoints[cov.SecretKey], m.readings, m.segments, m.points)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of %d credentials have a coverage", seen, len(want))
	}
}

// One row per call: however the calls are sent — in batches of any size and
// order, again and again, some with a changed host or a corrected count —
// the store holds one row per message_id, the last version sent, and the
// cells count each call once. The totals the page shows are those of the
// distinct calls: nothing is booked twice.
func TestExternalUsageOneRowPerCallWhateverIsSent(t *testing.T) {
	s := newStack(t)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	r := rand.New(rand.NewSource(165))
	base := time.Now().UTC().Truncate(time.Hour).Add(-20 * time.Hour)

	type call struct {
		out  int
		host string
		at   time.Time
	}
	const calls = 40
	last := map[string]call{}
	sent := 0
	for push := 0; push < 25; push++ {
		var rows []map[string]any
		for _, i := range r.Perm(calls)[:1+r.Intn(15)] {
			id := fmt.Sprintf("m-%d", i)
			c, ok := last[id]
			if !ok || r.Intn(3) == 0 { // new, or a corrected count or host
				c = call{out: 1 + r.Intn(1000), host: []string{"laptop", "desktop"}[r.Intn(2)],
					at: base.Add(time.Duration(i) * 25 * time.Minute)}
			}
			last[id] = c
			row := extRow(id, c.at, "claude-opus-5-5", int64(c.out))
			row["host"] = c.host
			rows = append(rows, row)
			sent++
		}
		admin.expect(http.MethodPost, externalPath, extUpload("operator: home", rows...), http.StatusOK)
	}
	if n := s.externalCount(t, s.orgID); n != len(last) {
		t.Errorf("%d rows after %d rows sent, want one per call: %d", n, sent, len(last))
	}
	var wantOut int64
	for _, c := range last {
		wantOut += int64(c.out)
	}
	var gotOut int64
	for _, c := range cellsExternal(t, admin, "days=2&bucket=hour").Cells {
		gotOut += c.Tokens["output"]
	}
	if gotOut != wantOut {
		t.Errorf("the cells count %d output tokens, the distinct calls have %d", gotOut, wantOut)
	}
}
