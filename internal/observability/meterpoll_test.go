package observability

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A poll source names its seat. One that fits is "meter poll: <key>#<slot>";
// one that does not keeps the prefix and as much of the key as fits, then a
// hash of key and slot, so that two slots of a long key, or two long keys
// alike at the start, never share a source and with it a link.
func TestMeterPollSourceForLongKeys(t *testing.T) {
	if got := MeterPollSourceFor("claude_code_oauth_token", 2); got != "meter poll: claude_code_oauth_token#2" {
		t.Errorf("a short key = %q", got)
	}
	key := strings.Repeat("a", 60)
	other := strings.Repeat("a", 59) + "b"
	names := map[string]bool{}
	for _, c := range []struct {
		key  string
		slot int
	}{{key, 0}, {key, 1}, {other, 0}, {"ü" + key, 0}} {
		s := MeterPollSourceFor(c.key, c.slot)
		if n := utf8.RuneCountInString(s); n > externalMaxSource {
			t.Errorf("%q is %d characters, over %d", s, n, externalMaxSource)
		}
		if err := ValidateExternalSource(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
		if !IsMeterPollSource(s) || !strings.HasPrefix(s, MeterPollSource+": ") {
			t.Errorf("%q is not recognised as a poll source", s)
		}
		if !strings.Contains(s, "~") || !strings.HasPrefix(s, MeterPollSource+": "+c.key[:20]) {
			t.Errorf("%q does not keep the key's start and a hash", s)
		}
		if s != MeterPollSourceFor(c.key, c.slot) {
			t.Errorf("%q is not stable", s)
		}
		if names[s] {
			t.Errorf("%q names two seats", s)
		}
		names[s] = true
	}
	// Exactly at the limit, the name is kept whole.
	fit := strings.Repeat("k", externalMaxSource-len("meter poll: #0"))
	if got := MeterPollSourceFor(fit, 0); got != "meter poll: "+fit+"#0" {
		t.Errorf("a key that just fits = %q", got)
	}
}
