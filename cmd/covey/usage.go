package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	neturl "net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"covey/internal/usagepush"
)

// covey usage push: send the token counts of Claude Code sessions that ran
// outside covey to a covey install (#120, part B). It runs where the
// transcripts are and needs neither database nor master key.
//
//	covey usage push DIR... --source LABEL [--url URL] [--host NAME]
//	    [--meter-secret KEY[:SLOT]] [--since RFC3339] [--complete]
//	    [--key-file PATH] [--dry-run] [--json]
//	covey usage push DIR... --source-per-project (--meter-secret KEY[:SLOT] | --no-meter)
//	    [--source PREFIX] [--home-prefix HOME]... [--label FOLDER=LABEL]...
//	    [the flags above]
//
// The reported_through watermark says that every call of this host for this
// source up to a time is in the push; covey books a meter reading's unsent
// remainder to itself once all hosts claim past it. So the watermark is sent
// only with --complete (the directories hold every transcript of this host),
// and never when --since, a symlinked directory, an unreadable line or a
// held-back call means the push may lack calls.
//
// With --source-per-project each Claude Code project folder is a source of
// its own, labelled by its folder name without the home directory, and the
// watermark is claimed per source: a source is one whole folder, so with
// --complete it is complete by construction, and a problem in one folder
// withholds that folder's claim only. No path is ever sent, only the label.
//
// The API key comes from COVEY_API_KEY or --key-file, never from argv.
// usageStderr gets the warnings; a variable so tests can read it.
var usageStderr io.Writer = os.Stderr

// multiFlag is a flag that may be given several times.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// pushSource is what one source sends.
type pushSource struct {
	name      string
	rows      []usagepush.Row
	st        usagepush.Stats
	elsewhere int
	through   map[string]time.Time
}

func runUsage(args []string, stdout io.Writer, getenv func(string) string) error {
	if len(args) == 0 || args[0] != "push" {
		return errors.New("usage: covey usage push DIR... (--source LABEL | --source-per-project (--meter-secret KEY[:SLOT] | --no-meter) [--source PREFIX] [--home-prefix HOME]... [--label FOLDER=LABEL]...) [--url URL] [--host NAME] [--meter-secret KEY[:SLOT]] [--since RFC3339] [--complete] [--key-file PATH] [--dry-run] [--json]")
	}
	fs := flag.NewFlagSet("covey usage push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "label for these sessions, e.g. \"operator: home\" (1-64 characters); with --source-per-project an optional prefix, \"PREFIX: <label>\"")
	perProject := fs.Bool("source-per-project", false, "each Claude Code project folder (a top-level directory under DIR, or DIR itself when it is one) is a source of its own, labelled by its folder name without the home directory")
	var homes, labelArgs multiFlag
	fs.Var(&homes, "home-prefix", "with --source-per-project: the home directory to strip from the folder names, as a path (C:\\Users\\me, /home/me) or encoded (C--Users-me); repeatable; default: read from each folder name")
	fs.Var(&labelArgs, "label", "with --source-per-project: FOLDER=LABEL labels one project folder; repeatable")
	url := fs.String("url", "", "covey install, e.g. http://localhost:8494 (default $COVEY_URL)")
	host := fs.String("host", "", "host name stored on every row and claimed in reported_through (default: this machine's; WSL2 reports the Windows name, so give each transcript store its own, e.g. home-win and home-wsl)")
	meter := fs.String("meter-secret", "", "subscription credential sharing the meter, KEY or KEY:SLOT")
	noMeter := fs.Bool("no-meter", false, "with --source-per-project: the sessions share no subscription meter (a per-use account); --source-per-project needs this or --meter-secret")
	since := fs.String("since", "", "only calls at or after this time (RFC 3339); no reported_through is sent")
	complete := fs.Bool("complete", false, "the directories hold every Claude Code transcript of this host for this source; only then is reported_through sent")
	keyFile := fs.String("key-file", "", "file holding the API key (must not be group- or world-readable)")
	dry := fs.Bool("dry-run", false, "print what would be sent, send nothing")
	asJSON := fs.Bool("json", false, "with --dry-run: print the totals as JSON")

	// Flags may come before or after the directories.
	var dirs []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		dirs = append(dirs, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(dirs) == 0 {
		return errors.New("covey usage push: name at least one transcripts directory")
	}
	src := strings.TrimSpace(*source)
	var rules usagepush.LabelRules
	if *perProject {
		if src != "" && !usagepush.ValidSource(src) {
			return errors.New("covey usage push: --source must be 1-64 characters of UTF-8 without NUL")
		}
		rules.Prefix = src
		// covey links a source to its meter per source, from a push that
		// names it: a new project pushed without --meter-secret would stay
		// unlinked, and its use would be booked to covey's agents.
		if (*meter == "") == !*noMeter {
			return errors.New("covey usage push: --source-per-project needs --meter-secret KEY[:SLOT] (the subscription the sessions share), or --no-meter for a per-use account; each new project is a new source, linked to the meter only by a push that names it")
		}
		for _, v := range homes {
			p, err := usagepush.HomePrefix(v)
			if err != nil {
				return fmt.Errorf("covey usage push: %w", err)
			}
			rules.Homes = append(rules.Homes, p)
		}
		rules.Labels = map[string]string{}
		for _, v := range labelArgs {
			f, l, ok := strings.Cut(v, "=")
			f, l = strings.TrimSpace(f), strings.TrimSpace(l)
			if !ok || f == "" || l == "" {
				return fmt.Errorf("covey usage push: --label %q: want FOLDER=LABEL", v)
			}
			if _, dup := rules.Labels[f]; dup {
				return fmt.Errorf("covey usage push: --label %s given twice", f)
			}
			rules.Labels[f] = l
		}
	} else {
		if !usagepush.ValidSource(src) {
			return errors.New("covey usage push: --source is required, 1-64 characters")
		}
		if len(homes) > 0 || len(labelArgs) > 0 || *noMeter {
			return errors.New("covey usage push: --home-prefix, --label and --no-meter need --source-per-project")
		}
	}
	var sinceT time.Time
	if *since != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return fmt.Errorf("covey usage push: --since: %w", err)
		}
		sinceT = t
	}
	var m *usagepush.Meter
	if *meter != "" {
		key, slot, _ := strings.Cut(*meter, ":")
		m = &usagepush.Meter{SecretKey: key}
		if slot != "" {
			n, err := strconv.Atoi(slot)
			if err != nil || n < 0 {
				return fmt.Errorf("covey usage push: --meter-secret: slot %q is not a number", slot)
			}
			m.SecretSlot = n
		}
	}
	h := strings.TrimSpace(*host)
	if h == "" {
		h, _ = os.Hostname()
	}
	if h == "" || !usagepush.ValidText(h) {
		return errors.New("covey usage push: --host is required (1-256 bytes of UTF-8, no NUL) when the host name is unknown")
	}

	// A real push checks its URL and key before reading anything.
	var base, key string
	if !*dry {
		base = *url
		if base == "" {
			base = getenv("COVEY_URL")
		}
		if base == "" {
			return errors.New("covey usage push: --url or COVEY_URL is required")
		}
		warnPlainHTTP(usageStderr, base)
		k, err := apiKey(*keyFile, getenv)
		if err != nil {
			return err
		}
		key = k
	}

	var srcs []*pushSource
	if *perProject {
		ps, err := usagepush.Projects(dirs, rules)
		if err != nil {
			return fmt.Errorf("covey usage push: %w", err)
		}
		reads, err := usagepush.ReadProjects(ps, h, sinceT)
		if err != nil {
			return fmt.Errorf("covey usage push: %w", err)
		}
		for _, r := range reads {
			srcs = append(srcs, &pushSource{name: r.Source, rows: r.Rows, st: r.Stats, elsewhere: r.Elsewhere})
		}
	} else {
		rows, st, err := usagepush.Read(dirs, h, sinceT)
		if err != nil {
			return fmt.Errorf("covey usage push: %w", err)
		}
		srcs = []*pushSource{{name: src, rows: rows, st: st}}
	}

	// The watermark claims that every call of this host for a source up to
	// its time is in the push. It is sent only when nothing can be missing:
	// --complete and --since decide for every source, what a read found for
	// its own source only.
	var why []string
	if !*complete {
		why = append(why, "--complete is not given, so the directories are not claimed to hold every transcript of this host; the meter split waits until a --complete push")
	}
	if *since != "" {
		why = append(why, "--since leaves older calls out")
	}
	// With --dry-run --json, stdout holds the JSON only; the notes go to stderr.
	info := stdout
	if *dry && *asJSON {
		info = usageStderr
	}
	for _, w := range why {
		fmt.Fprintf(info, "no reported_through sent: %s\n", w)
	}
	claimAt := time.Now().UTC().Add(-usagepush.WatermarkMargin).Truncate(time.Second)
	listed := func(label string, items []string, total int) {
		for _, it := range items {
			fmt.Fprintf(info, "%s: %s\n", label, it)
		}
		if total > len(items) {
			fmt.Fprintf(info, "%s: and %d more\n", label, total-len(items))
		}
	}
	// The intake refuses a ts later than now + 5 min. Calls stamped ahead of
	// this clock wait for a later run rather than fail the whole batch.
	held, elsewhere := 0, 0
	limit := time.Now().Add(4 * time.Minute)
	var all []usagepush.Row
	var st usagepush.Stats
	for _, s := range srcs {
		for _, p := range s.st.Symlinks {
			fmt.Fprintf(usageStderr, "warning: %s is a symlinked directory; it is not followed, so calls below it are not read\n", p)
		}
		for _, p := range s.st.Unread {
			fmt.Fprintf(usageStderr, "warning: %s cannot be read\n", p)
		}
		for _, p := range s.st.Other {
			fmt.Fprintf(usageStderr, "warning: %s is neither a regular file nor a directory; it is left out\n", p)
		}
		own := sourceProblems(s.st)
		for _, w := range own {
			if *perProject {
				fmt.Fprintf(info, "no reported_through sent for %q: %s\n", s.name, w)
			} else {
				fmt.Fprintf(info, "no reported_through sent: %s\n", w)
			}
		}
		if len(why) == 0 && len(own) == 0 {
			s.through = map[string]time.Time{h: claimAt}
		}
		listed("unreadable line", s.st.SuspectAt, s.st.Suspect)
		listed("held back", s.st.InvalidWhy, s.st.Invalid)
		kept := s.rows[:0]
		for _, r := range s.rows {
			if r.TS.After(limit) {
				held++
				continue
			}
			kept = append(kept, r)
		}
		s.rows = kept
		all = append(all, s.rows...)
		elsewhere += s.elsewhere
		st.Files += s.st.Files
		st.Copies += s.st.Copies
		st.Synthetic += s.st.Synthetic
		st.Skipped += s.st.Skipped
		st.Invalid += s.st.Invalid
	}
	if held > 0 {
		fmt.Fprintf(info, "%d calls are stamped more than 4 min ahead of this clock; they wait for a later run\n", held)
	}
	if elsewhere > 0 {
		fmt.Fprintf(info, "%d calls are in more than one project folder; each is sent once\n", elsewhere)
	}
	if *dry {
		sum := usagepush.Summarize(all)
		if *asJSON {
			type sourceJSON struct {
				Source  string           `json:"source"`
				Totals  usagepush.Totals `json:"totals"`
				Through *time.Time       `json:"reported_through,omitempty"`
			}
			out := struct {
				usagepush.Summary
				Files     int          `json:"files"`
				Copies    int          `json:"streamed_copies_dropped"`
				Synthetic int          `json:"synthetic_left_out"`
				Skipped   int          `json:"lines_skipped"`
				Held      int          `json:"calls_held_back"`
				Through   *time.Time   `json:"reported_through,omitempty"`
				Sources   []sourceJSON `json:"sources,omitempty"`
			}{Summary: sum, Files: st.Files, Copies: st.Copies, Synthetic: st.Synthetic, Skipped: st.Skipped, Held: st.Invalid}
			if *perProject {
				for _, s := range srcs {
					out.Sources = append(out.Sources, sourceJSON{s.name, usagepush.Summarize(s.rows).All, throughOf(s.through, h)})
				}
			} else {
				out.Through = throughOf(srcs[0].through, h)
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		fmt.Fprintf(stdout, "dry run, nothing sent: %d calls from %d files (%d streamed copies dropped, %d placeholders left out, %d lines skipped)\n",
			len(all), st.Files, st.Copies, st.Synthetic, st.Skipped)
		if len(all) > 0 {
			fmt.Fprintf(stdout, "%s .. %s\n", sum.First.Format(time.RFC3339), sum.Last.Format(time.RFC3339))
		}
		if !*perProject && srcs[0].through != nil {
			fmt.Fprintf(stdout, "reported_through %s: %s\n", h, claimAt.Format(time.RFC3339))
		}
		printTotals(stdout, "all", &sum.All)
		for _, group := range []map[string]*usagepush.Totals{sum.ByOrigin, sum.ByModel} {
			for _, k := range sortedKeys(group) {
				printTotals(stdout, k, group[k])
			}
		}
		if *perProject {
			fmt.Fprintf(stdout, "%d sources:\n", len(srcs))
			for _, s := range srcs {
				t := usagepush.Summarize(s.rows).All
				claim := "no reported_through"
				if s.through != nil {
					claim = "reported_through " + h + ": " + claimAt.Format(time.RFC3339)
				}
				fmt.Fprintf(stdout, "  source %-40q calls %6d  input %12d  output %12d  cache_read %14d  cache_write_5m %12d  cache_write_1h %12d  %s\n",
					s.name, t.Rows, t.Input, t.Output, t.CacheRead, t.CacheWrite5m, t.CacheWrite1h, claim)
			}
		}
		return nil
	}

	type sourceBatches struct {
		s       *pushSource
		batches [][]byte
	}
	var plan []sourceBatches
	nBatches := 0
	for _, s := range srcs {
		b, err := usagepush.Batches(s.name, m, s.rows, s.through)
		if err != nil {
			return err
		}
		plan = append(plan, sourceBatches{s, b})
		nBatches += len(b)
	}
	fmt.Fprintf(stdout, "read %d calls from %d files (%d streamed copies dropped, %d placeholders left out, %d lines skipped)\n",
		len(all), st.Files, st.Copies, st.Synthetic, st.Skipped)
	c := usagepush.Client{BaseURL: base, Key: key}
	var tot usagepush.Result
	done := 0
	for _, p := range plan {
		var sub usagepush.Result
		for i, b := range p.batches {
			r, err := c.Post(context.Background(), b)
			if err != nil {
				if done > 0 {
					fmt.Fprintf(stdout, "accepted before the failure: %d calls in %d requests: %d new, %d updated, %d unchanged\n",
						tot.Received+sub.Received, done, tot.Inserted+sub.Inserted, tot.Updated+sub.Updated, tot.Unchanged+sub.Unchanged)
				}
				if *perProject {
					return fmt.Errorf("source %q: batch %d of %d: %w", p.s.name, i+1, len(p.batches), err)
				}
				return fmt.Errorf("batch %d of %d: %w", i+1, len(p.batches), err)
			}
			done++
			sub.Received += r.Received
			sub.Inserted += r.Inserted
			sub.Updated += r.Updated
			sub.Unchanged += r.Unchanged
		}
		if *perProject && len(p.batches) > 0 {
			claim := ""
			if p.s.through != nil {
				claim = ", reported_through " + claimAt.Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "  %q: %d calls in %d requests: %d new, %d updated, %d unchanged%s\n",
				p.s.name, sub.Received, len(p.batches), sub.Inserted, sub.Updated, sub.Unchanged, claim)
		}
		tot.Received += sub.Received
		tot.Inserted += sub.Inserted
		tot.Updated += sub.Updated
		tot.Unchanged += sub.Unchanged
	}
	fmt.Fprintf(stdout, "sent %d calls in %d requests: %d new, %d updated, %d unchanged\n",
		tot.Received, nBatches, tot.Inserted, tot.Updated, tot.Unchanged)
	return nil
}

// sourceProblems says why a read may lack calls of its source.
func sourceProblems(st usagepush.Stats) []string {
	var why []string
	if len(st.Symlinks) > 0 {
		why = append(why, fmt.Sprintf("%d symlinked directories were not followed", len(st.Symlinks)))
	}
	if len(st.Unread) > 0 {
		why = append(why, fmt.Sprintf("%d transcript files or directories could not be read", len(st.Unread)))
	}
	if st.Suspect > 0 {
		why = append(why, fmt.Sprintf("%d lines with a usage could not be read (not just a last line still being written)", st.Suspect))
	}
	if st.Invalid > 0 {
		why = append(why, fmt.Sprintf("%d calls are held back, out of the intake's bounds", st.Invalid))
	}
	return why
}

func throughOf(m map[string]time.Time, h string) *time.Time {
	if t, ok := m[h]; ok {
		return &t
	}
	return nil
}

// warnPlainHTTP warns when the key and the counts would cross a network
// unencrypted: an http:// URL to a host that is not this machine.
func warnPlainHTTP(w io.Writer, raw string) {
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return
	}
	fmt.Fprintf(w, "warning: %s is plain http to a host that is not this machine; the API key and the counts travel unencrypted, use https\n", u.Redacted())
}

// apiKey reads the key from --key-file or COVEY_API_KEY. A key file others can
// read is refused on systems with Unix permissions; on Windows the file's ACL
// is not checked.
func apiKey(path string, getenv func(string) string) (string, error) {
	if path == "" {
		k := strings.TrimSpace(getenv("COVEY_API_KEY"))
		if k == "" {
			return "", errors.New("covey usage push: set COVEY_API_KEY or --key-file")
		}
		return k, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("covey usage push: --key-file: %w", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("covey usage push: --key-file %s is readable by others (mode %o); chmod 600 it", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("covey usage push: --key-file: %w", err)
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", fmt.Errorf("covey usage push: --key-file %s is empty", path)
	}
	return k, nil
}

func printTotals(w io.Writer, name string, t *usagepush.Totals) {
	fmt.Fprintf(w, "  %-28s calls %6d  input %12d  output %12d  cache_read %14d  cache_write_5m %12d  cache_write_1h %12d\n",
		name, t.Rows, t.Input, t.Output, t.CacheRead, t.CacheWrite5m, t.CacheWrite1h)
}

func sortedKeys(m map[string]*usagepush.Totals) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
