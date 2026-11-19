package usagepush

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxSource is the intake's limit on a source, in characters
// (observability.externalMaxSource).
const MaxSource = 64

// ValidSource says whether s is acceptable as the intake's source: 1 to
// MaxSource characters of UTF-8, no NUL.
func ValidSource(s string) bool {
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= MaxSource && !strings.ContainsRune(s, 0)
}

// Project is one Claude Code project folder, a source of its own under
// --source-per-project. Only Source is sent: the folder's name and path stay
// on this machine.
type Project struct {
	Source string // the source sent, "<prefix>: <label>" or the label alone
	Key    string // the folder's name, its drive letter in upper case
	// Folders are the folder's names: two when Claude Code wrote one Windows
	// directory once with an upper and once with a lower drive letter
	// (C--Users-… and c--Users-…); both belong to the one project.
	Folders []string
	Paths   []string
}

// LabelRules say how a project folder's name becomes its source.
type LabelRules struct {
	Prefix string // --source; "" sends the label alone
	// Homes are the --home-prefix values (encoded, C--Users-me, -home-me).
	// Given, they are the only homes stripped from the folder names.
	Homes  []string
	Labels map[string]string // --label FOLDER=LABEL
	// Known are encoded home directories known for sure, besides the ones
	// Projects finds itself (the home above a named .claude/projects, and
	// this user's home directory).
	Known []string
}

// userHomeDir is os.UserHomeDir, replaced in tests.
var userHomeDir = os.UserHomeDir

// homeHeads match the start of an encoded home directory up to the user
// name, as Claude Code writes it: every character other than an ASCII letter
// or digit becomes "-", so the user name's own end cannot be read from a
// folder name (jean.marc, jean-marc and "jean marc" are all jean-marc).
var homeHeads = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^[a-z]--users-`),                          // C:\Users\<name>
	regexp.MustCompile(`^-home-`),                                     // /home/<name>
	regexp.MustCompile(`^-Users-`),                                    // /Users/<name> (macOS)
	regexp.MustCompile(`(?i)^--wsl(-localhost-|--)[a-z0-9-]*?-home-`), // \\wsl.localhost\<distro>\home\<name>
}

var rootHome = regexp.MustCompile(`^-root(-|$)`) // /root

// Errors of HomeLabel.
var (
	ErrNoHome        = errors.New("no home directory recognised in the folder name")
	ErrAmbiguousHome = errors.New("the user name in the folder name may be longer than it looks")
)

// HomeLabel is the label of the folder named folder: its name without the
// encoded home directory, "~" for the home directory itself.
//
// homes are the --home-prefix values; given, only they are stripped. Else a
// home in known (encoded, known for sure) is stripped; else the start of a
// home is recognised (C--Users-, -home-, -Users-, -root, a WSL home seen from
// Windows) and the user name after it taken from known when one fits, or as
// one run of letters and digits. That run is refused (ErrAmbiguousHome) when
// a known user name begins with it and goes on ("jean" of "jean-marc"): the
// rest of the label could be the rest of the user name. ErrNoHome: the name
// might hold a user name; the folder needs --home-prefix or --label.
func HomeLabel(folder string, homes, known []string) (string, error) {
	if len(homes) > 0 {
		if l, ok := stripHome(folder, homes); ok {
			return l, nil
		}
		return "", ErrNoHome
	}
	if l, ok := stripHome(folder, known); ok {
		return l, nil
	}
	users := knownUsers(known)
	for _, h := range homeHeads {
		m := h.FindString(folder)
		if m == "" {
			continue
		}
		rest := folder[len(m):]
		best := ""
		for _, u := range users {
			if len(u) > len(best) && hasHomePrefix(rest, u) {
				best = u
			}
		}
		if best != "" {
			return labelAfter(rest[len(best):]), nil
		}
		name, after, _ := strings.Cut(rest, "-")
		if name == "" {
			return "", ErrNoHome
		}
		for _, u := range users {
			if len(u) > len(name) && strings.EqualFold(u[:len(name)+1], name+"-") {
				return "", ErrAmbiguousHome
			}
		}
		if after == "" && !strings.HasSuffix(rest, "-") {
			return "~", nil
		}
		return labelAfter("-" + after), nil
	}
	if m := rootHome.FindString(folder); m != "" {
		return labelAfter(folder[len("-root"):]), nil
	}
	return "", ErrNoHome
}

// hasHomePrefix: s is the home h or lies below it (case-insensitive: the
// drive letter, and Windows names, come in either case).
func hasHomePrefix(s, h string) bool {
	n := len(h)
	return n > 0 && len(s) >= n && strings.EqualFold(s[:n], h) && (len(s) == n || s[n] == '-')
}

// stripHome strips the longest of homes from folder.
func stripHome(folder string, homes []string) (string, bool) {
	best := -1
	for _, h := range homes {
		if hasHomePrefix(folder, h) && len(h) > best {
			best = len(h)
		}
	}
	if best < 0 {
		return "", false
	}
	return labelAfter(folder[best:]), true
}

// labelAfter makes the label of what follows a home: "-x" is x, "" is ~.
func labelAfter(rest string) string {
	if rest = strings.TrimPrefix(rest, "-"); rest == "" {
		return "~"
	}
	return rest
}

// knownUsers are the user names of the known homes, dashes included.
func knownUsers(known []string) []string {
	var out []string
	for _, k := range known {
		for _, h := range homeHeads {
			if m := h.FindString(k); m != "" && len(k) > len(m) {
				out = append(out, k[len(m):])
				break
			}
		}
	}
	return out
}

var mntDrive = regexp.MustCompile(`^/mnt/([a-zA-Z])/(.+)$`)

// knownHomes are the homes known for sure: the directory above
// .claude/projects in each root, and this user's home directory, encoded.
// A Windows directory seen from WSL (/mnt/c/Users/me) is also encoded as
// Windows writes it (C--Users-me).
func knownHomes(roots []string) []string {
	var out []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		if e := strings.TrimRight(EncodePath(dir), "-"); e != "" {
			out = append(out, e)
		}
		if m := mntDrive.FindStringSubmatch(filepath.ToSlash(dir)); m != nil {
			out = append(out, strings.ToUpper(m[1])+"--"+strings.TrimRight(EncodePath(m[2]), "-"))
		}
	}
	for _, r := range roots {
		cands := []string{r}
		if a, err := filepath.Abs(r); err == nil {
			cands = append(cands, a)
			if e, err := filepath.EvalSymlinks(a); err == nil {
				cands = append(cands, e)
			}
		}
		for _, c := range cands {
			add(homeOfStore(c))
		}
	}
	if h, err := userHomeDir(); err == nil {
		add(h)
	}
	return out
}

// homeOfStore is the directory above .claude/projects in p, or "".
func homeOfStore(p string) string {
	s := strings.ReplaceAll(p, `\`, "/") + "/"
	i := strings.LastIndex(s, "/.claude/projects/")
	if i <= 0 {
		return ""
	}
	return s[:i]
}

// EncodePath encodes a directory as Claude Code names its project folder:
// every character other than an ASCII letter or digit becomes "-".
func EncodePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// HomePrefix reads one --home-prefix: a path (C:\Users\me, /home/me) is
// encoded, an encoded one (C--Users-me) taken as it is.
func HomePrefix(v string) (string, error) {
	p := strings.TrimSpace(v)
	if strings.ContainsAny(p, `/\:`) {
		p = EncodePath(p)
	}
	p = strings.TrimRight(p, "-")
	if p == "" {
		return "", fmt.Errorf("--home-prefix %q names no directory", v)
	}
	return p, nil
}

// folderKey is a folder's name with a Windows drive letter in upper case, so
// that C--x and c--x, one directory on Windows, are one project.
func folderKey(f string) string {
	if len(f) >= 3 && f[1] == '-' && f[2] == '-' && (f[0] >= 'a' && f[0] <= 'z') {
		return strings.ToUpper(f[:1]) + f[1:]
	}
	return f
}

var uuidName = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// encodedName: a directory name as Claude Code names a project folder.
var encodedName = regexp.MustCompile(`^(-|[A-Za-z]--)`)

// Projects lists the project folders below the roots and gives each its
// source. A root that holds transcripts or session folders directly is one
// project folder; any other root holds one project per top-level directory
// (a symlinked one is followed, as a named root is). A folder is skipped (no
// source, no label needed, nothing sent) only when it could be walked whole
// and holds no *.jsonl and no linked or irregular entry: only memory/ left,
// or its transcripts cleaned up. Any other folder is a source, so that a read
// problem in it withholds its claim. A root without any *.jsonl below it is
// an error, as for Read.
func Projects(roots []string, rules LabelRules) ([]Project, error) {
	type folder struct{ name, path string }
	var found []folder
	seen := map[string]bool{}
	skipped := map[string]bool{} // folder keys without transcripts
	add := func(name, path string) {
		key := path
		if r, err := filepath.EvalSymlinks(path); err == nil {
			key = r
		}
		if emptyFolder(key) {
			skipped[folderKey(name)] = true
			return
		}
		if !seen[key] {
			seen[key] = true
			found = append(found, folder{name, path})
		}
	}
	for _, root := range roots {
		r := root
		if e, err := filepath.EvalSymlinks(r); err == nil {
			r = e
		}
		if a, err := filepath.Abs(r); err == nil {
			r = a
		}
		fi, err := os.Stat(r)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a directory: --source-per-project takes a project folder or the directory holding them", root)
		}
		if uuidName.MatchString(filepath.Base(r)) {
			return nil, fmt.Errorf("%s is one session's folder: name its project folder or the directory holding them", root)
		}
		ents, err := os.ReadDir(r)
		if err != nil {
			return nil, err
		}
		if !hasTranscript(r) {
			return nil, fmt.Errorf("no *.jsonl transcripts under %s", root)
		}
		if stray := sessionEntries(ents); len(stray) > 0 {
			// A project folder named itself; but transcripts or session
			// folders at the top of the store, beside project folders, are
			// strays, not a project called "projects".
			store := homeOfStore(r+string(filepath.Separator)+"x") != "" && filepath.Base(r) == "projects"
			projects := false
			for _, e := range ents {
				if e.IsDir() && !uuidName.MatchString(e.Name()) && encodedName.MatchString(e.Name()) {
					projects = true
				}
			}
			if store || projects {
				return nil, fmt.Errorf("%s holds %s beside its project folders: a transcript or session folder belongs in a project folder; move it there or away", root, strings.Join(stray, ", "))
			}
			add(filepath.Base(r), r)
			continue
		}
		for _, e := range ents {
			p := filepath.Join(r, e.Name())
			if !e.IsDir() {
				// a symlink or junction to a directory is followed
				if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
					continue
				}
			}
			add(e.Name(), p)
		}
	}

	groups := map[string]*Project{}
	var keys []string
	for _, f := range found {
		k := folderKey(f.name)
		g := groups[k]
		if g == nil {
			g = &Project{Key: k}
			groups[k] = g
			keys = append(keys, k)
		}
		g.Folders = append(g.Folders, f.name)
		g.Paths = append(g.Paths, f.path)
	}
	sort.Strings(keys)

	labels := map[string]string{}
	for f, l := range rules.Labels {
		k := folderKey(f)
		if groups[k] == nil {
			if skipped[k] {
				continue // no transcripts, nothing to label
			}
			return nil, fmt.Errorf("--label %s: no such project folder under the named directories", f)
		}
		if prev, ok := labels[k]; ok && prev != l {
			return nil, fmt.Errorf("--label %s: two labels for one project folder", f)
		}
		labels[k] = l
	}

	known := append(knownHomes(roots), rules.Known...)
	var out []Project
	var unknown, ambiguous []string
	bySource := map[string]string{}
	for _, k := range keys {
		label, ok := labels[k]
		if !ok {
			l, err := HomeLabel(k, rules.Homes, known)
			switch {
			case errors.Is(err, ErrAmbiguousHome):
				ambiguous = append(ambiguous, k)
				continue
			case err != nil:
				unknown = append(unknown, k)
				continue
			}
			label = l
		}
		src := strings.TrimSpace(label)
		if rules.Prefix != "" {
			src = rules.Prefix + ": " + src
		}
		if !ValidSource(strings.TrimSpace(label)) || !ValidSource(src) {
			return nil, fmt.Errorf("project folder %s: the source %q is not 1-%d characters of UTF-8 without NUL; give it a --label", k, src, MaxSource)
		}
		if other, dup := bySource[src]; dup {
			return nil, fmt.Errorf("project folders %s and %s both get the source %q; give one a --label", other, k, src)
		}
		bySource[src] = k
		g := groups[k]
		g.Source = src
		out = append(out, *g)
	}
	var errs []string
	if len(ambiguous) > 0 {
		errs = append(errs, fmt.Sprintf("in these project folder names the user name may go on past where it seems to end, so their labels might hold part of it: %s", strings.Join(ambiguous, ", ")))
	}
	if len(unknown) > 0 {
		errs = append(errs, fmt.Sprintf("the home directory in these project folder names is not known, so their labels might hold a user name: %s", strings.Join(unknown, ", ")))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s; give --home-prefix or --label FOLDER=LABEL", strings.Join(errs, "; "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// sessionEntries are the transcripts and session folders directly in a
// folder: what makes it a project folder.
func sessionEntries(ents []fs.DirEntry) []string {
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") && !e.IsDir() || e.IsDir() && uuidName.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// hasTranscript says whether any *.jsonl lies below root (symlinks below it
// are not followed, as in Read).
func hasTranscript(root string) bool {
	found := false
	walkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// emptyFolder says whether a project folder holds nothing to read: walked
// whole without an error, it has no *.jsonl and no linked or irregular entry
// (which read would not follow, so calls could be behind it).
func emptyFolder(dir string) bool {
	var st Stats
	var files []string
	failed := false
	walkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			failed = true
			return fs.SkipAll
		}
		visit(p, d, &st, &files)
		if len(files) > 0 || len(st.Symlinks)+len(st.Unread)+len(st.Other) > 0 {
			return fs.SkipAll
		}
		return nil
	})
	return !failed && len(files) == 0 && len(st.Symlinks)+len(st.Unread)+len(st.Other) == 0
}

// ProjectRead is what one project folder gave.
type ProjectRead struct {
	Project
	Rows  []Row
	Stats Stats
	// Elsewhere counts calls of this folder also found in another project
	// folder: each call is sent once, with one folder (see ReadProjects).
	Elsewhere int
}

// ReadProjects reads each project as Read reads its folders, except that a
// project without any *.jsonl gives no rows rather than an error, and a
// directory or file that cannot be read withholds that project's claim
// rather than failing the push.
//
// A message id found in more than one project (a session resumed or forked
// into another folder) is kept in one only: the intake keeps one row per
// source and message id, so it would count twice. The copy with the most
// output wins; on equal output the earliest timestamp (the original, not a
// later copy), then the folder's name, never the label: relabelling moves no
// call. When the winning folder's copy is later cleaned up, the other folder
// sends the call under its own source, and covey holds it under both.
func ReadProjects(ps []Project, host string, since time.Time) ([]ProjectRead, error) {
	out := make([]ProjectRead, len(ps))
	type at struct{ p, i int }
	seen := map[string]at{}
	drop := map[at]bool{}
	for pi, p := range ps {
		rows, st, err := read(p.Paths, host, since, true)
		if err != nil {
			return nil, err
		}
		out[pi] = ProjectRead{Project: p, Rows: rows, Stats: st}
		for i, r := range rows {
			prev, ok := seen[r.MessageID]
			if !ok {
				seen[r.MessageID] = at{pi, i}
				continue
			}
			q := out[prev.p].Rows[prev.i]
			better := r.Output > q.Output ||
				r.Output == q.Output && (r.TS.Before(q.TS) || r.TS.Equal(q.TS) && p.Key < ps[prev.p].Key)
			if better {
				drop[prev] = true
				out[prev.p].Elsewhere++
				seen[r.MessageID] = at{pi, i}
			} else {
				drop[at{pi, i}] = true
				out[pi].Elsewhere++
			}
		}
	}
	if len(drop) == 0 {
		return out, nil
	}
	for pi := range out {
		kept := make([]Row, 0, len(out[pi].Rows))
		for i, r := range out[pi].Rows {
			if !drop[at{pi, i}] {
				kept = append(kept, r)
			}
		}
		out[pi].Rows = kept
	}
	return out, nil
}
