package backlog

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"covey/web"
)

// set_stage resolves a name to a default column by the labels of all ten
// catalogues, the keys and the old German names — case-insensitive. A custom
// name ("Research", "Recherche") means no default column.
func TestStageKeyFor(t *testing.T) {
	cases := map[string]string{
		"Done": StageKeyDone, "done": StageKeyDone, "Erledigt": StageKeyDone, "erledigt": StageKeyDone,
		"Terminé": StageKeyDone, "完了": StageKeyDone, " DONE ": StageKeyDone,
		"In progress": StageKeyInProgress, "in progress": StageKeyInProgress, "in_progress": StageKeyInProgress,
		"In Arbeit": StageKeyInProgress, "En cours": StageKeyInProgress, "进行中": StageKeyInProgress,
		"Backlog": StageKeyOpen, "open": StageKeyOpen, "待办": StageKeyOpen,
	}
	for name, want := range cases {
		if got, ok := StageKeyFor(name); !ok || got != want {
			t.Errorf("StageKeyFor(%q) = %q, %v — want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"Research", "Recherche", "Review", "", "Shipped"} {
		if got, ok := StageKeyFor(name); ok {
			t.Errorf("StageKeyFor(%q) = %q, a custom name must not mean a default column", name, got)
		}
	}
}

// The API names an unnamed keyed column in English, the base language.
func TestDefaultStageName(t *testing.T) {
	for key, want := range map[string]string{
		StageKeyOpen: "Backlog", StageKeyInProgress: "In progress", StageKeyDone: "Done",
	} {
		if got := DefaultStageName(key); got != want {
			t.Errorf("DefaultStageName(%q) = %q, want %q", key, got, want)
		}
	}
}

// Every catalogue names all three columns, and no label means two of them —
// otherwise set_stage would pick whichever catalogue was read last.
func TestStageLabelsAreUnambiguous(t *testing.T) {
	dir, err := web.Locales()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		n++
		raw, err := fs.ReadFile(dir, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Agent struct {
				Backlog struct {
					DefaultStage map[string]string `json:"defaultStage"`
				} `json:"backlog"`
			} `json:"agent"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		labels := doc.Agent.Backlog.DefaultStage
		for _, d := range DefaultStages {
			label := strings.ToLower(strings.TrimSpace(labels[d.Key]))
			if label == "" {
				t.Errorf("%s: agent.backlog.defaultStage.%s is missing", e.Name(), d.Key)
				continue
			}
			if other, ok := seen[label]; ok && other != d.Key {
				t.Errorf("%s: %q is the label of %s and of %s", e.Name(), label, other, d.Key)
			}
			seen[label] = d.Key
		}
	}
	if n != 10 {
		t.Errorf("%d catalogues, expected 10", n)
	}
}

// A keyed column stays unnamed while it carries one of its own labels; any
// other name is a rename — including the label of a different column.
func TestCustomStageName(t *testing.T) {
	for _, name := range []string{"", "Done", "erledigt", "done", "Fatto"} {
		if got := customStageName(StageKeyDone, name); got != nil {
			t.Errorf("customStageName(done, %q) = %q, want unnamed", name, *got)
		}
	}
	for _, name := range []string{"Shipped", "Backlog", "In Arbeit"} {
		if got := customStageName(StageKeyDone, name); got == nil || *got != name {
			t.Errorf("customStageName(done, %q) = %v, want the name kept", name, got)
		}
	}
}

// The old German name and the key map onto each other, and the name a bundle
// carries for an unnamed column resolves back to that column on import.
func TestLegacyStageName(t *testing.T) {
	for _, key := range []string{StageKeyOpen, StageKeyInProgress, StageKeyDone} {
		name := LegacyStageName(key)
		if name == "" || LegacyStageKey(name) != key {
			t.Errorf("LegacyStageName(%q) = %q, which does not map back to the key", key, name)
		}
		if customStageName(key, name) != nil {
			t.Errorf("customStageName(%q, %q) is a rename, want unnamed", key, name)
		}
	}
	if got := LegacyStageName("research"); got != "" {
		t.Errorf("LegacyStageName(research) = %q, want empty", got)
	}
}
