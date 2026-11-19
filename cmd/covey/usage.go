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
//
// The reported_through watermark says that every call of this host for this
// source up to a time is in the push; covey books a meter reading's unsent
// remainder to itself once all hosts claim past it. So the watermark is sent
// only with --complete (the directories hold every transcript of this host),
// and never when --since, a symlinked directory, an unreadable line or a
// held-back call means the push may lack calls.
//
// The API key comes from COVEY_API_KEY or --key-file, never from argv.
// usageStderr gets the warnings; a variable so tests can read it.
var usageStderr io.Writer = os.Stderr

func runUsage(args []string, stdout io.Writer, getenv func(string) string) error {
	if len(args) == 0 || args[0] != "push" {
		return errors.New("usage: covey usage push DIR... --source LABEL [--url URL] [--host NAME] [--meter-secret KEY[:SLOT]] [--since RFC3339] [--complete] [--key-file PATH] [--dry-run] [--json]")
	}
	fs := flag.NewFlagSet("covey usage push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "label for these sessions, e.g. \"operator: home\" (1-64 characters)")
	url := fs.String("url", "", "covey install, e.g. http://localhost:8494 (default $COVEY_URL)")
	host := fs.String("host", "", "host name stored on every row and claimed in reported_through (default: this machine's; WSL2 reports the Windows name, so give each transcript store its own, e.g. home-win and home-wsl)")
	meter := fs.String("meter-secret", "", "subscription credential sharing the meter, KEY or KEY:SLOT")
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
	if s := strings.TrimSpace(*source); s == "" || len([]rune(s)) > 64 {
		return errors.New("covey usage push: --source is required, 1-64 characters")
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

	rows, st, err := usagepush.Read(dirs, h, sinceT)
	if err != nil {
		return fmt.Errorf("covey usage push: %w", err)
	}
	for _, p := range st.Symlinks {
		fmt.Fprintf(usageStderr, "warning: %s is a symlinked directory; it is not followed, so calls below it are not read\n", p)
	}

	for _, p := range st.Unread {
		fmt.Fprintf(usageStderr, "warning: %s is not a regular file and cannot be read\n", p)
	}
	for _, p := range st.Other {
		fmt.Fprintf(usageStderr, "warning: %s is neither a regular file nor a directory; it is left out\n", p)
	}

	// The watermark claims that every call of this host up to its time is in
	// the push. It is sent only when nothing can be missing.
	var why []string
	if !*complete {
		why = append(why, "--complete is not given, so the directories are not claimed to hold every transcript of this host; the meter split waits until a --complete push")
	}
	if *since != "" {
		why = append(why, "--since leaves older calls out")
	}
	if len(st.Symlinks) > 0 {
		why = append(why, fmt.Sprintf("%d symlinked directories were not followed", len(st.Symlinks)))
	}
	if len(st.Unread) > 0 {
		why = append(why, fmt.Sprintf("%d transcript files could not be read", len(st.Unread)))
	}
	if st.Suspect > 0 {
		why = append(why, fmt.Sprintf("%d lines with a usage could not be read (not just a last line still being written)", st.Suspect))
	}
	if st.Invalid > 0 {
		why = append(why, fmt.Sprintf("%d calls are held back, out of the intake's bounds", st.Invalid))
	}
	var through map[string]time.Time
	if len(why) == 0 {
		through = map[string]time.Time{h: time.Now().UTC().Add(-usagepush.WatermarkMargin).Truncate(time.Second)}
	}
	for _, w := range why {
		fmt.Fprintf(stdout, "no reported_through sent: %s\n", w)
	}
	listed := func(label string, items []string, total int) {
		for _, it := range items {
			fmt.Fprintf(stdout, "%s: %s\n", label, it)
		}
		if total > len(items) {
			fmt.Fprintf(stdout, "%s: and %d more\n", label, total-len(items))
		}
	}
	listed("unreadable line", st.SuspectAt, st.Suspect)
	listed("held back", st.InvalidWhy, st.Invalid)
	// The intake refuses a ts later than now + 5 min. Calls stamped ahead of
	// this clock wait for a later run rather than fail the whole batch.
	held := 0
	if limit := time.Now().Add(4 * time.Minute); len(rows) > 0 {
		kept := rows[:0]
		for _, r := range rows {
			if r.TS.After(limit) {
				held++
				continue
			}
			kept = append(kept, r)
		}
		rows = kept
	}
	if held > 0 {
		fmt.Fprintf(stdout, "%d calls are stamped more than 4 min ahead of this clock; they wait for a later run\n", held)
	}
	if *dry {
		sum := usagepush.Summarize(rows)
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				usagepush.Summary
				Files     int        `json:"files"`
				Copies    int        `json:"streamed_copies_dropped"`
				Synthetic int        `json:"synthetic_left_out"`
				Skipped   int        `json:"lines_skipped"`
				Held      int        `json:"calls_held_back"`
				Through   *time.Time `json:"reported_through,omitempty"`
			}{sum, st.Files, st.Copies, st.Synthetic, st.Skipped, st.Invalid, throughOf(through, h)})
		}
		fmt.Fprintf(stdout, "dry run, nothing sent: %d calls from %d files (%d streamed copies dropped, %d placeholders left out, %d lines skipped)\n",
			len(rows), st.Files, st.Copies, st.Synthetic, st.Skipped)
		if len(rows) > 0 {
			fmt.Fprintf(stdout, "%s .. %s\n", sum.First.Format(time.RFC3339), sum.Last.Format(time.RFC3339))
		}
		if through != nil {
			fmt.Fprintf(stdout, "reported_through %s: %s\n", h, through[h].Format(time.RFC3339))
		}
		printTotals(stdout, "all", &sum.All)
		for _, group := range []map[string]*usagepush.Totals{sum.ByOrigin, sum.ByModel} {
			for _, k := range sortedKeys(group) {
				printTotals(stdout, k, group[k])
			}
		}
		return nil
	}

	batches, err := usagepush.Batches(strings.TrimSpace(*source), m, rows, through)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "read %d calls from %d files (%d streamed copies dropped, %d placeholders left out, %d lines skipped)\n",
		len(rows), st.Files, st.Copies, st.Synthetic, st.Skipped)
	c := usagepush.Client{BaseURL: base, Key: key}
	var tot usagepush.Result
	for i, b := range batches {
		r, err := c.Post(context.Background(), b)
		if err != nil {
			if i > 0 {
				fmt.Fprintf(stdout, "accepted before the failure: %d calls in %d requests: %d new, %d updated, %d unchanged\n",
					tot.Received, i, tot.Inserted, tot.Updated, tot.Unchanged)
			}
			return fmt.Errorf("batch %d of %d: %w", i+1, len(batches), err)
		}
		tot.Received += r.Received
		tot.Inserted += r.Inserted
		tot.Updated += r.Updated
		tot.Unchanged += r.Unchanged
	}
	fmt.Fprintf(stdout, "sent %d calls in %d requests: %d new, %d updated, %d unchanged\n",
		tot.Received, len(batches), tot.Inserted, tot.Updated, tot.Unchanged)
	return nil
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
