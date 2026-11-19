package usagepush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A folder's label is its name without the encoded home directory, so that
// no user name leaves the machine.
func TestHomeLabel(t *testing.T) {
	for folder, want := range map[string]string{
		"C--Users-marco-projects-testusage":                         "projects-testusage",
		"c--Users-marco-projects-auction":                           "projects-auction",
		"D--Users-marco-AppData-Local-Temp":                         "AppData-Local-Temp",
		"C--Users-marco":                                            "~",
		"C--Users-marco--claude":                                    "-claude",
		"-home-jmarcone-lane-120-usage":                             "lane-120-usage",
		"-Users-me-src-app":                                         "src-app",
		"-root-work":                                                "work",
		"--wsl-localhost-Ubuntu-26-04-home-jmarcone-projects-pupil": "projects-pupil",
		"--wsl--Ubuntu-home-me-x":                                   "x",
	} {
		if got, ok := HomeLabel(folder, nil); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", folder, got, ok, want)
		}
	}
	// no home in the name: the label might hold a user name
	for _, folder := range []string{"D--work-x", "-opt-x", "-srv-me-x", "-homework-x", "-rootfs-x"} {
		if got, ok := HomeLabel(folder, nil); ok {
			t.Errorf("%s: label %q, want none", folder, got)
		}
	}

	// --home-prefix, as a path or encoded; a user name with a dot needs it
	if got, _ := HomeLabel("C--Users-jean-marc-projects-x", nil); got != "marc-projects-x" {
		t.Errorf("derived: %q", got)
	}
	for _, v := range []string{`C:\Users\jean.marc`, `C:\Users\jean.marc\`, "C--Users-jean-marc", "c--users-jean-marc-"} {
		p, err := HomePrefix(v)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := HomeLabel("C--Users-jean-marc-projects-x", []string{p}); !ok || got != "projects-x" {
			t.Errorf("--home-prefix %s (%s): %q %v", v, p, got, ok)
		}
	}
	unix, _ := HomePrefix("/home/u")
	if unix != "-home-u" {
		t.Fatalf("%q", unix)
	}
	homes := []string{unix, "-home-u-work"}
	for folder, want := range map[string]string{"-home-u-x": "x", "-home-u-work-y": "y", "-home-u": "~"} {
		if got, ok := HomeLabel(folder, homes); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", folder, got, ok, want)
		}
	}
	// a prefix ends at a "-": -home-u is not the home of -home-uu-x; and a
	// given prefix replaces the derived ones
	for _, folder := range []string{"-home-uu-x", "C--Users-marco-x"} {
		if got, ok := HomeLabel(folder, homes); ok {
			t.Errorf("%s: %q", folder, got)
		}
	}
	if _, err := HomePrefix(" - "); err == nil {
		t.Error("an empty --home-prefix accepted")
	}
}

// projectsRoot makes ~/.claude/projects with the given project folders, each
// holding one call per message id given.
func projectsRoot(t *testing.T, folders map[string][]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	for f, ids := range folders {
		if len(ids) == 0 {
			writeFile(t, filepath.Join(root, f, "memory", "MEMORY.md"), "notes")
			continue
		}
		var lines []string
		for _, id := range ids {
			lines = append(lines, callLine(id, "2026-11-18T10:00:00Z", ""))
		}
		writeFile(t, filepath.Join(root, f, "0b5c2f9e-1111-4222-8333-944445555666.jsonl"), lines...)
	}
	return root
}

func sources(ps []Project) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Source)
	}
	return out
}

func TestProjectsLabelsEachFolder(t *testing.T) {
	root := projectsRoot(t, map[string][]string{
		"C--Users-marco-projects-testusage": {"m1"},
		"c--Users-marco-projects-auction":   {"m2"},
		"C--Users-marco-projects-auction":   {"m3"}, // the same directory on Windows
		"C--Users-marco-AppData-Local-Temp": nil,    // transcripts cleaned up
	})
	ps, err := Projects([]string{root}, LabelRules{Labels: map[string]string{"C--Users-marco-projects-testusage": "covey operator"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sources(ps), "|"); got != "AppData-Local-Temp|covey operator|projects-auction" {
		t.Fatalf("sources %s", got)
	}
	if len(ps[2].Paths) != 2 {
		t.Errorf("C-- and c-- of one directory are one project: %+v", ps[2])
	}

	// --source is a prefix
	ps, err = Projects([]string{root}, LabelRules{Prefix: "home", Labels: map[string]string{"c--Users-marco-projects-testusage": "covey operator"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sources(ps), "|"); got != "home: AppData-Local-Temp|home: covey operator|home: projects-auction" {
		t.Fatalf("sources %s", got)
	}

	// a project folder named as the root is one project
	ps, err = Projects([]string{filepath.Join(root, "C--Users-marco-projects-testusage")}, LabelRules{})
	if err != nil || strings.Join(sources(ps), "|") != "projects-testusage" {
		t.Fatalf("%v %v", sources(ps), err)
	}
	// ... also when it holds only session folders
	sessOnly := filepath.Join(t.TempDir(), "-home-u-p")
	writeFile(t, filepath.Join(sessOnly, "0b5c2f9e-1111-4222-8333-944445555666", "subagents", "agent-a.jsonl"), callLine("s", "2026-11-18T10:00:00Z", ""))
	ps, err = Projects([]string{sessOnly}, LabelRules{})
	if err != nil || strings.Join(sources(ps), "|") != "p" {
		t.Fatalf("%v %v", sources(ps), err)
	}
}

func TestProjectsRefuses(t *testing.T) {
	root := projectsRoot(t, map[string][]string{
		"C--Users-marco-projects-x": {"m1"},
		"-home-marco-projects-x":    {"m2"},
		"D--work-y":                 {"m3"},
	})
	known := map[string]string{"D--work-y": "y"}
	for name, c := range map[string]struct {
		roots []string
		rules LabelRules
		want  string
	}{
		"two folders, one label": {[]string{root}, LabelRules{Labels: known}, "both get the source"},
		"unknown home":           {[]string{root}, LabelRules{Labels: map[string]string{"-home-marco-projects-x": "wsl x"}}, "D--work-y"},
		"unknown --label folder": {[]string{root}, LabelRules{Labels: map[string]string{"D--work-y": "y", "-home-marco-projects-x": "z", "C--nope": "n"}}, "no such project folder"},
		"label too long": {[]string{root}, LabelRules{Prefix: strings.Repeat("p", 60), Labels: map[string]string{"D--work-y": "y", "-home-marco-projects-x": "z"}},
			"give it a --label"},
		"session folder": {[]string{filepath.Join(root, "D--work-y", "0b5c2f9e-1111-4222-8333-944445555666")}, LabelRules{}, ""},
		"no transcripts": {[]string{t.TempDir()}, LabelRules{}, "no *.jsonl"},
	} {
		if c.want == "" { // the session folder must exist for this case
			writeFile(t, filepath.Join(c.roots[0], "subagents", "a.jsonl"), callLine("q", "2026-11-18T10:00:00Z", ""))
			c.want = "one session's folder"
		}
		if _, err := Projects(c.roots, c.rules); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the user names a home: the collision and the unknown home are gone
	ps, err := Projects([]string{root}, LabelRules{Homes: []string{"C--Users-marco"}, Labels: map[string]string{"D--work-y": "y", "-home-marco-projects-x": "wsl x"}})
	if err != nil || strings.Join(sources(ps), "|") != "projects-x|wsl x|y" {
		t.Fatalf("%v %v", sources(ps), err)
	}
}

// Each project is read on its own, with its own Stats; a call found in two
// project folders is kept in one of them only.
func TestReadProjects(t *testing.T) {
	root := projectsRoot(t, map[string][]string{
		"-home-u-a": {"a1", "both"},
		"-home-u-b": {"b1", "both"},
		"-home-u-c": nil,
	})
	// a broken line in b, written long ago: b's problem, not a's
	bad := filepath.Join(root, "-home-u-b", "old.jsonl")
	writeFile(t, bad, `{"type":"assistant","message":{"usage":{"input_tok`, "{}")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(bad, old, old)
	ps, err := Projects([]string{root}, LabelRules{})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := ReadProjects(ps, "h", time.Time{})
	if err != nil || len(reads) != 3 {
		t.Fatalf("%d %v", len(reads), err)
	}
	ids := func(rows []Row) string {
		var s []string
		for _, r := range rows {
			s = append(s, r.MessageID)
		}
		return strings.Join(s, ",")
	}
	a, b, c := reads[0], reads[1], reads[2]
	if a.Source != "a" || b.Source != "b" || c.Source != "c" {
		t.Fatalf("%s %s %s", a.Source, b.Source, c.Source)
	}
	// a full tie goes to the later source
	if ids(a.Rows) != "a1" || ids(b.Rows) != "b1,both" || a.Elsewhere != 1 || b.Elsewhere != 0 {
		t.Errorf("a %s (%d), b %s (%d)", ids(a.Rows), a.Elsewhere, ids(b.Rows), b.Elsewhere)
	}
	if a.Stats.Suspect != 0 || b.Stats.Suspect != 1 || len(c.Rows) != 0 || c.Stats.Files != 0 {
		t.Errorf("stats a %+v b %+v c %+v", a.Stats, b.Stats, c.Stats)
	}
	for _, r := range append(a.Rows, b.Rows...) {
		if r.SessionID != "0b5c2f9e-1111-4222-8333-944445555666" || r.Origin != OriginMain {
			t.Errorf("%+v", r)
		}
	}
}
