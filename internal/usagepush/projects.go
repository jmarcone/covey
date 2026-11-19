package usagepush

import (
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
	// Folders are the folder's names: two when Claude Code wrote one Windows
	// directory once with an upper and once with a lower drive letter
	// (C--Users-… and c--Users-…); both belong to the one project.
	Folders []string
	Paths   []string
}

// LabelRules say how a project folder's name becomes its source.
type LabelRules struct {
	Prefix string // --source; "" sends the label alone
	// Homes are encoded home directories (C--Users-me, -home-me) stripped
	// from the folder names; none derives the home from each folder name.
	Homes  []string
	Labels map[string]string // --label FOLDER=LABEL
}

// homePatterns match the encoded home directory at the start of a folder
// name, as Claude Code writes it (every character other than a letter or a
// digit becomes "-"). The user name is one run of letters and digits: a name
// with another character in it (jean.marc) needs --home-prefix.
var homePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^[a-z]--users-[a-z0-9]+(-|$)`),                          // C:\Users\me
	regexp.MustCompile(`^-home-[A-Za-z0-9]+(-|$)`),                                  // /home/me
	regexp.MustCompile(`^-Users-[A-Za-z0-9]+(-|$)`),                                 // /Users/me (macOS)
	regexp.MustCompile(`^-root(-|$)`),                                               // /root
	regexp.MustCompile(`(?i)^--wsl(-localhost-|--)[a-z0-9-]*?-home-[a-z0-9]+(-|$)`), // \\wsl.localhost\<distro>\home\me
}

// HomeLabel is the label of the folder named folder: its name without the
// encoded home directory, "~" for the home directory itself. homes are the
// --home-prefix values (encoded); without any, the home is read from the
// folder name. ok is false when no home is found: the name might then hold a
// user name, and the folder needs --home-prefix or --label.
func HomeLabel(folder string, homes []string) (label string, ok bool) {
	if len(homes) == 0 {
		for _, re := range homePatterns {
			if m := re.FindString(folder); m != "" {
				label, ok = folder[len(m):], true
				break
			}
		}
	} else {
		best := -1
		for _, h := range homes {
			n := len(h)
			if len(folder) >= n && strings.EqualFold(folder[:n], h) && (len(folder) == n || folder[n] == '-') && n > best {
				best = n
			}
		}
		if best >= 0 {
			label, ok = strings.TrimPrefix(folder[best:], "-"), true
		}
	}
	if ok && label == "" {
		label = "~"
	}
	return label, ok
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

// Projects lists the project folders below the roots and gives each its
// source. A root that holds transcripts or session folders directly is one
// project folder; any other root holds one project per top-level directory
// (a symlinked one is followed, as a named root is). A folder without any
// *.jsonl below it (only memory/ left, or its transcripts cleaned up) is no
// source: it is skipped, needs no label and sends nothing. A root without
// any *.jsonl below it is an error, as for Read.
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
		if !hasTranscript(key) {
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
		if isProjectFolder(ents) {
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
			g = &Project{}
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

	var out []Project
	var unknown []string
	bySource := map[string]string{}
	for _, k := range keys {
		label, ok := labels[k]
		if !ok {
			if label, ok = HomeLabel(k, rules.Homes); !ok {
				unknown = append(unknown, k)
				continue
			}
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
	if len(unknown) > 0 {
		return nil, fmt.Errorf("the home directory in these project folder names is not known, so their labels might hold a user name: %s; give --home-prefix or --label FOLDER=LABEL", strings.Join(unknown, ", "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// isProjectFolder: the folder holds transcripts or session folders directly.
func isProjectFolder(ents []fs.DirEntry) bool {
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") && !e.IsDir() {
			return true
		}
		if e.IsDir() && uuidName.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

// hasTranscript says whether any *.jsonl lies below root (symlinks below it
// are not followed, as in Read).
func hasTranscript(root string) bool {
	found := false
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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

// ProjectRead is what one project folder gave.
type ProjectRead struct {
	Project
	Rows  []Row
	Stats Stats
	// Elsewhere counts calls of this folder also found in another project
	// folder: each call is sent once, with the folder holding its better copy
	// (Read's rule; on a full tie the later source).
	Elsewhere int
}

// ReadProjects reads each project as Read reads its folders, except that a
// project without any *.jsonl (its transcripts cleaned up) gives no rows
// rather than an error. A message id found in more than one project is kept
// in one only: the intake keeps one row per source and message id, so it
// would count twice.
func ReadProjects(ps []Project, host string, since time.Time) ([]ProjectRead, error) {
	out := make([]ProjectRead, len(ps))
	type at struct{ p, i int }
	seen := map[string]at{}
	drop := map[at]bool{}
	for pi, p := range ps {
		rows, st, err := read(p.Paths, host, since, false)
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
				r.Output == q.Output && (r.TS.After(q.TS) || r.TS.Equal(q.TS) && p.Source > ps[prev.p].Source)
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
