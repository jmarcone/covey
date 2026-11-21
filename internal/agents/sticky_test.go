package agents

import "testing"

func TestDefaultStickyPolicy(t *testing.T) {
	cases := map[string]string{
		"covey-dev":      StickyConditional,
		"covey-frontend": StickyConditional,
		"covey-reviewer": StickyFresh,
		"support":        StickyFresh,
		"":               StickyFresh,
	}
	for slug, want := range cases {
		if got := DefaultStickyPolicy(slug); got != want {
			t.Errorf("DefaultStickyPolicy(%q) = %q, want %q", slug, got, want)
		}
	}
	for _, p := range []string{StickyFresh, StickyConditional, StickyAlways, StickyAB} {
		if !ValidStickyPolicy(p) {
			t.Errorf("%q must be valid", p)
		}
	}
	if ValidStickyPolicy("on") || ValidStickyPolicy("") {
		t.Error("only the four policies are valid")
	}
}
