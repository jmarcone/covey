package observability

import (
	"context"
	"fmt"
	"sort"
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
	// Reported is the agent's booked total over the window (ranking, legend).
	Reported float64 `json:"reported_usd"`
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
}

// CostCells is the whole data set for the chart.
type CostCells struct {
	Periods []time.Time `json:"periods"`
	Agents  []CellAgent `json:"agents"`
	Types   []string    `json:"types"`
	Cells   []CostCell  `json:"cells"`
	// Subscription is how far the provider's weekly meter moved per period
	// (percentage points), the third figure beside the two list prices: what
	// the work really used of a flat-rate seat. Account-wide, so not per agent.
	Subscription []MeterStep `json:"subscription"`
}

// MeterStep is the weekly meter's movement within one period.
type MeterStep struct {
	Period time.Time `json:"period"`
	Points float64   `json:"points"`
}

// CostCellsFor builds the data set for the organisation since `since`.
func (s *Store) CostCellsFor(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time) (CostCells, error) {
	bucket = normalizeBucket(bucket)
	out := CostCells{Periods: []time.Time{}, Agents: []CellAgent{}, Types: CostTypes, Cells: []CostCell{}}
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
			c = &CostCell{Period: k.p, AgentID: a, Measured: map[string]float64{}}
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
	rows, err := s.pool.Query(ctx, `SELECT date_trunc($2, ce.created_at, 'UTC'), a.id, a.display_name,
		ce.secret_key, ce.secret_slot, COALESCE(MAX(rc.kind),''), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		LEFT JOIN runtime_credentials rc ON rc.runtime_id = ce.runtime_id AND rc.ord = ce.credential_ord
		WHERE a.org_id = $1 AND ce.created_at >= $3
		GROUP BY 1,2,3,4,5`, orgID, bucket, since)
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
	rows, err = s.pool.Query(ctx, `SELECT date_trunc($2, mu.created_at, 'UTC'), mu.agent_id, a.display_name,
		mu.secret_key, mu.secret_slot, mu.credential_kind, mu.model,
		SUM(mu.input_tokens), SUM(mu.output_tokens), SUM(mu.cache_read_tokens),
		SUM(mu.cache_write_5m_tokens), SUM(mu.cache_write_1h_tokens), SUM(mu.web_search_requests),
		SUM(mu.engine_usd)::float8
		FROM model_usage mu JOIN agents a ON a.id = mu.agent_id
		WHERE mu.org_id = $1 AND mu.created_at >= $3
		GROUP BY 1,2,3,4,5,6,7`, orgID, bucket, since)
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
		agent(id, name)
		c := cell(p, id, sk, slot, kind)
		pr, ok := ListPrice(model)
		if !ok {
			c.Measured[TypeUnpriced] += engine // unknown model: keep the engine's figure, unsplit
			continue
		}
		c.Measured[TypeCacheRead] += float64(t.CacheRead) * pr.CacheRead / 1e6
		c.Measured[TypeCacheWrite] += (float64(t.CacheWrite5m)*pr.CacheWrite5m + float64(t.CacheWrite1h)*pr.CacheWrite1h) / 1e6
		c.Measured[TypeInput] += float64(t.Input) * pr.Input / 1e6
		c.Measured[TypeOutput] += float64(t.Output) * pr.Output / 1e6
		if t.WebSearches > 0 {
			c.Measured[TypeWebSearch] += float64(t.WebSearches) * WebSearchUSD
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Bookings no line was linked to keep their booked cost, visible as its
	// own part: there is nothing to measure them by.
	rows, err = s.pool.Query(ctx, `SELECT date_trunc($2, ce.created_at, 'UTC'), ce.agent_id,
		ce.secret_key, ce.secret_slot, COALESCE(MAX(rc.kind),''), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		LEFT JOIN runtime_credentials rc ON rc.runtime_id = ce.runtime_id AND rc.ord = ce.credential_ord
		WHERE a.org_id = $1 AND ce.created_at >= $3
		  AND NOT EXISTS (SELECT 1 FROM model_usage m WHERE m.cost_entry_id = ce.id)
		GROUP BY 1,2,3,4`, orgID, bucket, since)
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
	for _, a := range agents {
		out.Agents = append(out.Agents, *a)
	}
	// Costliest first: the palette and the bottom of every bar follow the ranking.
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].Reported > out.Agents[j].Reported })

	steps, err := s.meterSteps(ctx, orgID, bucket, since)
	if err != nil {
		return out, err
	}
	out.Subscription = steps
	return out, nil
}

// credOrder sorts a cell's credential: unattributed first, then by key and slot.
func credOrder(c CostCell) string {
	if c.SecretKey == nil || c.SecretSlot == nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%06d", *c.SecretKey, *c.SecretSlot)
}

// meterSteps turns the weekly readings into movement per period. Each reading
// is compared with the one before it (the last one before the window included);
// a drop means the week was reset, and then the new value is the movement.
// With several credentials the steps add up, as their seats would.
func (s *Store) meterSteps(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time) ([]MeterStep, error) {
	rows, err := s.pool.Query(ctx, `SELECT runtime_id, credential_ord, date_trunc($2, fetched_at, 'UTC'), week_percent
		FROM usage_readings
		WHERE org_id = $1 AND supported AND week_percent IS NOT NULL
		  AND fetched_at >= COALESCE((SELECT max(fetched_at) FROM usage_readings
		      WHERE org_id = $1 AND supported AND week_percent IS NOT NULL AND fetched_at < $3), $3)
		ORDER BY runtime_id, credential_ord, fetched_at`, orgID, bucket, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cred struct {
		rt  uuid.UUID
		ord int
	}
	last := map[cred]float64{}
	seen := map[cred]bool{}
	byPeriod := map[time.Time]float64{}
	for rows.Next() {
		var c cred
		var p time.Time
		var pct float64
		if err := rows.Scan(&c.rt, &c.ord, &p, &pct); err != nil {
			return nil, err
		}
		if seen[c] {
			d := pct - last[c]
			if d < 0 {
				d = pct // the week was reset
			}
			byPeriod[p.UTC()] += d
		}
		seen[c], last[c] = true, pct
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []MeterStep{}
	for p, v := range byPeriod {
		out = append(out, MeterStep{Period: p, Points: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period.Before(out[j].Period) })
	return out, nil
}
