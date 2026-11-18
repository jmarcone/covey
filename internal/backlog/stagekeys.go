package backlog

import (
	"encoding/json"
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
// key (migrations 0004 and 0148). They still resolve — an agent's playbook
// written against the old board says "Erledigt" — and an imported bundle from
// before the key recognises its default columns by them.
var legacyStageNames = map[string]string{
	"Backlog":   StageKeyOpen,
	"In Arbeit": StageKeyInProgress,
	"Erledigt":  StageKeyDone,
}

var stageLabels struct {
	once sync.Once
	// byKey holds the English label (the base language, as in
	// web/src/i18n.ts) — the name the API returns for an unnamed keyed column.
	byKey map[string]string
	// keyOf maps every label of every catalogue, lowercased, to its key.
	keyOf map[string]string
}

func loadStageLabels() {
	stageLabels.byKey = map[string]string{}
	stageLabels.keyOf = map[string]string{}
	for _, k := range []string{StageKeyOpen, StageKeyInProgress, StageKeyDone} {
		stageLabels.keyOf[k] = k
	}
	for name, k := range legacyStageNames {
		stageLabels.keyOf[strings.ToLower(name)] = k
	}
	dir, err := web.Locales()
	if err != nil {
		return
	}
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := fs.ReadFile(dir, e.Name())
		if err != nil {
			continue
		}
		var doc struct {
			Agent struct {
				Backlog struct {
					DefaultStage map[string]string `json:"defaultStage"`
				} `json:"backlog"`
			} `json:"agent"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for k, label := range doc.Agent.Backlog.DefaultStage {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			stageLabels.keyOf[strings.ToLower(label)] = k
			if e.Name() == "en.json" {
				stageLabels.byKey[k] = label
			}
		}
	}
}

// DefaultStageName is the name an unnamed keyed column carries in the API: the
// English label, so a client that does not know `key` still has something to
// show. The key itself if even that is missing.
func DefaultStageName(key string) string {
	stageLabels.once.Do(loadStageLabels)
	if l, ok := stageLabels.byKey[key]; ok {
		return l
	}
	return key
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
// Arbeit", "Erledigt"), or "". An exported bundle names an unnamed default
// column by it: a covey from before the key finds its default columns by
// exactly these names, and the imported board keeps auto-follow there too.
func LegacyStageName(key string) string {
	for name, k := range legacyStageNames {
		if k == key {
			return name
		}
	}
	return ""
}
