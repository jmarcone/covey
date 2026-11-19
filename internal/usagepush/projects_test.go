package usagepush

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests do not depend on the home directory of whoever runs them.
func init() {
	userHomeDir = func() (string, error) { return "", errors.New("no home in tests") }
	readDir = func(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist }
}

// testHomes are the homes the package tests know for sure.
var testHomes = []string{"C--Users-marco", "-home-marco", "-home-u", "-home-me"}

// Without a known home, a label is given only to a bare home and below
// /root: any text after a home's start could be the rest of a user name.
func TestHomeLabelFailsClosedWithoutAKnownHome(t *testing.T) {
	for folder, want := range map[string]string{
		"C--Users-marco": "~",
		"c--users-marco": "~",
		"-home-jmarcone": "~",
		"-Users-me":      "~",
		"-root-work":     "work",
		"-root":          "~",
	} {
		if got, err := HomeLabel(folder, nil, nil, nil); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", folder, got, err, want)
		}
	}
	for folder, want := range map[string]error{
		"C--Users-marco-projects-testusage":          ErrAmbiguousHome,
		"C--Users-jean-marc":                         ErrAmbiguousHome, // the bare home of jean-marc, or project marc of jean
		"C--Users-jean-marc-projects-x":              ErrAmbiguousHome,
		"-home-dev-proj":                             ErrAmbiguousHome,
		"--wsl-localhost-Ubuntu-26-04-home-dev-proj": ErrAmbiguousHome,
		"C--Users--x":                                ErrAmbiguousHome,
		"D--work-x":                                  ErrNoHome,
		"-opt-x":                                     ErrNoHome,
		"-srv-me-x":                                  ErrNoHome,
		"-homework-x":                                ErrNoHome,
		"-rootfs-x":                                  ErrNoHome,
		"-tmp-proj-x":                                ErrNoHome,
		"--wsl--U-x-y":                               ErrNoHome,
	} {
		if got, err := HomeLabel(folder, nil, nil, nil); !errors.Is(err, want) {
			t.Errorf("%s: %q %v, want %v", folder, got, err, want)
		}
	}
}

// A user name with a dash, a dot or a space is encoded with dashes; the
// folder name alone cannot tell where it ends. The home above the named
// .claude/projects (also a Windows home seen from WSL) and this user's home
// tell it, and a folder that fits two candidate homes is refused.
func TestHomeLabelWithKnownHomes(t *testing.T) {
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	for root, want := range map[string]string{
		"/mnt/c/Users/Marco Rossi/.claude/projects":                  "C--Users-Marco-Rossi",
		"/home/first.last/.claude/projects/-home-first-last-x":       "-home-first-last",
		`C:\Users\jean-marc\.claude\projects`:                        "C--Users-jean-marc",
		"/mnt/d/Users/jean-marc/.claude/projects/D--Users-jean-marc": "D--Users-jean-marc",
	} {
		if got := knownHomes([]string{root}); !has(got, want) {
			t.Errorf("%s: known homes %v, want %s among them", root, got, want)
		}
	}
	if got := knownHomes([]string{"/srv/transcripts"}); len(got) != 0 {
		t.Errorf("no .claude/projects, no home: %v", got)
	}
	old := userHomeDir
	userHomeDir = func() (string, error) { return "/home/first.last", nil }
	if got := knownHomes(nil); !has(got, "-home-first-last") {
		t.Errorf("the user's home: %v", got)
	}
	userHomeDir = old

	known := []string{"C--Users-jean-marc", "-home-first-last", "C--Users-Marco-Rossi"}
	for folder, want := range map[string]string{
		"C--Users-jean-marc-projects-x":            "projects-x",
		"c--users-jean-marc":                       "~",
		"--wsl-localhost-Ubuntu-home-first-last-w": "w", // the known Linux home, seen from Windows
		"-home-first-last-q":                       "q",
		"C--Users-Marco-Rossi-p":                   "p",
	} {
		if got, err := HomeLabel(folder, nil, known, nil); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", folder, got, err, want)
		}
	}
	// outside the known homes nothing is guessed
	for _, folder := range []string{"D--Users-jean-marc-y", "C--Users-jean-z", "-home-first-z", "C--Users-Marco-z", "-home-bob-x", "--wsl--U-home-dev-proj"} {
		if got, err := HomeLabel(folder, nil, known, nil); err == nil {
			t.Errorf("%s: %q, want an error", folder, got)
		}
	}

	// marco is known, marco-rossi is another user beside him: a folder that
	// fits both homes is refused, one that fits only one is labelled
	oldRead := readDir
	readDir = func(dir string) ([]fs.DirEntry, error) {
		if filepath.ToSlash(dir) == "/mnt/c/Users" || dir == `C:\Users` {
			return []fs.DirEntry{fakeEntry{"marco", fs.ModeDir}, fakeEntry{"marco.rossi", fs.ModeDir}, fakeEntry{"bob", fs.ModeDir}}, nil
		}
		return nil, fs.ErrNotExist
	}
	defer func() { readDir = oldRead }()
	others := otherHomes([]string{"C--Users-marco"})
	if !has(others, "C--Users-marco-rossi") || !has(others, "C--Users-bob") {
		t.Fatalf("other homes %v", others)
	}
	for folder, want := range map[string]string{
		"C--Users-marco-projects-x": "projects-x",
		"C--Users-marco":            "~",
		"C--Users-marco-rossi":      "",
		"C--Users-marco-rossi-x":    "",
		"C--Users-bob-x":            "x", // bob's home, listed: his name is not in the label
	} {
		got, err := HomeLabel(folder, nil, []string{"C--Users-marco"}, others)
		if want == "" && !errors.Is(err, ErrAmbiguousHome) || want != "" && (err != nil || got != want) {
			t.Errorf("%s: %q %v, want %q", folder, got, err, want)
		}
	}

	// --home-prefix, as a path or encoded, replaces the rest
	for _, v := range []string{`C:\Users\jean.marc`, `C:\Users\jean.marc\`, "C--Users-jean-marc", "c--users-jean-marc-"} {
		p, err := HomePrefix(v)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := HomeLabel("C--Users-jean-marc-projects-x", []string{p}, nil, nil); err != nil || got != "projects-x" {
			t.Errorf("--home-prefix %s (%s): %q %v", v, p, got, err)
		}
	}
	unix, _ := HomePrefix("/home/u")
	homes := []string{unix, "-home-u-work"}
	for folder, want := range map[string]string{"-home-u-x": "x", "-home-u-work-y": "y", "-home-u": "~"} {
		if got, err := HomeLabel(folder, homes, nil, nil); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", folder, got, err, want)
		}
	}
	for _, folder := range []string{"-home-uu-x", "C--Users-marco-x"} {
		if got, err := HomeLabel(folder, homes, nil, nil); err == nil {
			t.Errorf("%s: %q", folder, got)
		}
	}
	if _, err := HomePrefix(" - "); err == nil {
		t.Error("an empty --home-prefix accepted")
	}
}

// projectsRoot makes a directory of project folders, each holding one call
// per message id given (none: only memory/).
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
		"C--Users-marco-AppData-Local-Temp": nil,    // only memory/: no source
		"D--work-cleaned-up":                nil,    // no source, so its unknown home does not matter
	})
	// a transcript without any call is a source (it claims, with no calls)
	writeFile(t, filepath.Join(root, "C--Users-marco-projects-quiet", "s.jsonl"), `{"type":"user","message":{"content":"hi"}}`)
	ps, err := Projects([]string{root}, LabelRules{Known: testHomes, Labels: map[string]string{
		"C--Users-marco-projects-testusage": "covey operator",
		"C--Users-marco-AppData-Local-Temp": "temp", // a label for a folder without transcripts is no error
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sources(ps), "|"); got != "covey operator|projects-auction|projects-quiet" {
		t.Fatalf("sources %s", got)
	}
	if len(ps[1].Paths) != 2 || ps[1].Key != "C--Users-marco-projects-auction" {
		t.Errorf("C-- and c-- of one directory are one project: %+v", ps[1])
	}

	// --source is a prefix
	ps, err = Projects([]string{root}, LabelRules{Known: testHomes, Prefix: "home", Labels: map[string]string{"c--Users-marco-projects-testusage": "covey operator"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sources(ps), "|"); got != "home: covey operator|home: projects-auction|home: projects-quiet" {
		t.Fatalf("sources %s", got)
	}

	// a project folder named as the root is one project
	ps, err = Projects([]string{filepath.Join(root, "C--Users-marco-projects-testusage")}, LabelRules{Known: testHomes})
	if err != nil || strings.Join(sources(ps), "|") != "projects-testusage" {
		t.Fatalf("%v %v", sources(ps), err)
	}
	// ... also when it holds only session folders
	sessOnly := filepath.Join(t.TempDir(), "-home-u-p")
	writeFile(t, filepath.Join(sessOnly, "0b5c2f9e-1111-4222-8333-944445555666", "subagents", "agent-a.jsonl"), callLine("s", "2026-11-18T10:00:00Z", ""))
	ps, err = Projects([]string{sessOnly}, LabelRules{Known: testHomes})
	if err != nil || strings.Join(sources(ps), "|") != "p" {
		t.Fatalf("%v %v", sources(ps), err)
	}
}

// The home above the named .claude/projects is known, whatever the user name.
func TestProjectsKnowTheHomeOfTheStore(t *testing.T) {
	for _, user := range []string{"Marco Rossi", "first.last", "jean-marc"} {
		home := filepath.Join(t.TempDir(), user)
		root := filepath.Join(home, ".claude", "projects")
		enc := EncodePath(home)
		writeFile(t, filepath.Join(root, enc+"-projects-x", "s.jsonl"), callLine("m1", "2026-11-18T10:00:00Z", ""))
		writeFile(t, filepath.Join(root, enc, "s.jsonl"), callLine("m2", "2026-11-18T10:00:00Z", ""))
		ps, err := Projects([]string{root}, LabelRules{Known: testHomes})
		if err != nil || strings.Join(sources(ps), "|") != "projects-x|~" {
			t.Errorf("%s: %v %v", user, sources(ps), err)
		}
	}
	// a folder whose user name is only the start of a known one is refused
	root := projectsRoot(t, map[string][]string{"C--Users-jean-marc-a": {"m1"}, "C--Users-jean-b": {"m2"}})
	_, err := Projects([]string{root}, LabelRules{Known: []string{"C--Users-jean-marc"}})
	if err == nil || !strings.Contains(err.Error(), "C--Users-jean-b") || strings.Contains(err.Error(), "jean-marc-a") || !strings.Contains(err.Error(), "--home-prefix") {
		t.Fatalf("%v", err)
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
		"two folders, one label": {[]string{root}, LabelRules{Known: testHomes, Labels: known}, "both get the source"},
		"unknown home":           {[]string{root}, LabelRules{Known: testHomes, Labels: map[string]string{"-home-marco-projects-x": "wsl x"}}, "D--work-y"},
		"unknown --label folder": {[]string{root}, LabelRules{Known: testHomes, Labels: map[string]string{"D--work-y": "y", "-home-marco-projects-x": "z", "C--nope": "n"}}, "no such project folder"},
		"label too long": {[]string{root}, LabelRules{Known: testHomes, Prefix: strings.Repeat("p", 60), Labels: map[string]string{"D--work-y": "y", "-home-marco-projects-x": "z"}},
			"give it a --label"},
		"no transcripts": {[]string{t.TempDir()}, LabelRules{Known: testHomes}, "no *.jsonl"},
	} {
		if _, err := Projects(c.roots, c.rules); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	sess := filepath.Join(root, "D--work-y", "0b5c2f9e-1111-4222-8333-944445555666")
	writeFile(t, filepath.Join(sess, "subagents", "a.jsonl"), callLine("q", "2026-11-18T10:00:00Z", ""))
	if _, err := Projects([]string{sess}, LabelRules{Known: testHomes}); err == nil || !strings.Contains(err.Error(), "one session's folder") {
		t.Errorf("session folder: %v", err)
	}
	// the user names a home: the collision and the unknown home are gone
	ps, err := Projects([]string{root}, LabelRules{Homes: []string{"C--Users-marco"}, Labels: map[string]string{"D--work-y": "y", "-home-marco-projects-x": "wsl x"}})
	if err != nil || strings.Join(sources(ps), "|") != "projects-x|wsl x|y" {
		t.Fatalf("%v %v", sources(ps), err)
	}
}

// A transcript or a session folder at the top of the store is a stray, not a
// project called "projects".
func TestProjectsRefusesStraysAtTheTopOfTheStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "home", ".claude", "projects")
	writeFile(t, filepath.Join(store, "-home-u-a", "s.jsonl"), callLine("m1", "2026-11-18T10:00:00Z", ""))
	writeFile(t, filepath.Join(store, "stray.jsonl"), callLine("m2", "2026-11-18T10:00:00Z", ""))
	if _, err := Projects([]string{store}, LabelRules{Known: testHomes}); err == nil || !strings.Contains(err.Error(), "stray.jsonl beside its project folders") {
		t.Errorf("stray transcript: %v", err)
	}
	other := filepath.Join(t.TempDir(), "copy")
	writeFile(t, filepath.Join(other, "-home-u-a", "s.jsonl"), callLine("m1", "2026-11-18T10:00:00Z", ""))
	writeFile(t, filepath.Join(other, "0b5c2f9e-1111-4222-8333-944445555666", "subagents", "x.jsonl"), callLine("m2", "2026-11-18T10:00:00Z", ""))
	if _, err := Projects([]string{other}, LabelRules{Known: testHomes}); err == nil || !strings.Contains(err.Error(), "0b5c2f9e-1111-4222-8333-944445555666 beside") {
		t.Errorf("stray session folder: %v", err)
	}
}

// Each project is read on its own, with its own Stats; a call found in two
// project folders is kept in one of them only, chosen without the label.
func TestReadProjects(t *testing.T) {
	root := projectsRoot(t, map[string][]string{
		"-home-u-a": {"a1", "both"},
		"-home-u-b": {"b1", "both"},
		"-home-u-d": nil, // no transcripts: no source
	})
	// c has a transcript without any call: a source with no rows
	writeFile(t, filepath.Join(root, "-home-u-c", "s.jsonl"), `{"type":"user","message":{"content":"hi"}}`)
	// the earlier copy wins, whatever the folder
	writeFile(t, filepath.Join(root, "-home-u-a", "late.jsonl"), callLine("early", "2026-11-18T11:00:00Z", ""))
	writeFile(t, filepath.Join(root, "-home-u-b", "early.jsonl"), callLine("early", "2026-11-18T09:00:00Z", ""))
	// a broken line in b, written long ago: b's problem, not a's
	bad := filepath.Join(root, "-home-u-b", "old.jsonl")
	writeFile(t, bad, `{"type":"assistant","message":{"usage":{"input_tok`, "{}")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(bad, old, old)
	ids := func(rows []Row) string {
		var s []string
		for _, r := range rows {
			s = append(s, r.MessageID)
		}
		return strings.Join(s, ",")
	}
	// relabelling moves no call: the same split with the labels swapped
	for _, labels := range []map[string]string{nil, {"-home-u-a": "zz", "-home-u-b": "aa"}} {
		ps, err := Projects([]string{root}, LabelRules{Known: testHomes, Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		reads, err := ReadProjects(ps, "h", time.Time{})
		if err != nil || len(reads) != 3 {
			t.Fatalf("%d %v", len(reads), err)
		}
		by := map[string]ProjectRead{}
		for _, r := range reads {
			by[r.Key] = r
		}
		a, b, c := by["-home-u-a"], by["-home-u-b"], by["-home-u-c"]
		// a full tie goes to the folder whose name sorts first
		if ids(a.Rows) != "a1,both" || ids(b.Rows) != "early,b1" || a.Elsewhere != 1 || b.Elsewhere != 1 {
			t.Errorf("%v: a %s (%d), b %s (%d)", labels, ids(a.Rows), a.Elsewhere, ids(b.Rows), b.Elsewhere)
		}
		if a.Stats.Suspect != 0 || b.Stats.Suspect != 1 || len(c.Rows) != 0 || c.Stats.Files != 1 {
			t.Errorf("stats a %+v b %+v c %+v", a.Stats, b.Stats, c.Stats)
		}
	}
}

// A folder that cannot be read whole, or whose session folder is a link, is
// no empty folder: it stays a source, and its problem withholds its claim.
func TestProjectsKeepFoldersTheyCannotReadWhole(t *testing.T) {
	root := projectsRoot(t, map[string][]string{"-home-u-a": {"a1"}, "-home-u-empty": nil})
	if err := os.MkdirAll(filepath.Join(root, "-home-u-locked", "locked"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "s.jsonl"), callLine("l1", "2026-11-18T10:00:00Z", ""))
	if err := os.MkdirAll(filepath.Join(root, "-home-u-linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := os.Symlink(elsewhere, filepath.Join(root, "-home-u-linked", "0b5c2f9e-1111-4222-8333-944445555666")) == nil

	// "locked" cannot be listed
	old := walkDir
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		return old(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == "locked" {
				if r := fn(p, d, nil); r != nil {
					return r
				}
				if r := fn(p, d, fs.ErrPermission); r != nil {
					return r
				}
				return fs.SkipDir
			}
			return fn(p, d, err)
		})
	}
	defer func() { walkDir = old }()

	ps, err := Projects([]string{root}, LabelRules{Known: testHomes})
	if err != nil {
		t.Fatal(err)
	}
	want := "a|locked"
	if linked {
		want = "a|linked|locked"
	}
	if got := strings.Join(sources(ps), "|"); got != want {
		t.Fatalf("sources %s, want %s", got, want)
	}
	reads, err := ReadProjects(ps, "h", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reads {
		switch r.Source {
		case "a":
			if len(r.Stats.Unread)+len(r.Stats.Symlinks) != 0 || len(r.Rows) != 1 {
				t.Errorf("a: %+v", r.Stats)
			}
		case "locked":
			if len(r.Stats.Unread) != 1 || !strings.HasSuffix(r.Stats.Unread[0], "locked") {
				t.Errorf("locked: %+v", r.Stats)
			}
		case "linked":
			if len(r.Stats.Symlinks) != 1 || len(r.Rows) != 0 {
				t.Errorf("linked: %+v", r.Stats)
			}
		}
	}
}

// Below a project folder, a subagents/ with no session folder above it never
// takes the project folder's name (which holds the home) as its session.
func TestReadProjectsNeverNamesASessionAfterTheFolder(t *testing.T) {
	root := projectsRoot(t, map[string][]string{"-home-me-p": {"m1"}})
	writeFile(t, filepath.Join(root, "-home-me-p", "subagents", "agent-a.jsonl"), callLine("s1", "2026-11-18T10:00:00Z", ""))
	ps, err := Projects([]string{root}, LabelRules{Known: testHomes})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := ReadProjects(ps, "h", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range reads[0].Rows {
		if r.MessageID == "s1" {
			found = true
			if r.SessionID != UnknownSession || r.Origin != OriginSubagent {
				t.Errorf("%+v", r)
			}
		}
	}
	if !found {
		t.Fatal("no subagent call read")
	}
}
