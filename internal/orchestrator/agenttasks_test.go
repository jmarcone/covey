package orchestrator

import (
	"strings"
	"testing"
	"unicode/utf8"

	"covey/internal/backlog"
)

// The list shows one line of a result, or of the error when there is none.
func TestResultHeadIsOneLine(t *testing.T) {
	long := "Done.\n\n" + strings.Repeat("ö", 300)
	empty := "  "
	failed := "action gitlab/commit:\nhttp 409"
	for name, c := range map[string]struct {
		task backlog.Task
		want string
	}{
		"nothing":           {backlog.Task{}, ""},
		"short result":      {backlog.Task{Result: ptr("all green")}, "all green"},
		"error without one": {backlog.Task{Error: &failed}, "action gitlab/commit: http 409"},
		"empty result":      {backlog.Task{Result: &empty, Error: &failed}, "action gitlab/commit: http 409"},
		"long, over lines":  {backlog.Task{Result: &long}, "Done. " + strings.Repeat("ö", 194)},
	} {
		got := resultHead(c.task)
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
		if utf8.RuneCountInString(got) > resultHeadRunes || strings.Contains(got, "\n") {
			t.Errorf("%s: not one line of at most %d characters: %q", name, resultHeadRunes, got)
		}
	}
}

// get_task takes the start of an id from 8 hex characters on; anything that
// is not the start of a uuid never reaches the database.
func TestTaskPrefix(t *testing.T) {
	for _, ok := range []string{"abcdef01", "abcdef01-", "abcdef01-12", "abcdef01-1234-4", "abcdef01-1234-4abc-8def-0123"} {
		if !taskPrefix.MatchString(ok) {
			t.Errorf("%q is a prefix", ok)
		}
	}
	for _, bad := range []string{"", "abcdef0", "abcdef012", "abcdef0g", "abcdef01%", "abcdef01_", "' OR 1=1"} {
		if taskPrefix.MatchString(bad) {
			t.Errorf("%q is not a prefix", bad)
		}
	}
}

func ptr(s string) *string { return &s }
