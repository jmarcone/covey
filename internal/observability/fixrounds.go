package observability

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Fix rounds and sticky sessions (#130, AC 8 and 9).
//
// A developer's second, third … task on the same pull request or issue can
// resume the session of its previous one (migration 0154) instead of starting
// cold. Whether that pays is a question of figures, not of belief: what the
// first turn of such a round wrote to the cache and read from it, what the
// round cost, and how many rounds a pull request took — split by what the
// dispatcher decided (resumed or fresh) and by the policy it applied (the A/B
// test: always against conditional on one developer's own rounds).
//
// The definitions, pinned by fixrounds_test.go:
//
//   - A fix round is a CHAIN: a task with an explicit topic (pr:<owner>/<repo>#<n> or
//     issue:<owner>/<repo>#<n>) and the turn-limit continuations that carry its session on
//     (origin continuation:<parent>, parent_task_id). A round that hits
//     max_turns ends failed, and its continuation finishes the work — one
//     round, not a dropped one and a cheap one. The chain is a round when its
//     LAST task is done and finished (session_finished_at) inside [from, to),
//     and an EARLIER task of the same agent on the same topic exists that is
//     not a continuation (created before the chain's root, any state). The
//     first task on a topic is not a fix round: there was nothing to resume.
//   - The dispatch decision, its reason, the policy, the gap, the context and
//     the first-turn figures are the chain ROOT's: the root's dispatch is the
//     decision; a continuation resumes its own session by construction.
//   - First-turn cache write = first_turn_cache_write (the total; for rows
//     from before it existed, first_turn_cache_write_5m + _1h) of the first
//     assistant turn of the task's first run, also shown split by lifetime
//     where the engine sent the split (a 5-minute write inside a 55-minute
//     window is a full rewrite; an unsplit write counts in the total only,
//     never as 5-minute); first-turn cache read = first_turn_cache_read. A task whose
//     first turn is unknown (NULL) is left out of these figures, and their n
//     says how many were not.
//   - $ per fix round = Σ over the task's model_usage rows (one row per
//     result line and model, each the line's own delta — model_usage_since_last_result —
//     and the gap rows of processes without a line) of the tokens × covey's
//     list price per model, a model the list does not know at the engine's
//     own figure: the cost module's Measured source (CostCellsFor,
//     measuredBetween), never the booked cumulative totals — summed over
//     every task of the chain. A chain without a single row is left out (its
//     cost is unknown, not $0).
//   - The split by decision leaves out the rounds whose reason is "off": an
//     agent whose policy is fresh (the reviewer) is fresh by setting, not by a
//     decision, and would swamp the fresh side. They stay in the split by
//     policy (under fresh) and are counted in OffExcluded.
//   - Rounds per PR = the number of done tasks with topic pr:<owner>/<repo>#<n> per agent
//     (its role is its slug) and pull request, over every pull request with
//     at least one done task finished inside the window (a continuation
//     chain is one done task: the rounds before its end failed at the turn
//     limit). The pull request's
//     arm is the policy its tasks were dispatched with: one policy, or
//     "mixed".
//
// Every figure carries n, the number of tasks behind it. A figure with n = 0
// has no value (null): "no data" is not 0.

// Decision groups: what the dispatcher decided for a fix round. Anything else
// it recorded (sticky_pending: the daemon never answered) or nothing at all
// (a task from before 0154) is unknown.
const (
	FixDecisionResumed = "sticky_resumed"
	FixDecisionFresh   = "fresh"
	FixUnknown         = "unknown"
	FixPolicyMixed     = "mixed"
	// FixReasonOff: fresh because the agent's policy is fresh — a setting,
	// not a decision; left out of the split by decision.
	FixReasonOff = "off"
)

// fixDecisions and fixPolicies are the groups always shown, in this order —
// a group without tasks stands as "no data" rather than going missing.
var (
	fixDecisions = []string{FixDecisionResumed, FixDecisionFresh, FixUnknown}
	fixPolicies  = []string{"always", "conditional", "fresh", FixUnknown}
)

// FixFigure is one figure over a set of tasks: the mean per task and the
// total, both nil when no task carries it (N = 0).
type FixFigure struct {
	Mean  *float64 `json:"mean"`
	Total *float64 `json:"total"`
	N     int      `json:"n"`
}

// FixRoundFigures are the AC 8 figures of one group of fix rounds.
type FixRoundFigures struct {
	// Rounds is the number of fix rounds in the group.
	Rounds int `json:"rounds"`
	// USD is the measured cost per round (mean) and over the group (total).
	USD                   FixFigure `json:"usd"`
	FirstTurnCacheWrite   FixFigure `json:"first_turn_cache_write"`
	FirstTurnCacheWrite5m FixFigure `json:"first_turn_cache_write_5m"`
	FirstTurnCacheWrite1h FixFigure `json:"first_turn_cache_write_1h"`
	FirstTurnCacheRead    FixFigure `json:"first_turn_cache_read"`
	FirstTurnInput        FixFigure `json:"first_turn_input"`
	// GapMinutes: minutes from the predecessor's end to this round's dispatch
	// (sticky_gap_s / 60); ContextTokens: the predecessor's last-turn context
	// the decision was based on (sticky_context).
	GapMinutes    FixFigure `json:"gap_minutes"`
	ContextTokens FixFigure `json:"context_tokens"`
}

// FixRoundGroup is the figures of the rounds sharing one key (a decision or a policy).
type FixRoundGroup struct {
	Key string `json:"key"`
	FixRoundFigures
}

// FixRoundAgent is one agent's fix rounds, split both ways — the A/B is read
// per developer, never across models.
type FixRoundAgent struct {
	AgentID    uuid.UUID       `json:"agent_id"`
	Slug       string          `json:"slug"`
	ByDecision []FixRoundGroup `json:"by_decision"`
	ByPolicy   []FixRoundGroup `json:"by_policy"`
	// OffExcluded: the agent's rounds left out of ByDecision (reason off).
	OffExcluded int `json:"off_excluded"`
}

// PRRounds is one agent's rounds on one pull request.
type PRRounds struct {
	AgentID uuid.UUID `json:"agent_id"`
	Slug    string    `json:"slug"`
	// Repo is the pull request's repository (owner/repo), PR its number.
	Repo   string `json:"repo"`
	PR     int64  `json:"pr"`
	Rounds int    `json:"rounds"`
	// Policy is the arm: the one policy its tasks were dispatched with,
	// "mixed", or "unknown" when none was recorded.
	Policy string `json:"policy"`
}

// RoundsPerPR is the mean number of rounds per pull request of one agent and arm.
type RoundsPerPR struct {
	AgentID uuid.UUID `json:"agent_id"`
	Slug    string    `json:"slug"`
	Policy  string    `json:"policy"`
	PRs     int       `json:"prs"`
	// Rounds: mean = rounds per PR, total = done tasks, n = done tasks.
	Rounds FixFigure `json:"rounds"`
}

// FixRoundReport is the answer of GET /cost/fix-rounds.
type FixRoundReport struct {
	From       time.Time       `json:"from"`
	To         time.Time       `json:"to"`
	ByDecision []FixRoundGroup `json:"by_decision"`
	ByPolicy   []FixRoundGroup `json:"by_policy"`
	// OffExcluded: the rounds left out of ByDecision (reason off).
	OffExcluded int             `json:"off_excluded"`
	Agents      []FixRoundAgent `json:"agents"`
	RoundsPerPR []RoundsPerPR   `json:"rounds_per_pr"`
	PRs         []PRRounds      `json:"prs"`
}

// FixRoundUsage is one model's tokens of a task, summed over its model_usage rows.
type FixRoundUsage struct {
	Model string
	ModelTokens
	EngineUSD float64
}

// FixRoundRow is one fix round: its chain's root's dispatch figures, the
// finish of its last task, and the usage of the whole chain.
type FixRoundRow struct {
	TaskID     uuid.UUID // the chain's root
	AgentID    uuid.UUID
	Slug       string
	TopicKey   string
	Decision   *string // sticky_decision
	Reason     *string // sticky_reason
	Policy     *string // sticky_policy (the one applied)
	FinishedAt time.Time

	FirstInput, FirstCacheRead, FirstWrite5m, FirstWrite1h *int64
	// FirstWrite is the first turn's cache write in total (nil = unknown).
	FirstWrite *int64

	GapS    *int64 // sticky_gap_s
	Context *int64 // sticky_context

	// Usage is the chain's model_usage rows summed per task and model; empty
	// when no task of the chain left a line.
	Usage []FixRoundUsage
}

// ChainTask is one task of a candidate chain as read from backlog_tasks: the
// chain is named by its last task (EndID), done inside the window.
type ChainTask struct {
	EndID    uuid.UUID
	TaskID   uuid.UUID
	Parent   *uuid.UUID
	AgentID  uuid.UUID
	Slug     string
	TopicKey string
	Origin   string
	State    string

	Decision, Reason, Policy *string
	FinishedAt               *time.Time

	FirstInput, FirstCacheRead, FirstWrite5m, FirstWrite1h *int64
	FirstWrite                                             *int64
	GapS, Context                                          *int64

	// HasEarlier: an earlier task of the agent on the topic exists that is
	// not a continuation (created before this one). Read on the root.
	HasEarlier bool

	Usage []FixRoundUsage
}

// IsContinuation says whether a task carries another one's session on after
// the turn limit.
func IsContinuation(origin string) bool { return strings.HasPrefix(origin, "continuation:") }

// ChainRounds folds the chains into fix rounds: the root (the member that is
// not a continuation of another member) gives the dispatch figures, the end
// its finish, every member its usage. A chain whose end is not done, or whose
// root has no earlier task on the topic, is no fix round. Two ends on one
// root (which the orchestrator never makes) count once, by the later end.
func ChainRounds(tasks []ChainTask) []FixRoundRow {
	type chain struct {
		members []ChainTask
	}
	chains := map[uuid.UUID]*chain{}
	var order []uuid.UUID
	for _, t := range tasks {
		c := chains[t.EndID]
		if c == nil {
			c = &chain{}
			chains[t.EndID] = c
			order = append(order, t.EndID)
		}
		c.members = append(c.members, t)
	}
	byRoot := map[uuid.UUID]int{}
	var out []FixRoundRow
	for _, endID := range order {
		c := chains[endID]
		in := map[uuid.UUID]bool{}
		var end *ChainTask
		for i := range c.members {
			in[c.members[i].TaskID] = true
			if c.members[i].TaskID == endID {
				end = &c.members[i]
			}
		}
		var root *ChainTask
		for i := range c.members {
			m := &c.members[i]
			if !IsContinuation(m.Origin) || m.Parent == nil || !in[*m.Parent] {
				root = m
				break
			}
		}
		if end == nil || root == nil || end.State != "done" || end.FinishedAt == nil || !root.HasEarlier {
			continue
		}
		r := FixRoundRow{TaskID: root.TaskID, AgentID: root.AgentID, Slug: root.Slug, TopicKey: root.TopicKey,
			Decision: root.Decision, Reason: root.Reason, Policy: root.Policy, FinishedAt: *end.FinishedAt,
			FirstInput: root.FirstInput, FirstCacheRead: root.FirstCacheRead,
			FirstWrite5m: root.FirstWrite5m, FirstWrite1h: root.FirstWrite1h, FirstWrite: root.FirstWrite,
			GapS: root.GapS, Context: root.Context}
		for _, m := range c.members {
			r.Usage = append(r.Usage, m.Usage...)
		}
		if i, dup := byRoot[root.TaskID]; dup {
			if r.FinishedAt.After(out[i].FinishedAt) {
				out[i] = r
			}
			continue
		}
		byRoot[root.TaskID] = len(out)
		out = append(out, r)
	}
	return out
}

// PRTaskRow is one done task on a pull request (for rounds per PR).
type PRTaskRow struct {
	AgentID  uuid.UUID
	Slug     string
	TopicKey string
	Policy   *string
}

// MeasuredUSD prices a task's usage as the Measured source does: every model
// with covey's list, a model the list does not know at the engine's own
// figure. ok is false when there is no usage at all.
func MeasuredUSD(usage []FixRoundUsage) (usd float64, ok bool) {
	for _, u := range usage {
		if pr, known := ListPrice(u.Model); known {
			usd += pr.Cost(u.ModelTokens)
		} else {
			usd += u.EngineUSD
		}
	}
	return usd, len(usage) > 0
}

// figureAcc accumulates one FixFigure.
type figureAcc struct {
	sum float64
	n   int
}

func (a *figureAcc) add(v float64) { a.sum += v; a.n++ }

func (a figureAcc) figure() FixFigure {
	if a.n == 0 {
		return FixFigure{}
	}
	mean, total := a.sum/float64(a.n), a.sum
	return FixFigure{Mean: &mean, Total: &total, N: a.n}
}

type groupAcc struct {
	rounds                                       int
	usd, write, w5m, w1h, read, input, gap, ctxt figureAcc
}

func (g *groupAcc) add(r FixRoundRow) {
	g.rounds++
	if usd, ok := MeasuredUSD(r.Usage); ok {
		g.usd.add(usd)
	}
	switch {
	case r.FirstWrite != nil:
		g.write.add(float64(*r.FirstWrite))
	case r.FirstWrite5m != nil && r.FirstWrite1h != nil:
		g.write.add(float64(*r.FirstWrite5m + *r.FirstWrite1h))
	}
	if r.FirstWrite5m != nil {
		g.w5m.add(float64(*r.FirstWrite5m))
	}
	if r.FirstWrite1h != nil {
		g.w1h.add(float64(*r.FirstWrite1h))
	}
	if r.FirstCacheRead != nil {
		g.read.add(float64(*r.FirstCacheRead))
	}
	if r.FirstInput != nil {
		g.input.add(float64(*r.FirstInput))
	}
	if r.GapS != nil {
		g.gap.add(float64(*r.GapS) / 60)
	}
	if r.Context != nil {
		g.ctxt.add(float64(*r.Context))
	}
}

func (g groupAcc) figures() FixRoundFigures {
	return FixRoundFigures{
		Rounds:                g.rounds,
		USD:                   g.usd.figure(),
		FirstTurnCacheWrite:   g.write.figure(),
		FirstTurnCacheWrite5m: g.w5m.figure(),
		FirstTurnCacheWrite1h: g.w1h.figure(),
		FirstTurnCacheRead:    g.read.figure(),
		FirstTurnInput:        g.input.figure(),
		GapMinutes:            g.gap.figure(),
		ContextTokens:         g.ctxt.figure(),
	}
}

// DecisionKey is the decision group a round falls in.
func DecisionKey(d *string) string {
	if d != nil && (*d == FixDecisionResumed || *d == FixDecisionFresh) {
		return *d
	}
	return FixUnknown
}

// PolicyKey is the policy group a round falls in: the policy recorded on it,
// unknown when none was.
func PolicyKey(p *string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return FixUnknown
	}
	return *p
}

// groups turns the accumulators into the fixed groups in their order, plus
// any key outside them (a policy value this file does not know yet) after
// them, sorted — shown rather than folded into unknown.
func groups(fixed []string, accs map[string]*groupAcc) []FixRoundGroup {
	out := make([]FixRoundGroup, 0, len(fixed))
	seen := map[string]bool{}
	for _, k := range fixed {
		seen[k] = true
		var g groupAcc
		if a := accs[k]; a != nil {
			g = *a
		}
		out = append(out, FixRoundGroup{Key: k, FixRoundFigures: g.figures()})
	}
	var extra []string
	for k := range accs {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		out = append(out, FixRoundGroup{Key: k, FixRoundFigures: accs[k].figures()})
	}
	return out
}

func accFor(m map[string]*groupAcc, k string) *groupAcc {
	a := m[k]
	if a == nil {
		a = &groupAcc{}
		m[k] = a
	}
	return a
}

// prTopic reads the repository and number out of pr:<owner>/<repo>#<n>; ok is
// false for anything else.
func prTopic(topic string) (repo string, n int64, ok bool) {
	m := prTopicPattern.FindStringSubmatch(topic)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return m[1], n, true
}

var prTopicPattern = regexp.MustCompile(`^pr:([a-z0-9][a-z0-9-]{0,38}/[a-z0-9._-]{1,100})#([1-9][0-9]{0,17})$`)

// SummarizeFixRounds computes the report from the rows: rounds are the fix
// rounds of the window, prTasks every done task of the pull requests that had
// a done task in it.
func SummarizeFixRounds(from, to time.Time, rounds []FixRoundRow, prTasks []PRTaskRow) FixRoundReport {
	rep := FixRoundReport{From: from, To: to, Agents: []FixRoundAgent{}, RoundsPerPR: []RoundsPerPR{}, PRs: []PRRounds{}}

	byDecision, byPolicy := map[string]*groupAcc{}, map[string]*groupAcc{}
	type agentAcc struct {
		slug               string
		decision, policies map[string]*groupAcc
		off                int
	}
	agents := map[uuid.UUID]*agentAcc{}
	for _, r := range rounds {
		d, p := DecisionKey(r.Decision), PolicyKey(r.Policy)
		off := r.Reason != nil && *r.Reason == FixReasonOff
		a := agents[r.AgentID]
		if a == nil {
			a = &agentAcc{slug: r.Slug, decision: map[string]*groupAcc{}, policies: map[string]*groupAcc{}}
			agents[r.AgentID] = a
		}
		accFor(byPolicy, p).add(r)
		accFor(a.policies, p).add(r)
		if off {
			rep.OffExcluded++
			a.off++
			continue
		}
		accFor(byDecision, d).add(r)
		accFor(a.decision, d).add(r)
	}
	rep.ByDecision = groups(fixDecisions, byDecision)
	rep.ByPolicy = groups(fixPolicies, byPolicy)
	for id, a := range agents {
		rep.Agents = append(rep.Agents, FixRoundAgent{AgentID: id, Slug: a.slug,
			ByDecision: groups(fixDecisions, a.decision), ByPolicy: groups(fixPolicies, a.policies), OffExcluded: a.off})
	}
	sort.Slice(rep.Agents, func(i, j int) bool {
		if rep.Agents[i].Slug != rep.Agents[j].Slug {
			return rep.Agents[i].Slug < rep.Agents[j].Slug
		}
		return rep.Agents[i].AgentID.String() < rep.Agents[j].AgentID.String()
	})

	// Rounds per PR: per (agent, PR) the done tasks and the policies seen.
	type prKey struct {
		agent uuid.UUID
		repo  string
		pr    int64
	}
	type prAcc struct {
		slug     string
		rounds   int
		policies map[string]bool
	}
	prs := map[prKey]*prAcc{}
	for _, t := range prTasks {
		repo, n, ok := prTopic(t.TopicKey)
		if !ok {
			continue
		}
		k := prKey{t.AgentID, repo, n}
		a := prs[k]
		if a == nil {
			a = &prAcc{slug: t.Slug, policies: map[string]bool{}}
			prs[k] = a
		}
		a.rounds++
		if t.Policy != nil && strings.TrimSpace(*t.Policy) != "" {
			a.policies[*t.Policy] = true
		}
	}
	type armKey struct {
		agent  uuid.UUID
		policy string
	}
	type armAcc struct {
		slug string
		prs  int
		acc  figureAcc
	}
	arms := map[armKey]*armAcc{}
	for k, a := range prs {
		policy := FixUnknown
		switch len(a.policies) {
		case 0:
		case 1:
			for p := range a.policies {
				policy = p
			}
		default:
			policy = FixPolicyMixed
		}
		rep.PRs = append(rep.PRs, PRRounds{AgentID: k.agent, Slug: a.slug, Repo: k.repo, PR: k.pr, Rounds: a.rounds, Policy: policy})
		ak := armKey{k.agent, policy}
		arm := arms[ak]
		if arm == nil {
			arm = &armAcc{slug: a.slug}
			arms[ak] = arm
		}
		arm.prs++
		arm.acc.sum += float64(a.rounds)
		arm.acc.n += a.rounds
	}
	for k, a := range arms {
		f := FixFigure{N: a.acc.n}
		if a.acc.n > 0 && a.prs > 0 {
			mean, total := a.acc.sum/float64(a.prs), a.acc.sum
			f.Mean, f.Total = &mean, &total
		}
		rep.RoundsPerPR = append(rep.RoundsPerPR, RoundsPerPR{AgentID: k.agent, Slug: a.slug, Policy: k.policy, PRs: a.prs, Rounds: f})
	}
	sort.Slice(rep.PRs, func(i, j int) bool {
		a, b := rep.PRs[i], rep.PRs[j]
		if a.Slug != b.Slug {
			return a.Slug < b.Slug
		}
		if a.AgentID != b.AgentID {
			return a.AgentID.String() < b.AgentID.String()
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.PR < b.PR
	})
	sort.Slice(rep.RoundsPerPR, func(i, j int) bool {
		a, b := rep.RoundsPerPR[i], rep.RoundsPerPR[j]
		if a.Slug != b.Slug {
			return a.Slug < b.Slug
		}
		if a.AgentID != b.AgentID {
			return a.AgentID.String() < b.AgentID.String()
		}
		return a.Policy < b.Policy
	})
	return rep
}

// explicitTopic is the topic form the sticky lookup matches. #45 (topic
// routing) may store keys derived from titles in the same column; those are
// not rounds of one pull request or issue and stay out.
const explicitTopic = `'^(pr|issue):[a-z0-9][a-z0-9-]{0,38}/[a-z0-9._-]{1,100}#[1-9][0-9]{0,17}$'`

// FixRounds reads the fix rounds of an organisation finished in [from, to)
// and summarizes them (see the definitions at the top of this file).
func (s *Store) FixRounds(ctx context.Context, orgID uuid.UUID, from, to time.Time) (FixRoundReport, error) {
	// The candidate chains: every done task with a topic finished in the
	// window, walked up its continuations to the root (as the gap booking
	// walks them, costinsights.go). The depth bounds a cycle no one makes.
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE ends AS (
			SELECT t.id FROM backlog_tasks t
			WHERE t.org_id = $1 AND t.state = 'done' AND t.topic_key ~ `+explicitTopic+`
			  AND t.session_finished_at >= $2 AND t.session_finished_at < $3),
		chain(end_id, task_id, depth) AS (
			SELECT id, id, 0 FROM ends
			UNION
			SELECT c.end_id, t.parent_task_id, c.depth + 1
			FROM chain c JOIN backlog_tasks t ON t.id = c.task_id
			WHERE t.origin LIKE 'continuation:%' AND t.parent_task_id IS NOT NULL AND c.depth < 100)
		SELECT c.end_id, t.id, t.parent_task_id, t.agent_id, a.slug, COALESCE(t.topic_key, ''), t.origin, t.state,
			t.sticky_decision, t.sticky_reason, t.sticky_policy, t.session_finished_at,
			t.first_turn_input, t.first_turn_cache_read, t.first_turn_cache_write_5m, t.first_turn_cache_write_1h, t.first_turn_cache_write,
			t.sticky_gap_s, t.sticky_context,
			EXISTS (SELECT 1 FROM backlog_tasks p
				WHERE p.agent_id = t.agent_id AND p.topic_key = t.topic_key
				  AND p.origin NOT LIKE 'continuation:%' AND p.id <> t.id AND p.created_at < t.created_at)
		FROM chain c JOIN backlog_tasks t ON t.id = c.task_id JOIN agents a ON a.id = t.agent_id
		WHERE t.org_id = $1`, orgID, from, to)
	if err != nil {
		return FixRoundReport{}, err
	}
	var tasks []ChainTask
	idx := map[uuid.UUID][]int{}
	for rows.Next() {
		var c ChainTask
		if err := rows.Scan(&c.EndID, &c.TaskID, &c.Parent, &c.AgentID, &c.Slug, &c.TopicKey, &c.Origin, &c.State,
			&c.Decision, &c.Reason, &c.Policy, &c.FinishedAt,
			&c.FirstInput, &c.FirstCacheRead, &c.FirstWrite5m, &c.FirstWrite1h, &c.FirstWrite, &c.GapS, &c.Context, &c.HasEarlier); err != nil {
			rows.Close()
			return FixRoundReport{}, err
		}
		idx[c.TaskID] = append(idx[c.TaskID], len(tasks))
		tasks = append(tasks, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FixRoundReport{}, err
	}

	// The Measured source: every model_usage row of every task of the chains,
	// whenever it was filed (a round's first run may start before the window).
	if len(tasks) > 0 {
		ids := make([]uuid.UUID, 0, len(idx))
		for id := range idx {
			ids = append(ids, id)
		}
		rows, err = s.pool.Query(ctx, `SELECT task_id, model, SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens),
			SUM(cache_write_5m_tokens), SUM(cache_write_1h_tokens), SUM(web_search_requests), SUM(engine_usd)::float8
			FROM model_usage
			WHERE org_id = $1 AND task_id = ANY($2)
			GROUP BY task_id, model`, orgID, ids)
		if err != nil {
			return FixRoundReport{}, err
		}
		for rows.Next() {
			var (
				id uuid.UUID
				u  FixRoundUsage
			)
			if err := rows.Scan(&id, &u.Model, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite5m, &u.CacheWrite1h,
				&u.WebSearches, &u.EngineUSD); err != nil {
				rows.Close()
				return FixRoundReport{}, err
			}
			for _, i := range idx[id] {
				tasks[i].Usage = append(tasks[i].Usage, u)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return FixRoundReport{}, err
		}
	}
	rounds := ChainRounds(tasks)

	rows, err = s.pool.Query(ctx, `
		SELECT t.agent_id, a.slug, t.topic_key, t.sticky_policy
		FROM backlog_tasks t JOIN agents a ON a.id = t.agent_id
		WHERE t.org_id = $1 AND t.state = 'done' AND t.topic_key ~ '^pr:[a-z0-9][a-z0-9-]{0,38}/[a-z0-9._-]{1,100}#[1-9][0-9]{0,17}$'
		  AND EXISTS (SELECT 1 FROM backlog_tasks w
			WHERE w.agent_id = t.agent_id AND w.topic_key = t.topic_key AND w.state = 'done'
			  AND w.session_finished_at >= $2 AND w.session_finished_at < $3)`, orgID, from, to)
	if err != nil {
		return FixRoundReport{}, err
	}
	var prTasks []PRTaskRow
	for rows.Next() {
		var p PRTaskRow
		if err := rows.Scan(&p.AgentID, &p.Slug, &p.TopicKey, &p.Policy); err != nil {
			rows.Close()
			return FixRoundReport{}, err
		}
		prTasks = append(prTasks, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FixRoundReport{}, err
	}
	return SummarizeFixRounds(from, to, rounds, prTasks), nil
}
