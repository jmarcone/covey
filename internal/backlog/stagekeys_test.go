package backlog

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

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

// Every catalogue names all three columns, and the table set_stage reads
// builds without a conflict: no label means two columns, not even against the
// keys and the old German names, which no catalogue lists.
func TestStageLabelsAreUnambiguous(t *testing.T) {
	dir, err := web.Locales()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageKeyTable(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
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
		for _, d := range DefaultStages {
			if strings.TrimSpace(doc.Agent.Backlog.DefaultStage[d.Key]) == "" {
				t.Errorf("%s: agent.backlog.defaultStage.%s is missing", e.Name(), d.Key)
			}
		}
	}
	if n != 10 {
		t.Errorf("%d catalogues, expected 10", n)
	}
}

// A label that would mean a second column is refused, whatever it collides
// with: another catalogue's label, a key, an old German name. So is a
// catalogue that cannot be read, rather than being skipped.
func TestStageKeyTableRefusesConflicts(t *testing.T) {
	catalogue := func(open, inProgress, done string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"agent":{"backlog":{"defaultStage":{` +
			`"open":"` + open + `","in_progress":"` + inProgress + `","done":"` + done + `"}}}}`)}
	}
	good := catalogue("Backlog", "In progress", "Done")
	if _, err := stageKeyTable(fstest.MapFS{"en.json": good, "de.json": catalogue("Backlog", "In Arbeit", "Erledigt")}); err != nil {
		t.Fatalf("consistent catalogues: %v", err)
	}
	for name, dir := range map[string]fstest.MapFS{
		"two catalogues":   {"en.json": good, "xx.json": catalogue("Backlog", "Done", "Finished")},
		"a key":            {"en.json": good, "xx.json": catalogue("Backlog", "Open", "Done")},
		"an old name":      {"en.json": good, "xx.json": catalogue("Erledigt", "In progress", "Done")},
		"one catalogue":    {"xx.json": catalogue("Backlog", "Same", "same")},
		"a broken file":    {"en.json": good, "xx.json": &fstest.MapFile{Data: []byte(`{"agent":`)}},
		"an unknown key":   {"xx.json": &fstest.MapFile{Data: []byte(`{"agent":{"backlog":{"defaultStage":{"later":"Later"}}}}`)}},
		"an unreadable fs": {"en.json": &fstest.MapFile{Mode: fs.ModeDir}},
	} {
		if _, err := stageKeyTable(dir); err == nil {
			t.Errorf("%s: a conflicting or unreadable table must be refused", name)
		}
	}
}

// The old German name and the key map onto each other: a keyed column that
// carries it is not renamed, and the name a bundle from before the key
// carries resolves back to that column on import.
func TestLegacyStageName(t *testing.T) {
	for _, key := range []string{StageKeyOpen, StageKeyInProgress, StageKeyDone} {
		name := LegacyStageName(key)
		if name == "" || LegacyStageKey(name) != key {
			t.Errorf("LegacyStageName(%q) = %q, which does not map back to the key", key, name)
		}
	}
	if got := LegacyStageName("research"); got != "" {
		t.Errorf("LegacyStageName(research) = %q, want empty", got)
	}
}

// A keyed column is renamed exactly when it no longer carries its old German
// name — a label of its own in another language is a name a human chose.
func TestStageRenamed(t *testing.T) {
	key := func(k string) *string { return &k }
	for _, c := range []struct {
		st   Stage
		want bool
	}{
		{Stage{Name: "Erledigt", Key: key(StageKeyDone)}, false},
		{Stage{Name: "Shipped", Key: key(StageKeyDone)}, true},
		{Stage{Name: "Done", Key: key(StageKeyDone)}, true},
		{Stage{Name: "Backlog", Key: key(StageKeyDone)}, true},
		{Stage{Name: "Erledigt"}, false},
	} {
		if got := stageRenamed(c.st); got != c.want {
			t.Errorf("stageRenamed(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
}
