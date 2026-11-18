package backlog

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"covey/web"
)

// The keys of the three default columns. A keyed column is found by its key —
// by auto-follow (stateStage) and by set_stage — whatever it is called.
const (
	StageKeyOpen       = "open"
	StageKeyInProgress = "in_progress"
	StageKeyDone       = "done"
)

// stageLabelKey is where the interface keeps the label of a keyed column:
// agent.backlog.defaultStage.<key> in web/src/locales/*.json. The Go side reads
// the same catalogues (package web embeds them) rather than a copy of the
// table: the parity test keeps all ten in step, and a wording changed there is
// understood by set_stage without a second edit here.
const stageLabelKey = "agent.backlog.defaultStage."

// legacyStageNames are the names the default columns carried before they got a
// key (migrations 0004 and 0148), and still carry as long as nobody renamed
// them: an instance of the code before the key finds them by exactly these
// names. They resolve too — an agent's playbook written against the old board
// says "Erledigt" — and an imported bundle from before the key recognises its
// default columns by them.
var legacyStageNames = map[string]string{
	"Backlog":   StageKeyOpen,
	"In Arbeit": StageKeyInProgress,
	"Erledigt":  StageKeyDone,
}

var stageLabels struct {
	once sync.Once
	// keyOf maps every key, every old German name and every label of every
	// catalogue, lowercased, to its key.
	keyOf map[string]string
}

// stageKeyTable reads the labels of the ten catalogues into one table of
// lowercased names. A name that would mean two different columns — a label
// that equals another column's key, its old German name or its label in some
// other language — is an error rather than a silent overwrite: set_stage would
// put the task into whichever came last. So is a catalogue that cannot be read;
// the catalogues are compiled in, so either is a defect of the build, and the
// unit test reads the same table.
func stageKeyTable(dir fs.FS) (map[string]string, error) {
	keyOf := map[string]string{}
	add := func(name, key, from string) error {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" {
			return nil
		}
		if had, ok := keyOf[n]; ok && had != key {
			return fmt.Errorf("stage label %q (%s) means %s and %s", name, from, had, key)
		}
		keyOf[n] = key
		return nil
	}
	for _, k := range []string{StageKeyOpen, StageKeyInProgress, StageKeyDone} {
		if err := add(k, k, "key"); err != nil {
			return nil, err
		}
	}
	for name, k := range legacyStageNames {
		if err := add(name, k, "old name"); err != nil {
			return nil, err
		}
	}
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := fs.ReadFile(dir, e.Name())
		if err != nil {
			return nil, err
		}
		var doc struct {
			Agent struct {
				Backlog struct {
					DefaultStage map[string]string `json:"defaultStage"`
				} `json:"backlog"`
			} `json:"agent"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		for k, label := range doc.Agent.Backlog.DefaultStage {
			if !IsStageKey(k) {
				return nil, fmt.Errorf("%s: %s%s is no stage key", e.Name(), stageLabelKey, k)
			}
			if err := add(label, k, e.Name()); err != nil {
				return nil, err
			}
		}
	}
	return keyOf, nil
}

func loadStageLabels() {
	dir, err := web.Locales()
	if err == nil {
		stageLabels.keyOf, err = stageKeyTable(dir)
	}
	if err != nil {
		panic("backlog: the stage labels of web/src/locales: " + err.Error())
	}
}

// StageKeyFor says which default column a name means: a label of that column in
// any of the ten languages, the key itself or the column's old German name,
// case-insensitive. "Done", "erledigt", "In progress", "in_progress" all
// answer; "Research" does not.
func StageKeyFor(name string) (string, bool) {
	stageLabels.once.Do(loadStageLabels)
	k, ok := stageLabels.keyOf[strings.ToLower(strings.TrimSpace(name))]
	return k, ok
}

// IsStageKey says whether key is one of the three keys.
func IsStageKey(key string) bool {
	return key == StageKeyOpen || key == StageKeyInProgress || key == StageKeyDone
}

// LegacyStageKey is the key of a column that carries one of the old German
// default names exactly ("Backlog", "In Arbeit", "Erledigt"), or "".
func LegacyStageKey(name string) string {
	return legacyStageNames[name]
}

// LegacyStageName is the old German name of a keyed column ("Backlog", "In
// Arbeit", "Erledigt"), or "". A keyed column that still carries it is not
// renamed: the interface shows the label of the reader's language for it.
func LegacyStageName(key string) string {
	for name, k := range legacyStageNames {
		if k == key {
			return name
		}
	}
	return ""
}
