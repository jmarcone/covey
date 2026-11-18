package observability

import "strings"

// ListPriceModel is the Anthropic first-party API list price of one model, in
// US dollars per MILLION tokens, split the way Claude Code counts tokens.
//
// It exists to CHECK the engine, not to replace it: Claude Code prices its own
// runs (costUSD), covey books that figure, and this table recomputes it from
// the raw tokens so that a drift between the two shows up. Same rule as the
// daemon's price list: a model the table does not know yields no figure.
type ListPriceModel struct {
	Input, Output, CacheRead, CacheWrite5m, CacheWrite1h float64
}

// WebSearchUSD is the price of one web search request ($10 per 1,000).
const WebSearchUSD = 0.01

// listPrices: Anthropic API pricing (2026-11). Cache writes are 1.25× (5 min)
// and 2× (1 h) the input price. Haiku 5.5's cache read is not published
// separately; 0.1× input is assumed.
var listPrices = map[string]ListPriceModel{
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite5m: 5, CacheWrite1h: 8},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10, CacheWrite5m: 2.5, CacheWrite1h: 4},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite5m: 0.125, CacheWrite1h: 0.20},
}

// ListPrice looks a model up; dated or suffixed ids ("claude-opus-5-5[1m]")
// resolve to their base id.
func ListPrice(model string) (ListPriceModel, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(m, '['); i > 0 {
		m = m[:i]
	}
	if p, ok := listPrices[m]; ok {
		return p, true
	}
	for id, p := range listPrices {
		if strings.HasPrefix(m, id+"-") {
			return p, true
		}
	}
	return ListPriceModel{}, false
}

// Cost prices tokens. Output already includes thinking tokens, as Claude Code reports them.
func (p ListPriceModel) Cost(t ModelTokens) float64 {
	const perMillion = 1_000_000.0
	return (float64(t.Input)*p.Input+float64(t.Output)*p.Output+float64(t.CacheRead)*p.CacheRead+
		float64(t.CacheWrite5m)*p.CacheWrite5m+float64(t.CacheWrite1h)*p.CacheWrite1h)/perMillion +
		float64(t.WebSearches)*WebSearchUSD
}
