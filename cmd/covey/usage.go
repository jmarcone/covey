package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
//	    [--meter-secret KEY[:SLOT]] [--since RFC3339] [--key-file PATH]
//	    [--dry-run] [--json]
//
// The API key comes from COVEY_API_KEY or --key-file, never from argv.
func runUsage(args []string, stdout io.Writer, getenv func(string) string) error {
	if len(args) == 0 || args[0] != "push" {
		return errors.New("usage: covey usage push DIR... --source LABEL [--url URL] [--host NAME] [--meter-secret KEY[:SLOT]] [--since RFC3339] [--key-file PATH] [--dry-run] [--json]")
	}
	fs := flag.NewFlagSet("covey usage push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "label for these sessions, e.g. \"operator: home\" (1-64 characters)")
	url := fs.String("url", "", "covey install, e.g. http://localhost:8494 (default $COVEY_URL)")
	host := fs.String("host", "", "host name stored on every row (default: this machine's)")
	meter := fs.String("meter-secret", "", "subscription credential sharing the meter, KEY or KEY:SLOT")
	since := fs.String("since", "", "only calls at or after this time (RFC 3339)")
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
	if h == "" || len(h) > 256 {
		return errors.New("covey usage push: --host is required (1-256 bytes) when the host name is unknown")
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
		k, err := apiKey(*keyFile, getenv)
		if err != nil {
			return err
		}
		key = k
	}

	// The watermark: every call of this host up to here is in the files read
	// now. Five minutes are left as a margin for a reply still being written.
	through := map[string]time.Time{h: time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)}
	rows, st, err := usagepush.Read(dirs, h, sinceT)
	if err != nil {
		return err
	}
	if st.Files == 0 {
		return fmt.Errorf("covey usage push: no *.jsonl transcripts under %s", strings.Join(dirs, ", "))
	}
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
				Files     int `json:"files"`
				Copies    int `json:"streamed_copies_dropped"`
				Synthetic int `json:"synthetic_left_out"`
				Skipped   int `json:"lines_skipped"`
			}{sum, st.Files, st.Copies, st.Synthetic, st.Skipped})
		}
		fmt.Fprintf(stdout, "dry run, nothing sent: %d calls from %d files (%d streamed copies dropped, %d placeholders left out, %d lines skipped)\n",
			len(rows), st.Files, st.Copies, st.Synthetic, st.Skipped)
		if len(rows) > 0 {
			fmt.Fprintf(stdout, "%s .. %s\n", sum.First.Format(time.RFC3339), sum.Last.Format(time.RFC3339))
		}
		fmt.Fprintf(stdout, "reported_through %s: %s\n", h, through[h].Format(time.RFC3339))
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
