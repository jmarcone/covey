package observability

import "testing"

// #154: every model seen in covey's data has a price, and a dated id resolves to
// the longest base id it extends (not to a shorter sibling like claude-opus-5).
func TestListPriceKnowsTheModelsInUse(t *testing.T) {
	for _, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2",
		"claude-fable-5-1", "claude-fable-5", "claude-opus-5", "claude-sonnet-5", "claude-mythos-5-1"} {
		if _, ok := ListPrice(m); !ok {
			t.Errorf("%s: no list price", m)
		}
	}
	if _, ok := ListPrice("claude-haiku-4-5-20251001"); ok {
		t.Errorf("claude-haiku-4-5 must stay unpriced until its price is checked")
	}
}

// A dated id resolves to its base; any other suffix is another model.
func TestListPriceDatedIDsResolveOtherSuffixesDont(t *testing.T) {
	for _, m := range []string{"claude-sonnet-5-7", "claude-opus-5-9", "claude-fable-5-3"} {
		if p, ok := ListPrice(m); ok {
			t.Errorf("%s: priced at input %v; an unknown model must stay unpriced", m, p.Input)
		}
	}
}

func TestListPriceLongestBaseIDWins(t *testing.T) {
	cases := map[string]float64{ // id -> expected input $/MTok
		"claude-opus-5-5-20261101":  4,
		"claude-opus-5-20260501":    5,
		"claude-fable-5-1-20260801": 10,
		"claude-fable-5-2-20261001": 8,
		"claude-sonnet-5-5[1m]":     2,
	}
	for i := 0; i < 50; i++ { // map iteration order is random: repeat
		for id, want := range cases {
			p, ok := ListPrice(id)
			if !ok || p.Input != want {
				t.Fatalf("%s: got input %v (ok=%v), want %v", id, p.Input, ok, want)
			}
		}
	}
	if p, _ := ListPrice("claude-fable-5-1"); p.CacheRead != 0.25 {
		t.Errorf("fable-5-1 cache read: got %v, want 0.25 (0.025x input)", p.CacheRead)
	}
	if p, _ := ListPrice("claude-opus-5"); p.CacheRead != 0.50 {
		t.Errorf("opus-5 cache read: got %v, want 0.50 (0.1x input)", p.CacheRead)
	}
}
