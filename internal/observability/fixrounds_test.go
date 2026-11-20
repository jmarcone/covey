package observability

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fxI64(v int64) *int64   { return &v }
func fxStr(v string) *string { return &v }

func fxGroup(t *testing.T, gs []FixRoundGroup, key string) FixRoundGroup {
	t.Helper()
	for _, g := range gs {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("no group %q in %+v", key, gs)
	return FixRoundGroup{}
}

func fxMean(t *testing.T, f FixFigure, want float64, n int) {
	t.Helper()
	if f.N != n {
		t.Fatalf("n = %d, want %d", f.N, n)
	}
	if f.Mean == nil || !near(*f.Mean, want) {
		t.Fatalf("mean = %v, want %v", f.Mean, want)
	}
}

func fxNoData(t *testing.T, f FixFigure) {
	t.Helper()
	if f.N != 0 || f.Mean != nil || f.Total != nil {
		t.Fatalf("a figure without tasks is no data (nil, n=0), got %+v", f)
	}
}

// $ per fix round is the Measured source: tokens × covey's list price per
// model, the engine's own figure for a model the list does not know.
func TestMeasuredUSDPricesPerModelWithTheList(t *testing.T) {
	usage := []FixRoundUsage{
		{Model: "claude-opus-5-5", ModelTokens: ModelTokens{
			Input: 1_000_000, Output: 100_000, CacheRead: 2_000_000, CacheWrite5m: 200_000, CacheWrite1h: 100_000,
			WebSearches: 3}, EngineUSD: 99}, // the engine's figure is ignored for a priced model
		{Model: "someone-elses-model", ModelTokens: ModelTokens{Input: 5}, EngineUSD: 0.5},
	}
	// opus: 4 + 2 + 0.4 + 1 + 0.8 + 3 × 0.01 = 8.23; unknown model: 0.5.
	usd, ok := MeasuredUSD(usage)
	if !ok || !near(usd, 8.73) {
		t.Fatalf("measured = %v (%v), want 8.73", usd, ok)
	}
	if _, ok := MeasuredUSD(nil); ok {
		t.Fatal("a task without a single usage row has no measured cost, not $0")
	}
}

// The figures split by decision: first-turn cache write = 5m + 1h, shown
// split too; first-turn cache read; NULL first turns left out of the means
// and their n; sticky_pending and NULL decisions as unknown.
func TestFixRoundsByDecision(t *testing.T) {
	dev := uuid.New()
	opus := func(input int64) []FixRoundUsage {
		return []FixRoundUsage{{Model: "claude-opus-5-5", ModelTokens: ModelTokens{Input: input}}}
	}
	rounds := []FixRoundRow{
		{TaskID: uuid.New(), AgentID: dev, Slug: "covey-dev", TopicKey: "pr:42", Decision: fxStr("sticky_resumed"), Policy: fxStr("conditional"),
			FirstInput: fxI64(10), FirstCacheRead: fxI64(150_000), FirstWrite5m: fxI64(1_000), FirstWrite1h: fxI64(2_000),
			GapS: fxI64(600), Context: fxI64(120_000), Usage: opus(1_000_000)}, // $4
		{TaskID: uuid.New(), AgentID: dev, Slug: "covey-dev", TopicKey: "pr:42", Decision: fxStr("sticky_resumed"), Policy: fxStr("always"),
			// first turn unknown: out of the first-turn figures
			GapS: fxI64(1800), Context: fxI64(80_000), Usage: opus(500_000)}, // $2
		{TaskID: uuid.New(), AgentID: dev, Slug: "covey-dev", TopicKey: "issue:7", Decision: fxStr("fresh"), Policy: fxStr("conditional"),
			FirstInput: fxI64(20), FirstCacheRead: fxI64(0), FirstWrite5m: fxI64(40_000), FirstWrite1h: fxI64(0),
			GapS: fxI64(4_000), Usage: opus(250_000)}, // $1
		{TaskID: uuid.New(), AgentID: dev, Slug: "covey-dev", TopicKey: "pr:43", Decision: fxStr("sticky_pending"), Policy: fxStr("always")},
		{TaskID: uuid.New(), AgentID: dev, Slug: "covey-dev", TopicKey: "pr:44"}, // from before 0154
	}
	rep := SummarizeFixRounds(time.Time{}, time.Time{}, rounds, nil)

	if len(rep.ByDecision) != 3 || rep.ByDecision[0].Key != "sticky_resumed" || rep.ByDecision[1].Key != "fresh" || rep.ByDecision[2].Key != "unknown" {
		t.Fatalf("decision groups in fixed order: %+v", rep.ByDecision)
	}
	r := fxGroup(t, rep.ByDecision, "sticky_resumed")
	if r.Rounds != 2 {
		t.Fatalf("resumed rounds = %d", r.Rounds)
	}
	fxMean(t, r.USD, 3, 2) // (4 + 2) / 2
	if !near(*r.USD.Total, 6) {
		t.Fatalf("resumed total = %v", *r.USD.Total)
	}
	fxMean(t, r.FirstTurnCacheWrite, 3_000, 1) // 1000 + 2000, the NULL round left out
	fxMean(t, r.FirstTurnCacheWrite5m, 1_000, 1)
	fxMean(t, r.FirstTurnCacheWrite1h, 2_000, 1)
	fxMean(t, r.FirstTurnCacheRead, 150_000, 1)
	fxMean(t, r.FirstTurnInput, 10, 1)
	fxMean(t, r.GapMinutes, 20, 2) // (10 + 30) / 2
	fxMean(t, r.ContextTokens, 100_000, 2)

	f := fxGroup(t, rep.ByDecision, "fresh")
	fxMean(t, f.FirstTurnCacheWrite, 40_000, 1)
	fxMean(t, f.FirstTurnCacheRead, 0, 1) // a measured 0 is a figure, not "no data"
	fxMean(t, f.USD, 1, 1)
	fxNoData(t, f.ContextTokens)

	u := fxGroup(t, rep.ByDecision, "unknown")
	if u.Rounds != 2 {
		t.Fatalf("sticky_pending and NULL are unknown: %+v", u)
	}
	fxNoData(t, u.USD)
	fxNoData(t, u.FirstTurnCacheWrite)

	p := fxGroup(t, rep.ByPolicy, "always")
	if p.Rounds != 2 {
		t.Fatalf("always rounds = %d", p.Rounds)
	}
	fxMean(t, p.USD, 2, 1)
	c := fxGroup(t, rep.ByPolicy, "conditional")
	fxMean(t, c.USD, 2.5, 2)
	if fxGroup(t, rep.ByPolicy, "unknown").Rounds != 1 || fxGroup(t, rep.ByPolicy, "fresh").Rounds != 0 {
		t.Fatalf("policy groups: %+v", rep.ByPolicy)
	}

	if len(rep.Agents) != 1 || rep.Agents[0].Slug != "covey-dev" {
		t.Fatalf("agents: %+v", rep.Agents)
	}
	if fxGroup(t, rep.Agents[0].ByDecision, "sticky_resumed").Rounds != 2 {
		t.Fatalf("per agent split: %+v", rep.Agents[0])
	}
}

// No rounds: every group is there, every figure is null with n = 0 — in the
// JSON too, where 0 would read as a measurement.
func TestFixRoundsWithoutDataAreNull(t *testing.T) {
	rep := SummarizeFixRounds(time.Time{}, time.Time{}, nil, nil)
	if len(rep.ByDecision) != 3 || len(rep.ByPolicy) != 4 {
		t.Fatalf("the fixed groups stand without data: %+v %+v", rep.ByDecision, rep.ByPolicy)
	}
	for _, g := range append(rep.ByDecision, rep.ByPolicy...) {
		if g.Rounds != 0 {
			t.Fatalf("%s: rounds %d", g.Key, g.Rounds)
		}
		fxNoData(t, g.USD)
		fxNoData(t, g.FirstTurnCacheWrite)
		fxNoData(t, g.FirstTurnCacheRead)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"usd":{"mean":null,"total":null,"n":0}`) {
		t.Fatalf("no data must marshal as null: %s", s)
	}
	if !strings.Contains(s, `"agents":[]`) || !strings.Contains(s, `"prs":[]`) || !strings.Contains(s, `"rounds_per_pr":[]`) {
		t.Fatalf("empty lists, not null: %s", s)
	}
}

// A policy value outside the known ones is shown on its own, after them.
func TestFixRoundsKeepUnknownPolicyValues(t *testing.T) {
	rep := SummarizeFixRounds(time.Time{}, time.Time{}, []FixRoundRow{{AgentID: uuid.New(), Policy: fxStr("ab")}}, nil)
	if len(rep.ByPolicy) != 5 || rep.ByPolicy[4].Key != "ab" || rep.ByPolicy[4].Rounds != 1 {
		t.Fatalf("policy groups: %+v", rep.ByPolicy)
	}
	if fxGroup(t, rep.ByPolicy, "unknown").Rounds != 0 {
		t.Fatal("a recorded policy is not unknown")
	}
}

// Rounds per PR: done tasks with pr:<n> per agent and PR; the PR's arm is the
// one policy its tasks ran under, "mixed" or "unknown"; issues are no PRs.
func TestRoundsPerPR(t *testing.T) {
	dev, fe := uuid.New(), uuid.New()
	task := func(agent uuid.UUID, slug, topic string, policy *string) PRTaskRow {
		return PRTaskRow{AgentID: agent, Slug: slug, TopicKey: topic, Policy: policy}
	}
	tasks := []PRTaskRow{
		task(dev, "covey-dev", "pr:1", fxStr("always")),
		task(dev, "covey-dev", "pr:1", fxStr("always")),
		task(dev, "covey-dev", "pr:1", nil), // the first task may carry no policy
		task(dev, "covey-dev", "pr:2", fxStr("conditional")),
		task(dev, "covey-dev", "pr:2", fxStr("conditional")),
		task(dev, "covey-dev", "pr:4", fxStr("conditional")),
		task(dev, "covey-dev", "pr:3", fxStr("always")),
		task(dev, "covey-dev", "pr:3", fxStr("conditional")),
		task(dev, "covey-dev", "issue:5", fxStr("always")),
		task(dev, "covey-dev", "pr:x", fxStr("always")),
		task(fe, "covey-frontend", "pr:1", nil),
	}
	rep := SummarizeFixRounds(time.Time{}, time.Time{}, nil, tasks)

	want := []PRRounds{
		{AgentID: dev, Slug: "covey-dev", PR: 1, Rounds: 3, Policy: "always"},
		{AgentID: dev, Slug: "covey-dev", PR: 2, Rounds: 2, Policy: "conditional"},
		{AgentID: dev, Slug: "covey-dev", PR: 3, Rounds: 2, Policy: "mixed"},
		{AgentID: dev, Slug: "covey-dev", PR: 4, Rounds: 1, Policy: "conditional"},
		{AgentID: fe, Slug: "covey-frontend", PR: 1, Rounds: 1, Policy: "unknown"},
	}
	if len(rep.PRs) != len(want) {
		t.Fatalf("prs: %+v", rep.PRs)
	}
	for i := range want {
		if rep.PRs[i] != want[i] {
			t.Fatalf("pr %d = %+v, want %+v", i, rep.PRs[i], want[i])
		}
	}

	arm := func(agent uuid.UUID, policy string) RoundsPerPR {
		for _, r := range rep.RoundsPerPR {
			if r.AgentID == agent && r.Policy == policy {
				return r
			}
		}
		t.Fatalf("no arm %s/%s in %+v", agent, policy, rep.RoundsPerPR)
		return RoundsPerPR{}
	}
	c := arm(dev, "conditional")
	if c.PRs != 2 {
		t.Fatalf("conditional PRs = %d", c.PRs)
	}
	fxMean(t, c.Rounds, 1.5, 3) // 3 tasks over 2 PRs; n is the tasks
	a := arm(dev, "always")
	fxMean(t, a.Rounds, 3, 3)
	fxMean(t, arm(dev, "mixed").Rounds, 2, 2)
	fxMean(t, arm(fe, "unknown").Rounds, 1, 1)
	if len(rep.RoundsPerPR) != 4 {
		t.Fatalf("arms: %+v", rep.RoundsPerPR)
	}
}

func TestPRNumber(t *testing.T) {
	for topic, want := range map[string]int64{"pr:42": 42, "pr:0": 0} {
		if n, ok := prNumber(topic); !ok || n != want {
			t.Fatalf("%s → %d %v", topic, n, ok)
		}
	}
	for _, topic := range []string{"issue:42", "pr:", "pr:-1", "pr:4x", "pr:99999999999999999999", "PR:1"} {
		if _, ok := prNumber(topic); ok {
			t.Fatalf("%s is no pull request", topic)
		}
	}
}

// A round that hits the turn limit ends failed and its continuation finishes
// it: one round, with the root's dispatch figures and the whole chain's cost.
func TestChainRoundsFoldContinuations(t *testing.T) {
	dev := uuid.New()
	root, cont, cont2, single := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	end := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	opus := func(input int64) []FixRoundUsage {
		return []FixRoundUsage{{Model: "claude-opus-5-5", ModelTokens: ModelTokens{Input: input}}}
	}
	tasks := []ChainTask{
		// Chain: root failed at the turn limit → continuation failed → continuation done.
		{EndID: cont2, TaskID: cont2, Parent: &cont, AgentID: dev, Slug: "covey-dev", TopicKey: "pr:42",
			Origin: "continuation:" + cont.String(), State: "done", FinishedAt: &end, Policy: fxStr("always"),
			FirstCacheRead: fxI64(999_999), Usage: opus(250_000)}, // $1; its own first turn is a resume of itself
		{EndID: cont2, TaskID: cont, Parent: &root, AgentID: dev, Slug: "covey-dev", TopicKey: "pr:42",
			Origin: "continuation:" + root.String(), State: "failed", Policy: fxStr("always"), Usage: opus(500_000)}, // $2
		{EndID: cont2, TaskID: root, AgentID: dev, Slug: "covey-dev", TopicKey: "pr:42", Origin: "agent:lead",
			State: "failed", Decision: fxStr("fresh"), Reason: fxStr("window"), Policy: fxStr("conditional"),
			FirstCacheRead: fxI64(0), FirstWrite5m: fxI64(40_000), FirstWrite1h: fxI64(0), GapS: fxI64(4_000),
			HasEarlier: true, Usage: opus(1_000_000)}, // $4
		// A done task without an earlier one on its topic: no fix round.
		{EndID: single, TaskID: single, AgentID: dev, Slug: "covey-dev", TopicKey: "pr:43", Origin: "manual",
			State: "done", FinishedAt: &end, Decision: fxStr("fresh"), Reason: fxStr("no_previous")},
	}
	rounds := ChainRounds(tasks)
	if len(rounds) != 1 {
		t.Fatalf("one chain is one round: %+v", rounds)
	}
	r := rounds[0]
	if r.TaskID != root || *r.Decision != "fresh" || *r.Reason != "window" || *r.Policy != "conditional" {
		t.Fatalf("the dispatch figures are the root's: %+v", r)
	}
	if *r.FirstCacheRead != 0 || *r.FirstWrite5m != 40_000 || *r.GapS != 4_000 || !r.FinishedAt.Equal(end) {
		t.Fatalf("first turn and gap from the root, finish from the end: %+v", r)
	}
	if usd, ok := MeasuredUSD(r.Usage); !ok || !near(usd, 7) {
		t.Fatalf("$ over the whole chain = %v, want 7", usd)
	}
	rep := SummarizeFixRounds(time.Time{}, time.Time{}, rounds, nil)
	f := fxGroup(t, rep.ByDecision, "fresh")
	if f.Rounds != 1 {
		t.Fatalf("one fresh round: %+v", rep.ByDecision)
	}
	fxMean(t, f.USD, 7, 1)
	fxMean(t, f.FirstTurnCacheWrite, 40_000, 1)

	// The end not done (the chain still failed): no round.
	tasks[0].State = "failed"
	if got := ChainRounds(tasks); len(got) != 0 {
		t.Fatalf("a chain that did not finish is no round: %+v", got)
	}
}

// An agent whose policy is fresh is fresh by setting (reason off): left out of
// the split by decision, kept under fresh in the split by policy, counted.
func TestFixRoundsLeaveOffOutOfTheDecision(t *testing.T) {
	dev, rev := uuid.New(), uuid.New()
	rounds := []FixRoundRow{
		{AgentID: dev, Slug: "covey-dev", Decision: fxStr("fresh"), Reason: fxStr("window"), Policy: fxStr("conditional")},
		{AgentID: dev, Slug: "covey-dev", Decision: fxStr("sticky_resumed"), Policy: fxStr("conditional")},
		{AgentID: rev, Slug: "covey-reviewer", Decision: fxStr("fresh"), Reason: fxStr("off"), Policy: fxStr("fresh")},
		{AgentID: rev, Slug: "covey-reviewer", Decision: fxStr("fresh"), Reason: fxStr("off"), Policy: fxStr("fresh")},
	}
	rep := SummarizeFixRounds(time.Time{}, time.Time{}, rounds, nil)
	if fxGroup(t, rep.ByDecision, "fresh").Rounds != 1 || fxGroup(t, rep.ByDecision, "sticky_resumed").Rounds != 1 {
		t.Fatalf("off rounds stay out of the decision split: %+v", rep.ByDecision)
	}
	if rep.OffExcluded != 2 {
		t.Fatalf("off_excluded = %d", rep.OffExcluded)
	}
	if fxGroup(t, rep.ByPolicy, "fresh").Rounds != 2 {
		t.Fatalf("off rounds stay in the policy split: %+v", rep.ByPolicy)
	}
	for _, a := range rep.Agents {
		total := 0
		for _, g := range a.ByDecision {
			total += g.Rounds
		}
		switch a.Slug {
		case "covey-dev":
			if total != 2 || a.OffExcluded != 0 {
				t.Fatalf("dev: %+v", a)
			}
		case "covey-reviewer":
			if total != 0 || a.OffExcluded != 2 {
				t.Fatalf("reviewer: %+v", a)
			}
		}
	}
}
