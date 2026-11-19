package observability

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// --- Usage outside covey, per model (#120, the detailed view) -------------------
//
// /cost/cells carries the outside series per bucket, source and origin. The
// detailed view of the sessions outside covey also splits it per model and
// prices every token type on its own. That is a read of its own
// (GET /api/v1/cost/external-usage), so no existing answer changes (F4):
// the same rows, cut by the same periodSQL rule and priced with the same
// ListPrice, one level finer. The sources and the meter share are taken from
// externalFor as /cost/cells computes them, never computed a second time.

// ExternalModelUsage is the answer of GET /api/v1/cost/external-usage.
type ExternalModelUsage struct {
	// Bucket: the bucket the cells are cut in, after normalizeBucket.
	Bucket string `json:"bucket"`
	// Since: the window's start.
	Since time.Time `json:"since"`
	// Source: the source the cells are narrowed to; nil for every source.
	Source *string `json:"source"`
	// Sources: every source of the window, whatever ?source= says — the
	// figures of /cost/cells' external.sources.
	Sources []ExternalSource `json:"sources"`
	// Models: the models of the cells, by name, with whether the list prices
	// them.
	Models []ExternalModelName `json:"models"`
	// Cells: per bucket, source, origin and model.
	Cells []ExternalModelCell `json:"cells"`
	// Meter: F3, exactly /cost/cells' external.meter (every source on the
	// meter, also with ?source=: a share is of the whole meter).
	Meter ExternalMeter `json:"meter"`
}

// ExternalModelName is one model of the window.
type ExternalModelName struct {
	Model  string `json:"model"`
	Priced bool   `json:"priced"`
}

// ExternalModelCell is one bucket × source × origin × model. Summed over the
// models it is /cost/cells' external cell of the same bucket, source and
// origin, in every token type and in dollars.
type ExternalModelCell struct {
	Period time.Time `json:"period"`
	Source string    `json:"source"`
	// Origin: main | subagent | workflow; nil where the uploader did not say.
	Origin *string `json:"origin"`
	Model  string  `json:"model"`
	Calls  int64   `json:"calls"`
	// ListUSD: F1 over the cell's calls; nil for a model the list does not
	// know (never 0). Measured splits it by cost type as the agents' cells
	// do; TypeUSD by token type (input, output, cache_read, cache_write_5m,
	// cache_write_1h). Both are empty for an unknown model.
	ListUSD  *float64           `json:"list_usd"`
	Measured map[string]float64 `json:"measured"`
	TypeUSD  map[string]float64 `json:"type_usd"`
	// ReportedUSD: Σ reported_usd where sent (ReportedCalls of Calls); nil
	// when no call carried one.
	ReportedUSD   *float64 `json:"reported_usd"`
	ReportedCalls int64    `json:"reported_calls"`
	// Tokens per type over every call, priced or not; UnpricedTokens and
	// UnpricedCalls are all of them for an unknown model, 0 otherwise.
	Tokens         map[string]int64 `json:"tokens"`
	UnpricedTokens int64            `json:"unpriced_tokens"`
	UnpricedCalls  int64            `json:"unpriced_calls"`
}

// ExternalUsageByModel reads the outside series of the window per model.
// source narrows the cells ("" = every source); the sources and the meter
// stay whole.
func (s *Store) ExternalUsageByModel(ctx context.Context, orgID uuid.UUID, bucket string, since time.Time, source string) (ExternalModelUsage, error) {
	bucket = normalizeBucket(bucket)
	out := ExternalModelUsage{Bucket: bucket, Since: since.UTC(), Sources: []ExternalSource{},
		Models: []ExternalModelName{}, Cells: []ExternalModelCell{},
		Meter: emptyMeter()}
	if source != "" {
		src := source
		out.Source = &src
	}

	// The sources and the meter share, as /cost/cells has them: the same
	// plans and coverage CostCellsFor hands externalFor, which splits the
	// meter per reading step whatever the bucket.
	plans, err := s.CredentialPlans(ctx, orgID, since)
	if err != nil {
		return out, err
	}
	// The cost basis (0151) as CostCellsFor reads it: a plan prices nothing
	// while its credential is billed by usage.
	bases, err := s.costBasesFor(ctx, orgID, since)
	if err != nil {
		return out, err
	}
	priced := seatPlans(plans, bases)
	_, coverage, err := s.meterSteps(ctx, orgID, bucket, since, priced, bases)
	if err != nil {
		return out, err
	}
	ext, err := s.externalFor(ctx, orgID, bucket, since, coverage, priced, bases)
	if err != nil {
		return out, err
	}
	out.Sources, out.Meter = ext.Sources, ext.Meter

	rows, err := s.pool.Query(ctx, `SELECT `+periodSQL(bucket, "ts")+`, source, origin, model, count(*),
		SUM(input), SUM(output), SUM(cache_read), SUM(cache_write_5m), SUM(cache_write_1h),
		SUM(reported_usd)::float8, count(reported_usd)
		FROM external_usage
		WHERE org_id = $1 AND ts >= $2 AND ($3 = '' OR source = $3)
		GROUP BY 1, 2, 3, 4`, orgID, since, source)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	models := map[string]bool{}
	for rows.Next() {
		var (
			c ExternalModelCell
			t externalTokens
		)
		if err := rows.Scan(&c.Period, &c.Source, &c.Origin, &c.Model, &t.calls, &t.input, &t.output, &t.cr, &t.w5m, &t.w1h,
			&t.reported, &t.reportedCalls); err != nil {
			return out, err
		}
		c.Period = c.Period.UTC()
		c.Calls = t.calls
		c.Tokens = map[string]int64{"input": t.input, "output": t.output, "cache_read": t.cr,
			"cache_write_5m": t.w5m, "cache_write_1h": t.w1h}
		c.Measured, c.TypeUSD = map[string]float64{}, map[string]float64{}
		if t.reported != nil {
			r := *t.reported
			c.ReportedUSD = &r
			c.ReportedCalls = t.reportedCalls
		}
		pr, ok := ListPrice(c.Model)
		models[c.Model] = ok
		if !ok {
			c.UnpricedTokens, c.UnpricedCalls = t.total(), t.calls
		} else {
			// The cell's figure is F1 itself, as externalFor adds it per model.
			usd := pr.Cost(t.model())
			c.ListUSD = &usd
			c.Measured[TypeCacheRead] = float64(t.cr) * pr.CacheRead / 1e6
			c.Measured[TypeCacheWrite] = (float64(t.w5m)*pr.CacheWrite5m + float64(t.w1h)*pr.CacheWrite1h) / 1e6
			c.Measured[TypeInput] = float64(t.input) * pr.Input / 1e6
			c.Measured[TypeOutput] = float64(t.output) * pr.Output / 1e6
			c.TypeUSD["input"] = float64(t.input) * pr.Input / 1e6
			c.TypeUSD["output"] = float64(t.output) * pr.Output / 1e6
			c.TypeUSD["cache_read"] = float64(t.cr) * pr.CacheRead / 1e6
			c.TypeUSD["cache_write_5m"] = float64(t.w5m) * pr.CacheWrite5m / 1e6
			c.TypeUSD["cache_write_1h"] = float64(t.w1h) * pr.CacheWrite1h / 1e6
		}
		out.Cells = append(out.Cells, c)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	sort.Slice(out.Cells, func(i, j int) bool {
		a, b := out.Cells[i], out.Cells[j]
		if !a.Period.Equal(b.Period) {
			return a.Period.Before(b.Period)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if oa, ob := originOrder(a.Origin), originOrder(b.Origin); oa != ob {
			return oa < ob
		}
		return a.Model < b.Model
	})
	for m, priced := range models {
		out.Models = append(out.Models, ExternalModelName{Model: m, Priced: priced})
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Model < out.Models[j].Model })
	return out, nil
}
