package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cost cells: the data behind the combinable cost chart. Every bucket × agent
// × credential carries two independent figures, so the UI can filter and split
// them freely without asking again:
//
//   - reported: what the daemon sent and covey booked (cost_entries) — Claude
//     Code's own list-price calculation, per run;
//   - measured: the per-line usage deltas (model_usage) priced per token type
//     with covey's list — our own calculation of the same work. Only this one
//     splits by cost type: the engine reports one figure per model, not per
//     type.
//
// Both are plain sums of rows that each carry their own share, so they agree
// wherever a run's booking was made from (or checked against) its lines. A
// booking no line was linked to (a run cut off before Claude Code wrote its
// result, an engine without result lines, a booking older than the recordings
// 0142 could reach) keeps its figure on the measured side as "unattributed";
// a model the price list does not know keeps the engine's figure as
// "unpriced". Neither is split by type, and neither is mixed with the other.

// Cost-type keys of the measured figure.
const (
	TypeCacheRead    = "cache_read"
	TypeCacheWrite   = "cache_write"
	TypeInput        = "input"
	TypeOutput       = "output"
	TypeWebSearch    = "web_search"
	TypeUnpriced     = "unpriced"
	TypeUnattributed = "unattributed"
)

// CostTypes in display order (bottom of a bar first).
var CostTypes = []string{TypeCacheRead, TypeCacheWrite, TypeInput, TypeOutput, TypeWebSearch, TypeUnpriced, TypeUnattributed}

// CellAgent names an agent that appears in the cells.
type CellAgent struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
	// Reported is the agent's booked total over the window.
	Reported float64 `json:"reported_usd"`
	// Measured is the agent's recomputed list price over the window (its
	// lines' tokens priced per type, unknown models at the engine's figure):
	// the ranking and the legend. Bookings without lines are not in it.
	Measured float64 `json:"measured_usd"`
	// DepartmentID and Department name the department the agent belongs to
	// now (both nil without one), so the filter can group the agents the way
	// the org chart does. Its current department, not the one it had when
	// the costs were booked: covey keeps no history of moves.
	DepartmentID *uuid.UUID `json:"department_id"`
	Department   *string    `json:"department"`
}

// CostCell is one bucket × agent × credential. The credential is the secret
// value that paid (nil key: not attributable); CredentialKind says what money
// means on it (api_key | subscription, empty when unknown).
type CostCell struct {
	Period         time.Time          `json:"period"`
	AgentID        uuid.UUID          `json:"agent_id"`
	SecretKey      *string            `json:"secret_key"`
	SecretSlot     *int               `json:"secret_slot"`
	CredentialKind string             `json:"credential_kind"`
	Reported       float64            `json:"reported_usd"`
	Measured       map[string]float64 `json:"measured"` // cost type → USD
	// EngineUSD is what Claude Code itself priced the same lines at (Σ
	// costUSD of the deltas): the "reported by Claude Code" figure, of the
	// same calls as Measured — unlike Reported, which is what covey booked.
	EngineUSD float64 `json:"engine_usd"`
	// Tokens are the lines' tokens per type (input, output, cache_read,
	// cache_write_5m, cache_write_1h) and web searches (web_search), over
	// every model: the calls the dollars above are made of.
	Tokens map[string]int64 `json:"tokens"`
}

// CostCells is the whole data set for the chart.
type CostCells struct {
	// Since is the window's start: every line and source total is from it
	// on, while a meter step's span (from, at] may begin before it (#120).
	Since   time.Time   `json:"since"`
	Periods []time.Time `json:"periods"`
	Agents  []CellAgent `json:"agents"`
	Types   []string    `json:"types"`
	Cells   []CostCell  `json:"cells"`
	// Subscription is how far the provider's weekly meter moved per period,
	// per credential and source (percentage points): the third figure beside
	// the two list prices, what the work really used of a flat-rate seat.
	// The meter is account-wide — anything else on the account moves it too —
	// so it is per credential, never per agent. Nothing is added up across
	// credentials or sources here: two sources read the same meter, and two
	// credentials may be seats of one account.
	Subscription []MeterStep `json:"subscription"`
	// Meter says how well each credential's meter was read over the window,
	// per source: the reading the movement counts from, the last one, the
	// weeks it spans.
	Meter []MeterCoverage `json:"meter"`
	// Plans are the dated plan prices valid in the window, per credential
	// (migration 0145). The steps are priced with them.
	Plans []CredentialPlan `json:"plans"`
	// Bookings says what the window's cost entries were booked from, per
	// credential and basis (0144): a booking-check, so a reader sees when
	// some of the booked figures are an older daemon's running totals
	// (legacy, cumulative) rather than a run's own spend.
	Bookings []BookingBasis `json:"bookings"`
	// External is the usage outside covey (#120, external.go): its own
	// series beside the agents, in none of the fields above.
	External ExternalUsage `json:"external"`
	// CostBases are the dated bases valid in the window (0151, credcost.go):
	// how each credential's real money is formed. Absent for a credential
	// without one, and the whole field absent when none has one, so an
	// organisation that never set one gets the cells it got before.
	CostBases []CredentialBasis `json:"cost_bases,omitempty"`
	// UsageCharges are the usage charges that reach into the window, each in
	// its own currency, with the part the window holds and the part a usage
	// basis counts. Absent when there are none.
	UsageCharges []WindowCharge `json:"usage_charges,omitempty"`
	// UsageLimits are the dated monthly usage limits valid in the window
	// (0152): what turns a monthly meter's points into money. Absent when
	// none is.
	UsageLimits []UsageLimit `json:"usage_limits,omitempty"`
}

// cellDepartments fills in the department of every agent in the cells. A
// department of another organisation is never named, even if a row pointed
// at one.
func (s *Store) cellDepartments(ctx context.Context, orgID uuid.UUID, agents map[uuid.UUID]*CellAgent) error {
	if len(agents) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id, d.id, d.name
		FROM agents a JOIN departments d ON d.id = a.department_id AND d.org_id = a.org_id
		WHERE a.org_id = $1 AND a.id = ANY($2)`, orgID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, dep uuid.UUID
			name    string
		)
		if err := rows.Scan(&id, &dep, &name); err != nil {
			return err
		}
		if ag, ok := agents[id]; ok {
			ag.DepartmentID, ag.Department = &dep, &name
		}
	}
	return rows.Err()
}

// BookingBasis counts the cost entries of one credential booked on one basis.
type BookingBasis struct {
	SecretKey  *string `json:"secret_key"`
	SecretSlot *int    `json:"secret_slot"`
	Basis      string  `json:"basis"`
	Entries    int64   `json:"entries"`
	USD        float64 `json:"usd"`
}

// CostCellsFor builds the data set for the organisation since `since`.
func (s *Store) CostCellsFor(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time) (CostCells, error) {
	bucket = normalizeBucket(bucket)
	out := CostCells{Since: since.UTC(), Periods: []time.Time{}, Agents: []CellAgent{}, Types: CostTypes, Cells: []CostCell{}, Bookings: []BookingBasis{}}
	type key struct {
		p    time.Time
		a    uuid.UUID
		sk   string
		slot int
		attr bool
	}
	cells := map[key]*CostCell{}
	agents := map[uuid.UUID]*CellAgent{}
	cell := func(p time.Time, a uuid.UUID, sk *string, slot *int, kind string) *CostCell {
		k := key{p: p.UTC(), a: a}
		if sk != nil && slot != nil {
			k.sk, k.slot, k.attr = *sk, *slot, true
		}
		c, ok := cells[k]
		if !ok {
			c = &CostCell{Period: k.p, AgentID: a, Measured: map[string]float64{}, Tokens: map[string]int64{}}
			if k.attr {
				c.SecretKey, c.SecretSlot = &k.sk, &k.slot
			}
			cells[k] = c
		}
		if c.CredentialKind == "" {
			c.CredentialKind = kind
		}
		return c
	}
	agent := func(id uuid.UUID, name string) *CellAgent {
		ag, ok := agents[id]
		if !ok {
			ag = &CellAgent{ID: id, Label: name}
			agents[id] = ag
		}
		return ag
	}

	// Reported: what was booked per bucket, agent and credential.
	rows, err := s.pool.Query(ctx, `SELECT `+periodSQL(bucket, "ce.created_at")+`, a.id, a.display_name,
		ce.secret_key, ce.secret_slot, COALESCE(MAX(rc.kind),''), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		LEFT JOIN runtime_credentials rc ON rc.runtime_id = ce.runtime_id AND rc.ord = ce.credential_ord
		WHERE a.org_id = $1 AND ce.created_at >= $2
		GROUP BY 1,2,3,4,5`, orgID, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var (
			p    time.Time
			id   uuid.UUID
			name string
			sk   *string
			slot *int
			kind string
			usd  float64
		)
		if err := rows.Scan(&p, &id, &name, &sk, &slot, &kind, &usd); err != nil {
			rows.Close()
			return out, err
		}
		cell(p, id, sk, slot, kind).Reported += usd
		agent(id, name).Reported += usd
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Measured: the per-line deltas per bucket, agent, credential and model,
	// priced per type. model_usage carries the org, so this is a range scan.
	rows, err = s.pool.Query(ctx, `SELECT `+periodSQL(bucket, "mu.created_at")+`, mu.agent_id, a.display_name,
		mu.secret_key, mu.secret_slot, mu.credential_kind, mu.model,
		SUM(mu.input_tokens), SUM(mu.output_tokens), SUM(mu.cache_read_tokens),
		SUM(mu.cache_write_5m_tokens), SUM(mu.cache_write_1h_tokens), SUM(mu.web_search_requests),
		SUM(mu.engine_usd)::float8
		FROM model_usage mu JOIN agents a ON a.id = mu.agent_id
		WHERE mu.org_id = $1 AND mu.created_at >= $2
		GROUP BY 1,2,3,4,5,6,7`, orgID, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var (
			p           time.Time
			id          uuid.UUID
			name, model string
			sk          *string
			slot        *int
			kind        string
			t           ModelTokens
			engine      float64
		)
		if err := rows.Scan(&p, &id, &name, &sk, &slot, &kind, &model, &t.Input, &t.Output, &t.CacheRead,
			&t.CacheWrite5m, &t.CacheWrite1h, &t.WebSearches, &engine); err != nil {
			rows.Close()
			return out, err
		}
		ag := agent(id, name)
		c := cell(p, id, sk, slot, kind)
		c.EngineUSD += engine
		c.Tokens["input"] += t.Input
		c.Tokens["output"] += t.Output
		c.Tokens["cache_read"] += t.CacheRead
		c.Tokens["cache_write_5m"] += t.CacheWrite5m
		c.Tokens["cache_write_1h"] += t.CacheWrite1h
		c.Tokens["web_search"] += t.WebSearches
		pr, ok := ListPrice(model)
		if !ok {
			c.Measured[TypeUnpriced] += engine // unknown model: keep the engine's figure, unsplit
			ag.Measured += engine
			continue
		}
		c.Measured[TypeCacheRead] += float64(t.CacheRead) * pr.CacheRead / 1e6
		c.Measured[TypeCacheWrite] += (float64(t.CacheWrite5m)*pr.CacheWrite5m + float64(t.CacheWrite1h)*pr.CacheWrite1h) / 1e6
		c.Measured[TypeInput] += float64(t.Input) * pr.Input / 1e6
		c.Measured[TypeOutput] += float64(t.Output) * pr.Output / 1e6
		if t.WebSearches > 0 {
			c.Measured[TypeWebSearch] += float64(t.WebSearches) * WebSearchUSD
		}
		ag.Measured += pr.Cost(t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Bookings no line was linked to keep their booked cost, visible as its
	// own part: there is nothing to measure them by.
	rows, err = s.pool.Query(ctx, `SELECT `+periodSQL(bucket, "ce.created_at")+`, ce.agent_id,
		ce.secret_key, ce.secret_slot, COALESCE(MAX(rc.kind),''), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		LEFT JOIN runtime_credentials rc ON rc.runtime_id = ce.runtime_id AND rc.ord = ce.credential_ord
		WHERE a.org_id = $1 AND ce.created_at >= $2
		  AND NOT EXISTS (SELECT 1 FROM model_usage m WHERE m.cost_entry_id = ce.id)
		GROUP BY 1,2,3,4`, orgID, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var (
			p    time.Time
			id   uuid.UUID
			sk   *string
			slot *int
			kind string
			usd  float64
		)
		if err := rows.Scan(&p, &id, &sk, &slot, &kind, &usd); err != nil {
			rows.Close()
			return out, err
		}
		if usd > 0.0005 {
			cell(p, id, sk, slot, kind).Measured[TypeUnattributed] += usd
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	periods := map[time.Time]bool{}
	for _, c := range cells {
		out.Cells = append(out.Cells, *c)
		periods[c.Period] = true
	}
	for p := range periods {
		out.Periods = append(out.Periods, p)
	}
	sort.Slice(out.Periods, func(i, j int) bool { return out.Periods[i].Before(out.Periods[j]) })
	sort.Slice(out.Cells, func(i, j int) bool {
		a, b := out.Cells[i], out.Cells[j]
		if !a.Period.Equal(b.Period) {
			return a.Period.Before(b.Period)
		}
		if a.AgentID != b.AgentID {
			return a.AgentID.String() < b.AgentID.String()
		}
		return credOrder(a) < credOrder(b)
	})
	if err := s.cellDepartments(ctx, orgID, agents); err != nil {
		return out, err
	}
	for _, a := range agents {
		out.Agents = append(out.Agents, *a)
	}
	// Costliest first by the recomputed list price: the palette and the
	// bottom of every bar follow the ranking. The booked figure only breaks
	// a tie (an agent with bookings but no lines).
	sort.Slice(out.Agents, func(i, j int) bool {
		a, b := out.Agents[i], out.Agents[j]
		if a.Measured != b.Measured {
			return a.Measured > b.Measured
		}
		return a.Reported > b.Reported
	})

	// The booking-check: what the window's entries were booked from.
	rows, err = s.pool.Query(ctx, `SELECT ce.secret_key, ce.secret_slot, ce.basis, count(*), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		WHERE a.org_id = $1 AND ce.created_at >= $2
		GROUP BY 1,2,3 ORDER BY 1,2,3`, orgID, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b BookingBasis
		if err := rows.Scan(&b.SecretKey, &b.SecretSlot, &b.Basis, &b.Entries, &b.USD); err != nil {
			rows.Close()
			return out, err
		}
		out.Bookings = append(out.Bookings, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	plans, err := s.CredentialPlans(ctx, orgID, since)
	if err != nil {
		return out, err
	}
	out.Plans = plans
	// The basis of each credential (0151): while it is usage, the plan prices
	// nothing. Without a basis the plans price the meter as they always did.
	bases, err := s.costBasesFor(ctx, orgID, since)
	if err != nil {
		return out, err
	}
	out.CostBases = bases
	priced := seatPlans(plans, bases)
	steps, coverage, err := s.meterSteps(ctx, orgID, bucket, since, priced, bases)
	if err != nil {
		return out, err
	}
	out.Subscription, out.Meter = steps, coverage

	if out.External, err = s.externalFor(ctx, orgID, bucket, since, coverage, priced, bases); err != nil {
		return out, err
	}
	// A monthly usage limit (0152) turns its meter's movement into money,
	// and over the spans that money covers a charge entered by hand is not
	// counted again: the meter charges come first, and their spans go into
	// the hand-entered ones.
	limits, err := s.usageLimitsFor(ctx, orgID, since)
	if err != nil {
		return out, err
	}
	var meter []WindowCharge
	if len(limits) > 0 {
		out.UsageLimits = limits
		rows, err := s.meterRows(ctx, orgID, since)
		if err != nil {
			return out, err
		}
		meter = meterLimitCharges(rows, since, limits, bases)
	}
	if out.UsageCharges, err = s.usageChargesFor(ctx, orgID, since, time.Now(), bases, meter); err != nil {
		return out, err
	}
	out.UsageCharges = append(out.UsageCharges, meter...)
	return out, nil
}

// credOrder sorts a cell's credential: unattributed first, then by key and slot.
func credOrder(c CostCell) string {
	if c.SecretKey == nil || c.SecretSlot == nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%06d", *c.SecretKey, *c.SecretSlot)
}

// MeterStep is how far one credential's meter moved within one period, as
// read by one source. Steps of different credentials or sources are never
// added up here: the reader decides what to compare.
type MeterStep struct {
	Period         time.Time `json:"period"`
	SecretKey      string    `json:"secret_key"`
	SecretSlot     int       `json:"secret_slot"`
	CredentialKind string    `json:"credential_kind"`
	// Source: usage_cmd | inband | manual. WindowKind: the provider's window.
	Source     string  `json:"source"`
	WindowKind string  `json:"window_kind"`
	Points     float64 `json:"points"`
	// Readings in the period, and how many of them began a new week.
	Readings int `json:"readings"`
	Resets   int `json:"resets"`
	// LongestGapSecs is the longest time between a reading in the period and
	// the one before it: how coarsely the movement was observed.
	LongestGapSecs float64 `json:"longest_gap_secs"`
	// USD is the points priced at the plan valid when each was read (the
	// plan's weekly price over 100 points). Nil when no plan covers any of
	// them, and for every window but the weekly all-models one. Points read
	// while no plan was valid are UnpricedPoints and not in USD.
	USD            *float64 `json:"usd"`
	UnpricedPoints float64  `json:"unpriced_points"`
	// UsagePoints are points read while the credential's basis was usage
	// (0151): not in USD and not unpriced either — their money is the usage
	// charges. Absent without such a basis.
	UsagePoints float64 `json:"usage_points,omitempty"`
}

// MeterCoverage is how well one credential's meter was read over the window,
// by one source.
type MeterCoverage struct {
	SecretKey      string `json:"secret_key"`
	SecretSlot     int    `json:"secret_slot"`
	CredentialKind string `json:"credential_kind"`
	Source         string `json:"source"`
	WindowKind     string `json:"window_kind"`
	// BaselineAt/BaselinePercent: the reading the window's movement counts
	// from — the last one before the window (up to eight days back), else the
	// first one in it. BaselineBefore says which: without a reading before
	// the window, whatever moved before the first one is not in the figure.
	BaselineAt      time.Time `json:"baseline_at"`
	BaselinePercent float64   `json:"baseline_percent"`
	BaselineBefore  bool      `json:"baseline_before_window"`
	LastAt          time.Time `json:"last_at"`
	LastPercent     float64   `json:"last_percent"`
	// Readings in the window.
	Readings int `json:"readings"`
	// Points is the movement over the window. The meter shows whole
	// percents, so each week segment's movement is known to ±1 point:
	// PointsLow/PointsHigh are the bounds that leaves.
	Points         float64 `json:"points"`
	PointsLow      float64 `json:"points_low"`
	PointsHigh     float64 `json:"points_high"`
	LongestGapSecs float64 `json:"longest_gap_secs"`
	// Segments are the weeks the window spans: a new one at every reset.
	Segments []MeterSegment `json:"segments"`
	// MeasuredUSD is the recomputed list price of the credential's lines
	// between the baseline and the last reading — created_at in (BaselineAt,
	// LastAt], by the lines' own timestamps — the spend the movement above
	// was made of. A line of a model the list does not know counts at the
	// engine's figure, as in the cells. It is what the multiple divides, so it
	// must not depend on the chart's bucket: a cell holds only part of its
	// bucket at both ends of the window, and the baseline can lie before it.
	MeasuredUSD float64 `json:"measured_usd"`
	// USD/UnpricedPoints are Points priced the way the steps are: each
	// reading's points at the plan valid when it was read, summed over the
	// window — the sum of the steps' own figures, so a reader never re-prices
	// the movement at one plan. USD is nil when no plan covers any of the
	// points, and for every window but the weekly all-models one; points read
	// while no plan was valid are UnpricedPoints and not in USD.
	USD            *float64 `json:"usd"`
	UnpricedPoints float64  `json:"unpriced_points"`
	// UsagePoints: as on the steps, summed over the window.
	UsagePoints float64 `json:"usage_points,omitempty"`
}

// MeterSegment is one week of a credential's meter within the window.
type MeterSegment struct {
	StartAt time.Time `json:"start_at"`
	EndAt   time.Time `json:"end_at"`
	// From is what the movement counts from: the baseline's level for the
	// first segment, 0 for a week that began with a reset. To is the highest
	// reading of the week up to its last one in the window.
	From   float64 `json:"from_percent"`
	To     float64 `json:"to_percent"`
	Reset  bool    `json:"reset"`
	Points float64 `json:"points"`
}

// CredentialPlan is a credential's plan price, valid over [ValidFrom,
// ValidTo) (migration 0145). For measurement only: it prices the meter's
// points, it is never a budget.
type CredentialPlan struct {
	RuntimeID  *uuid.UUID `json:"runtime_id"`
	Ord        *int       `json:"ord"`
	SecretKey  string     `json:"secret_key"`
	SecretSlot int        `json:"secret_slot"`
	PlanName   string     `json:"plan_name"`
	Amount     float64    `json:"amount"`
	Currency   string     `json:"currency"`
	Period     string     `json:"period"`
	TaxBasis   string     `json:"tax_basis"`
	FXToUSD    float64    `json:"fx_to_usd"`
	FXDate     *string    `json:"fx_date"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to"`
	// WeeklyUSD is the plan's price per week in USD (a month is 52/12
	// weeks); USDPerPoint is one point of the weekly meter — what the seat
	// costs per percent of the week's allowance, IF the allowance is used up.
	// At half the allowance used, a used point costs twice that.
	WeeklyUSD   float64 `json:"weekly_usd"`
	USDPerPoint float64 `json:"usd_per_point"`
}

// WeeklyUSD spreads a plan's price over the weeks of its period.
func WeeklyUSD(amount, fxToUSD float64, period string) float64 {
	usd := amount * fxToUSD
	if period == "year" {
		return usd / 52
	}
	return usd * 12 / 52
}

// CredentialPlans returns the organisation's plan rows valid at any point
// since `since`, per credential in order of validity.
func (s *Store) CredentialPlans(ctx context.Context, orgID uuid.UUID, since time.Time) ([]CredentialPlan, error) {
	rows, err := s.pool.Query(ctx, `SELECT runtime_id, ord, secret_key, secret_slot, plan_name, amount::float8,
		currency, period, tax_basis, fx_to_usd::float8, to_char(fx_date, 'YYYY-MM-DD'), valid_from, valid_to
		FROM credential_plans
		WHERE org_id = $1 AND (valid_to IS NULL OR valid_to > $2)
		ORDER BY secret_key, secret_slot, valid_from`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredentialPlan{}
	for rows.Next() {
		var p CredentialPlan
		var ord *int16
		if err := rows.Scan(&p.RuntimeID, &ord, &p.SecretKey, &p.SecretSlot, &p.PlanName, &p.Amount,
			&p.Currency, &p.Period, &p.TaxBasis, &p.FXToUSD, &p.FXDate, &p.ValidFrom, &p.ValidTo); err != nil {
			return nil, err
		}
		if ord != nil {
			o := int(*ord)
			p.Ord = &o
		}
		p.WeeklyUSD = WeeklyUSD(p.Amount, p.FXToUSD, p.Period)
		p.USDPerPoint = p.WeeklyUSD / 100
		out = append(out, p)
	}
	return out, rows.Err()
}

// planAt is the plan of a credential valid at t.
func planAt(plans []CredentialPlan, key string, slot int, t time.Time) (CredentialPlan, bool) {
	for _, p := range plans {
		if p.SecretKey == key && p.SecretSlot == slot && !t.Before(p.ValidFrom) && (p.ValidTo == nil || t.Before(*p.ValidTo)) {
			return p, true
		}
	}
	return CredentialPlan{}, false
}

// meterQuery computes each reading's step, per credential (by its secret),
// source and window, over the window and the eight days before it — 35 for a
// window other than the weekly ones (a monthly usage limit, 0152, whose
// baseline may be read rarely) — in one pass of window functions:
//
//   - stale and unsupported readings are dropped: an hour-old figure read as
//     current turns a lag into movement;
//   - a reading of a week that is already over is dropped: its reset time
//     lies more than an hour before the latest one an earlier reading gave.
//     Kept, it would rejoin the old week's level inside the new week and add
//     the whole difference as points (and its successor would count the new
//     week from 0 a second time). The mark is the latest reset time of ALL
//     earlier readings, so a run of late readings is dropped as a whole;
//   - a reading begins a new week when its reset time moved forward. Where a
//     reading carries no reset time, when the previous one's reset has
//     passed (and not while it is still ahead); and only where no reset time
//     says anything, when it dropped by more than one point — the meter shows
//     whole percents, so a drop of one is a lagging reading, but a small week
//     (6 % then 2 %) is a new one and must not be missed;
//   - within a week the meter only rises: a step is the rise of the running
//     maximum, so a lagging reading adds 0 instead of counting its catch-up
//     a second time;
//   - the first reading of a new week counts in full (the meter started at
//     0); the first reading of a credential is its baseline and counts 0.
//
// Partitioned by the secret, not the runtime: the same value on two runtimes
// is one meter, and its history survives a Reorder. The period column comes
// from periodSQL (meterQuery).
const meterQueryTemplate = `
WITH r0 AS (
	SELECT id, secret_key, secret_slot, source, window_kind, credential_kind, fetched_at,
		week_percent AS pct, resets_at,
		MAX(resets_at) OVER (PARTITION BY secret_key, secret_slot, source, window_kind ORDER BY fetched_at, id
			ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS seen_resets
	FROM usage_readings
	WHERE org_id = $1 AND supported AND NOT stale AND week_percent IS NOT NULL AND secret_key IS NOT NULL
	  AND (fetched_at >= $2::timestamptz - interval '8 days'
	    OR (window_kind NOT LIKE 'seven\_day%' AND fetched_at >= $2::timestamptz - interval '35 days'))),
r AS (
	SELECT id, secret_key, secret_slot, source, window_kind, credential_kind, fetched_at, pct, resets_at
	FROM r0
	WHERE resets_at IS NULL OR seen_resets IS NULL OR resets_at >= seen_resets - interval '1 hour'),
l AS (
	SELECT r.*, LAG(pct) OVER w AS prev_pct, LAG(resets_at) OVER w AS prev_resets, LAG(fetched_at) OVER w AS prev_at
	FROM r
	WINDOW w AS (PARTITION BY secret_key, secret_slot, source, window_kind ORDER BY fetched_at, id)),
f AS (
	SELECT l.*, CASE
		WHEN prev_pct IS NULL THEN 0
		WHEN resets_at IS NOT NULL AND prev_resets IS NOT NULL THEN
			CASE WHEN resets_at > prev_resets + interval '1 hour' THEN 1 ELSE 0 END
		WHEN prev_resets IS NOT NULL THEN
			CASE WHEN prev_resets <= fetched_at THEN 1 ELSE 0 END
		WHEN pct < prev_pct - 1 THEN 1
		ELSE 0 END AS reset
	FROM l),
g AS (
	SELECT f.*, SUM(reset) OVER (PARTITION BY secret_key, secret_slot, source, window_kind
		ORDER BY fetched_at, id) AS week
	FROM f),
m AS (
	SELECT g.*, MAX(pct) OVER (PARTITION BY secret_key, secret_slot, source, window_kind, week
		ORDER BY fetched_at, id) AS top
	FROM g)
SELECT secret_key, secret_slot, MAX(credential_kind) OVER (PARTITION BY secret_key, secret_slot),
	source, window_kind, fetched_at, {{period}},
	pct, top, week::bigint, reset = 1,
	CASE WHEN prev_pct IS NULL THEN 0
	     WHEN reset = 1 THEN top
	     ELSE top - LAG(top) OVER (PARTITION BY secret_key, secret_slot, source, window_kind ORDER BY fetched_at, id) END,
	COALESCE(EXTRACT(EPOCH FROM fetched_at - prev_at)::float8, 0),
	resets_at
FROM m
ORDER BY secret_key, secret_slot, source, window_kind, fetched_at, id`

// meterQuery is meterQueryTemplate with the bucket's period: $1 org, $2 since.
func meterQuery(bucket string) string {
	return strings.Replace(meterQueryTemplate, "{{period}}", periodSQL(bucket, "fetched_at"), 1)
}

// meterRow is one reading with its step.
type meterRow struct {
	key            string
	slot           int
	kind           string
	source, window string
	at, period     time.Time
	pct, top       float64
	week           int64
	reset          bool
	points, gap    float64
	// resets is the reading's own reset time (nil where it named none): a
	// monthly limit merges the sources of one meter and finds its months
	// from it (limitcharges.go). The weekly figures do not read it.
	resets *time.Time
}

// meterRowsAt are the readings with their steps (meterQuery), in partition
// and time order, each with the bucket's period.
func (s *Store) meterRowsAt(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time) ([]meterRow, error) {
	rows, err := s.pool.Query(ctx, meterQuery(bucket), orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []meterRow
	for rows.Next() {
		var m meterRow
		if err := rows.Scan(&m.key, &m.slot, &m.kind, &m.source, &m.window, &m.at, &m.period,
			&m.pct, &m.top, &m.week, &m.reset, &m.points, &m.gap, &m.resets); err != nil {
			return nil, err
		}
		m.period = m.period.UTC()
		all = append(all, m)
	}
	return all, rows.Err()
}

// meterRows are the readings with their steps, for the meter share, which
// splits per reading whatever the chart's bucket.
func (s *Store) meterRows(ctx context.Context, orgID uuid.UUID, since time.Time) ([]meterRow, error) {
	return s.meterRowsAt(ctx, orgID, "hour", since)
}

// meterSteps turns the readings into movement per period, per credential and
// source, and says how well each was covered. Points of the weekly all-models
// window are priced with the plan valid when they were read; those read while
// the credential's basis was usage (0151) are its usage points instead.
func (s *Store) meterSteps(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time, plans []CredentialPlan, bases []CredentialBasis) ([]MeterStep, []MeterCoverage, error) {
	all, err := s.meterRowsAt(ctx, orgID, bucket, since)
	if err != nil {
		return nil, nil, err
	}

	steps, coverage := []MeterStep{}, []MeterCoverage{}
	for start := 0; start < len(all); {
		end := start + 1
		for end < len(all) && all[end].key == all[start].key && all[end].slot == all[start].slot &&
			all[end].source == all[start].source && all[end].window == all[start].window {
			end++
		}
		if st, cov, ok := meterPartition(all[start:end], since, plans, bases); ok {
			steps = append(steps, st...)
			coverage = append(coverage, cov)
		}
		start = end
	}
	for i := range coverage {
		usd, err := s.measuredBetween(ctx, orgID, coverage[i].SecretKey, coverage[i].SecretSlot,
			coverage[i].BaselineAt, coverage[i].LastAt)
		if err != nil {
			return nil, nil, err
		}
		coverage[i].MeasuredUSD = usd
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Period.Before(steps[j].Period) })
	return steps, coverage, nil
}

// measuredBetween is the recomputed list price of one credential's lines with
// created_at in (from, to]: every model priced per token type with covey's
// list, a model the list does not know at the engine's own figure.
func (s *Store) measuredBetween(ctx context.Context, orgID uuid.UUID, key string, slot int, from, to time.Time) (float64, error) {
	if !to.After(from) {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT model, SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens),
		SUM(cache_write_5m_tokens), SUM(cache_write_1h_tokens), SUM(web_search_requests), SUM(engine_usd)::float8
		FROM model_usage
		WHERE org_id = $1 AND secret_key = $2 AND secret_slot = $3 AND created_at > $4 AND created_at <= $5
		GROUP BY model`, orgID, key, slot, from, to)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var usd float64
	for rows.Next() {
		var (
			model  string
			t      ModelTokens
			engine float64
		)
		if err := rows.Scan(&model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h,
			&t.WebSearches, &engine); err != nil {
			return 0, err
		}
		if pr, ok := ListPrice(model); ok {
			usd += pr.Cost(t)
		} else {
			usd += engine
		}
	}
	return usd, rows.Err()
}

// addUSD adds usd to a figure that is nil until something was priced.
func addUSD(sum *float64, usd float64) *float64 {
	if sum != nil {
		usd += *sum
	}
	return &usd
}

// meterPartition is one credential × source × window: its steps per period
// within the window, and its coverage. ok is false when nothing was read in
// the window.
func meterPartition(rs []meterRow, since time.Time, plans []CredentialPlan, bases []CredentialBasis) ([]MeterStep, MeterCoverage, bool) {
	first := 0
	for first < len(rs) && rs[first].at.Before(since) {
		first++
	}
	if first == len(rs) {
		return nil, MeterCoverage{}, false
	}
	base := first // no reading before the window: the first one in it
	if first > 0 {
		base = first - 1 // the last one before it
	}
	c := MeterCoverage{SecretKey: rs[0].key, SecretSlot: rs[0].slot, CredentialKind: rs[0].kind,
		Source: rs[0].source, WindowKind: rs[0].window,
		BaselineAt: rs[base].at, BaselinePercent: rs[base].pct, BaselineBefore: first > 0,
		LastAt: rs[len(rs)-1].at, LastPercent: rs[len(rs)-1].pct, Segments: []MeterSegment{}}

	seg := MeterSegment{StartAt: rs[base].at, EndAt: rs[base].at, From: rs[base].top, To: rs[base].top}
	week, inSeg := rs[base].week, 0
	byPeriod := map[time.Time]*MeterStep{}
	var order []time.Time
	priced := c.WindowKind == WindowSevenDay
	for _, r := range rs[first:] {
		if r.week != week {
			// A baseline whose week ended before the window's first reading
			// moved nothing in the window: no segment of its own.
			if inSeg > 0 {
				c.Segments = append(c.Segments, seg)
			}
			seg, week, inSeg = MeterSegment{StartAt: r.at, Reset: true}, r.week, 0
		}
		inSeg++
		seg.EndAt, seg.To = r.at, r.top
		seg.Points += r.points

		st, ok := byPeriod[r.period]
		if !ok {
			st = &MeterStep{Period: r.period, SecretKey: r.key, SecretSlot: r.slot, CredentialKind: c.CredentialKind,
				Source: r.source, WindowKind: r.window}
			byPeriod[r.period] = st
			order = append(order, r.period)
		}
		st.Points += r.points
		st.Readings++
		if r.reset {
			st.Resets++
		}
		st.LongestGapSecs = max(st.LongestGapSecs, r.gap)
		if priced {
			if p, ok := planAt(plans, r.key, r.slot, r.at); ok {
				st.USD = addUSD(st.USD, r.points*p.USDPerPoint)
				c.USD = addUSD(c.USD, r.points*p.USDPerPoint)
			} else if len(bases) > 0 && basisAt(bases, r.key, r.slot, r.at) == BasisUsage {
				// Billed by usage then: the charges are these points' money.
				st.UsagePoints += r.points
				c.UsagePoints += r.points
			} else {
				st.UnpricedPoints += r.points
				c.UnpricedPoints += r.points
			}
		}
		c.Readings++
		c.Points += r.points
		c.LongestGapSecs = max(c.LongestGapSecs, r.gap)
	}
	c.Segments = append(c.Segments, seg)
	for _, sg := range c.Segments {
		c.PointsLow += max(0, sg.Points-1)
		c.PointsHigh += sg.Points + 1
	}
	steps := make([]MeterStep, 0, len(order))
	for _, p := range order {
		steps = append(steps, *byPeriod[p])
	}
	return steps, c, true
}
