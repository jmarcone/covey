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
// and 2× (1 h) the input price. Cache reads are 0.025× input on Fable 5.1/5.2
// (and Mythos 5.1/5.2), 0.05× on Opus 5.5 and Sonnet 5.5, and 0.1× on every
// other model (Anthropic's pricing, #154). Haiku 5.5 is priced at its
// ≤100K-prompt tier. Claude Haiku 4.5 is left out until its price is checked
// against the pricing page: its calls stay unpriced, never guessed.
var listPrices = map[string]ListPriceModel{
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite5m: 5, CacheWrite1h: 8},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10, CacheWrite5m: 2.5, CacheWrite1h: 4},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite5m: 0.125, CacheWrite1h: 0.20},
	// Earlier and other current models seen in covey's data (#154).
	"claude-fable-5-2":  {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite5m: 10, CacheWrite1h: 16},
	"claude-mythos-5-2": {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite5m: 10, CacheWrite1h: 16},
	"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite5m: 12.5, CacheWrite1h: 20},
	"claude-mythos-5-1": {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite5m: 12.5, CacheWrite1h: 20},
	"claude-fable-5":    {Input: 10, Output: 50, CacheRead: 1, CacheWrite5m: 12.5, CacheWrite1h: 20},
	"claude-mythos-5":   {Input: 10, Output: 50, CacheRead: 1, CacheWrite5m: 12.5, CacheWrite1h: 20},
	"claude-opus-5":     {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10},
	"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite5m: 2.5, CacheWrite1h: 4},
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
	// A dated id ("claude-haiku-4-5-20251001") resolves to its base id. Only a
	// date extends an id: "claude-sonnet-5-7" is a different model, not a dated
	// Sonnet 5, and stays unpriced rather than borrowing Sonnet 5's price.
	if j := strings.LastIndexByte(m, '-'); j > 0 && isDate(m[j+1:]) {
		if p, ok := listPrices[m[:j]]; ok {
			return p, true
		}
	}
	return ListPriceModel{}, false
}

// isDate: an id's date suffix, eight digits (YYYYMMDD).
func isDate(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Cost prices tokens. Output already includes thinking tokens, as Claude Code reports them.
func (p ListPriceModel) Cost(t ModelTokens) float64 {
	const perMillion = 1_000_000.0
	return (float64(t.Input)*p.Input+float64(t.Output)*p.Output+float64(t.CacheRead)*p.CacheRead+
		float64(t.CacheWrite5m)*p.CacheWrite5m+float64(t.CacheWrite1h)*p.CacheWrite1h)/perMillion +
		float64(t.WebSearches)*WebSearchUSD
}
