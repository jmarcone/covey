package backlog

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeTopic(t *testing.T) {
	ok := map[string]string{
		"pr:42":         "pr:42",
		"  issue:7 ":    "issue:7",
		"":              "",
		"   ":           "",
		"pr:1234567890": "pr:1234567890",
	}
	for in, want := range ok {
		got, err := NormalizeTopic(in)
		if err != nil || got != want {
			t.Errorf("NormalizeTopic(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"../x", "pr:../x", "pr:", "pr:0", "pr:042", "PR:42", "pr: 42", "mr:42",
		"issue:4a", "pr:42/1", "#42", "pr:" + strings.Repeat("9", 62), "cost page headline"}
	for _, in := range bad {
		_, err := NormalizeTopic(in)
		if !errors.Is(err, ErrInvalidTopic) {
			t.Errorf("NormalizeTopic(%q) must be refused, got %v", in, err)
		}
	}
	if !strings.Contains(ErrInvalidTopic.Error(), `"pr:<n>" or "issue:<n>"`) {
		t.Errorf("the error must say the format: %v", ErrInvalidTopic)
	}
	// A title-derived key of topic routing (#45) is never a sticky topic.
	for _, k := range []string{"#45", "cost page headline", ""} {
		if IsStickyTopic(k) {
			t.Errorf("%q must not be sticky", k)
		}
	}
}
