// Package usagepush reads Claude Code transcripts on the machine they were
// written on and turns them into the token-only rows that
// POST /api/v1/cost/external-usage takes (#120, part B).
//
// Only counts leave the machine: the message id, the session id, the model,
// the timestamp and the five token counts. No prompt, response, tool input or
// tool output is ever read into a Row.
//
// The parsing is a port of covey-experiment scripts/measure/harness_usage.py,
// the script the operator's measurement was made with, so that covey's figures
// and the measurement agree:
//   - every *.jsonl under the root, recursively (main sessions, subagents/,
//     workflows/);
//   - a row is an "assistant" line whose message carries an id and a usage;
//   - streamed copies of one message id: the copy with the most output tokens
//     wins, a later copy with equal output replaces an earlier one;
//   - cache writes: cache_creation.ephemeral_1h_input_tokens is the 1-hour
//     write, ephemeral_5m_input_tokens the 5-minute write (an explicit null
//     counts as 0); without the 5-minute key, cache_creation_input_tokens
//     minus the 1-hour part counts as 5-minute;
//   - an empty model is read (as "unknown"), as are the other ISO 8601
//     timestamp forms Python takes.
package usagepush

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Row is one model call, in exactly the shape the intake takes. Nothing else
// is sent: the intake refuses unknown fields, and this type has no room for
// text.
type Row struct {
	Host         string    `json:"host"`
	SessionID    string    `json:"session_id"`
	MessageID    string    `json:"message_id"`
	TS           time.Time `json:"ts"`
	Model        string    `json:"model"`
	Input        int64     `json:"input"`
	Output       int64     `json:"output"`
	CacheRead    int64     `json:"cache_read"`
	CacheWrite5m int64     `json:"cache_write_5m"`
	CacheWrite1h int64     `json:"cache_write_1h"`
	Origin       string    `json:"origin"`
}

// Origins of a call, from the transcript's path.
const (
	OriginMain     = "main"
	OriginSubagent = "subagent"
	OriginWorkflow = "workflow"
)

// SyntheticModel is the model Claude Code writes on placeholder messages it
// makes up itself (an interrupted or failed turn). They carry zero tokens and
// were never a model call, so they are not sent.
const SyntheticModel = "<synthetic>"

// optInt is a count that tells an absent key from an explicit null. The
// script reads an explicit null as 0, where an absent key takes the default.
type optInt struct {
	Set bool
	V   int64
}

func (o *optInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.V = 0
		return nil
	}
	return json.Unmarshal(b, &o.V)
}

// transcript line, only the fields we read. Everything else in the line
// (content, tool calls) is never decoded into a Go value.
type line struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m optInt `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h optInt `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// The intake's bounds (observability.externalMaxTokens, externalMaxField): a
// row outside them would fail its whole batch, so it is held back instead.
const (
	MaxTokens = 1_000_000_000
	MaxText   = 256
)

// UnknownModel stands in for an empty model: the script counts such a call,
// the intake wants a model on every row.
const UnknownModel = "unknown"

// WatermarkMargin is left between the run's time and the claim, for replies
// still being written and for clock skew. A broken last line is looked at
// again (see RecheckWait) only in a file modified within it (after the time
// the claim is made for), and only when none of its timestamps is older.
const WatermarkMargin = 15 * time.Minute

// RecheckWait is how long after the first look a broken last line is read
// again. Claude Code appends each line in one write, so a line that was only
// half written at the first look is whole by then; one that is unchanged was
// cut short (a crash) and is suspect.
const RecheckWait = 1500 * time.Millisecond

// tsInText finds the timestamps in a line too broken to decode.
var tsInText = regexp.MustCompile(`"timestamp"\s*:\s*"([^"]+)"`)

// clock is time.Now, replaced in tests.
var clock = time.Now

// settle waits until RecheckWait has passed since first, the latest first
// look at a broken last line; replaced in tests.
var settle = func(first time.Time) { time.Sleep(time.Until(first.Add(RecheckWait))) }

// walkDir is filepath.WalkDir, replaced in tests.
var walkDir = filepath.WalkDir

// UnknownSession is the session of a transcript whose place names no session
// by a uuid (see place).
const UnknownSession = "unknown"

// sessionName is the name of a session folder: a uuid.
var sessionName = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidText says whether s is acceptable as the intake's text field: UTF-8, no
// NUL, at most MaxText bytes.
func ValidText(s string) bool {
	return utf8.ValidString(s) && len(s) <= MaxText && !strings.ContainsRune(s, 0)
}

// Stats says what a read found besides the rows.
type Stats struct {
	Files     int // transcript files read
	Lines     int // assistant lines with a usage
	Copies    int // streamed copies dropped by the dedup
	Synthetic int // placeholder messages left out
	// Skipped counts lines that are no well-formed JSON (whatever they hold:
	// a line cut short loses its "usage" first, the content comes before it)
	// and lines with a usage that gave no call: no message id, no valid
	// timestamp.
	Skipped int
	// Suspect is the part of Skipped that is not a last line still being
	// written: one cut short at its end (nothing after the break) that grew
	// between two looks RecheckWait apart, in a file modified within
	// WatermarkMargin. Calls may be missing then, so the caller must not
	// claim to have sent every call.
	Suspect int
	// MidWrite is set when a last line was taken for one still being
	// written: the earliest of that file's change time and the timestamp of
	// the first copy of the last call before it in that file (the line may be
	// its final copy, with more output than the copy sent). A claim must stay
	// below it.
	MidWrite time.Time
	// Symlinks lists symlinked directories below a root. They are not
	// followed, so calls below them are not read.
	Symlinks []string
	// Unread lists *.jsonl entries that are not regular files (and no
	// symlink to one): they cannot be read. Other, non-regular entries that
	// are no directory are in Other. Both are only warned about, but Unread
	// also sends no claim.
	Unread, Other []string
	// SuspectAt lists file:line of the first Suspect lines.
	SuspectAt []string
	// Invalid counts calls held back because a count or a text field is
	// outside the intake's bounds; InvalidWhy names the first few.
	Invalid    int
	InvalidWhy []string // "file:line: message id: why", the first few
}

// MaxListed is how many locations a Stats keeps for the user to fix.
const MaxListed = 20

// rowProblem says why the intake would refuse r, or "".
func rowProblem(r Row) string {
	for _, f := range []struct{ n, v string }{{"session_id", r.SessionID}, {"message_id", r.MessageID}, {"model", r.Model}} {
		if !ValidText(f.v) {
			return fmt.Sprintf("%s is not UTF-8 without NUL of at most %d bytes", f.n, MaxText)
		}
	}
	for _, c := range []struct {
		n string
		v int64
	}{{"input", r.Input}, {"output", r.Output}, {"cache_read", r.CacheRead}, {"cache_write_5m", r.CacheWrite5m}, {"cache_write_1h", r.CacheWrite1h}} {
		if c.v < 0 || c.v > MaxTokens {
			return fmt.Sprintf("%s %d is outside 0..%d", c.n, c.v, MaxTokens)
		}
	}
	return ""
}

// tsLayouts are the timestamp forms accepted: RFC 3339 (what Claude Code
// writes) and the other ISO 8601 forms Python's fromisoformat takes, which is
// what the script reads. A time without a zone is taken as UTC.
var tsLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

func parseTS(s string) (time.Time, bool) {
	for _, l := range tsLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Read walks each root for *.jsonl and returns one Row per message id, sorted
// by (ts, message id). host is stored on every row. Rows before since are
// left out (zero since keeps everything). A root without any *.jsonl is an
// error, even when another root has files.
func Read(roots []string, host string, since time.Time) ([]Row, Stats, error) {
	return read(roots, host, since, false)
}

// read is Read; with project set it reads project folders for
// --source-per-project: a root without any *.jsonl gives no rows rather than
// an error, and a directory or file that cannot be read is listed in Unread
// (it withholds the claim of that source only) rather than failing the push.
func read(roots []string, host string, since time.Time, project bool) ([]Row, Stats, error) {
	var st Stats
	type kept struct {
		row   Row
		path  string
		line  int
		first time.Time // the earliest copy's timestamp
	}
	seen := map[string]*kept{}
	// Broken last lines to look at again once everything else is read.
	type pendingTail struct {
		*tail
		abs    string
		lastID string // the last call before it in its file
		each   func(l *line, no int) bool
		skip   func(no int)
	}
	var tails []pendingTail
	suspect := func(abs string, no int) {
		st.Skipped++
		st.Suspect++
		if len(st.SuspectAt) < MaxListed {
			st.SuspectAt = append(st.SuspectAt, fmt.Sprintf("%s:%d", abs, no))
		}
	}
	for _, root := range roots {
		// A root that is itself a symlink (a dotfiles setup) is followed;
		// WalkDir alone would list nothing below it.
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		if a, err := filepath.Abs(root); err == nil {
			root = a
		}
		var files []string
		err := walkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if project {
					st.Unread = append(st.Unread, p)
					return nil
				}
				return err
			}
			visit(p, d, &st, &files)
			return nil
		})
		if err != nil {
			return nil, st, fmt.Errorf("read %s: %w", root, err)
		}
		if len(files) == 0 && !project {
			return nil, st, fmt.Errorf("no *.jsonl transcripts under %s", root)
		}
		sort.Strings(files)
		for _, f := range files {
			abs, err := filepath.Abs(f)
			if err != nil {
				return nil, st, err
			}
			session, origin := place(root, abs)
			st.Files++
			lastID := ""
			each := func(l *line, no int) bool {
				m := l.Message
				st.Lines++
				if m.ID == "" {
					return false
				}
				ts, ok := parseTS(l.Timestamp)
				if !ok {
					return false
				}
				model := m.Model
				if model == "" {
					model = UnknownModel
				}
				u := m.Usage
				var w1h int64
				var w5m = u.CacheCreationInputTokens
				if cc := u.CacheCreation; cc != nil {
					w1h = cc.Ephemeral1h.V
					if cc.Ephemeral5m.Set {
						// An explicit null is 0, as in the script.
						w5m = cc.Ephemeral5m.V
					} else {
						w5m = u.CacheCreationInputTokens - w1h
					}
				}
				if w5m < 0 {
					w5m = 0
				}
				r := Row{
					Host: host, SessionID: session, MessageID: m.ID, TS: ts.UTC(), Model: model,
					Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens,
					CacheWrite5m: w5m, CacheWrite1h: w1h, Origin: origin,
				}
				lastID = m.ID
				if prev, ok := seen[m.ID]; ok {
					st.Copies++
					if r.TS.Before(prev.first) {
						prev.first = r.TS
					}
					// The copy with the most output wins. On equal output the
					// later timestamp, as the script keeps the later copy (the
					// streamed message's last line, nearest to when the meter
					// counts it), then the larger path, then the later line:
					// the choice does not depend on the order files are read
					// in, so a push of more files moves no row unless a file
					// holds a better copy.
					better := r.Output > prev.row.Output
					if r.Output == prev.row.Output {
						better = r.TS.After(prev.row.TS) || (r.TS.Equal(prev.row.TS) && (abs > prev.path || (abs == prev.path && no > prev.line)))
					}
					if better {
						prev.row, prev.path, prev.line = r, abs, no
					}
					return true
				}
				seen[m.ID] = &kept{row: r, path: abs, line: no, first: r.TS}
				return true
			}
			skip := func(no int) { suspect(abs, no) }
			tl, err := readFile(f, each, skip)
			if err != nil {
				if project {
					st.Unread = append(st.Unread, abs)
					continue
				}
				return nil, st, fmt.Errorf("read %s: %w", f, err)
			}
			if tl != nil {
				tails = append(tails, pendingTail{tail: tl, abs: abs, lastID: lastID, each: each, skip: skip})
			}
		}
	}
	// A broken last line is looked at again, RecheckWait after the first
	// look: a line still being written is whole by then (it is read as any
	// other), a line cut short by a crash is unchanged (suspect).
	if len(tails) > 0 {
		latest := tails[0].at
		for _, p := range tails {
			if p.at.After(latest) {
				latest = p.at
			}
		}
		settle(latest)
	}
	for _, p := range tails {
		b, live := recheck(p.tail)
		switch {
		case b != nil:
			takeLine(b, p.no, p.each, p.skip)
		case !live:
			suspect(p.abs, p.no)
		default:
			// Still being written: the claim stays below the file's change
			// and below the first copy of the last call before the line,
			// which may be that call's final copy with more output.
			st.Skipped++
			mw := p.mtime
			if k, ok := seen[p.lastID]; ok && k.first.Before(mw) {
				mw = k.first
			}
			if st.MidWrite.IsZero() || mw.Before(st.MidWrite) {
				st.MidWrite = mw
			}
		}
	}
	rows := make([]Row, 0, len(seen))
	for _, k := range seen {
		if k.row.Model == SyntheticModel {
			st.Synthetic++
			continue
		}
		if why := rowProblem(k.row); why != "" {
			st.Invalid++
			if len(st.InvalidWhy) < MaxListed {
				st.InvalidWhy = append(st.InvalidWhy, fmt.Sprintf("%s:%d: %s: %s", k.path, k.line, k.row.MessageID, why))
			}
			continue
		}
		if !since.IsZero() && k.row.TS.Before(since) {
			continue
		}
		rows = append(rows, k.row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].TS.Equal(rows[j].TS) {
			return rows[i].TS.Before(rows[j].TS)
		}
		return rows[i].MessageID < rows[j].MessageID
	})
	sort.Strings(st.Symlinks)
	sort.Strings(st.Unread)
	sort.Strings(st.Other)
	return rows, st, nil
}

// place names a transcript's session and origin by the file's own position
// below its root: <session>/subagents/<file> is a subagent of <session>,
// <session>/subagents/workflows/<wf>/<file> a workflow agent, and any other
// file a main session named after the file. The root may be all projects, one
// project, or one session's folder (then <session> is the root's own name).
// The root's own name counts for the layout, so a subagents/ or workflows/
// pushed alone holds subagent or workflow calls; nothing above the root is
// looked at. A session is named only by a uuid, as Claude Code names session
// folders and main transcripts: the folder above subagents/ (the root itself
// when subagents/ lies directly in it, a session folder pushed alone), or a
// main transcript's file name. Any other name (a project folder's, which
// holds its home directory, an agent's file nested deeper than this layout,
// the folder above a subagents/ pushed alone) gives UnknownSession.
func place(root, abs string) (session, origin string) {
	rel, err := filepath.Rel(root, abs)
	if err == nil && rel == "." { // the root is the file itself
		return sessionOf(strings.TrimSuffix(filepath.Base(abs), ".jsonl")), OriginMain
	}
	if err != nil {
		rel = filepath.Base(abs)
	}
	// The root's own name counts for the layout (a subagents/ pushed alone
	// holds subagent calls); nothing above it is looked at.
	parts := append([]string{filepath.Base(root)}, strings.Split(filepath.ToSlash(rel), "/")...)
	n := len(parts)
	sessionAt := func(k int) string { // the directory just above parts[k]
		if k > 0 {
			return sessionOf(parts[k-1])
		}
		return UnknownSession
	}
	for k := 0; k < n-1; k++ {
		if parts[k] != "subagents" {
			continue
		}
		if k == n-2 {
			return sessionAt(k), OriginSubagent
		}
		if k+1 < n-1 && parts[k+1] == "workflows" && k+3 == n-1 {
			return sessionAt(k), OriginWorkflow
		}
	}
	if parts[0] == "workflows" && n == 3 { // a workflows/ pushed alone
		return UnknownSession, OriginWorkflow
	}
	return sessionOf(strings.TrimSuffix(parts[n-1], ".jsonl")), OriginMain
}

// sessionOf is name when it is a session's (a uuid), else UnknownSession.
func sessionOf(name string) string {
	if sessionName.MatchString(name) {
		return name
	}
	return UnknownSession
}

// visit sorts one walked entry: a *.jsonl file is collected; a symlink or
// junction (Windows reports a reparse point as ModeIrregular) to a directory
// is not descended by WalkDir and is listed; any other non-regular entry that
// is no directory is listed too.
func visit(p string, d fs.DirEntry, st *Stats, files *[]string) {
	if d.IsDir() {
		return
	}
	jsonl := strings.HasSuffix(d.Name(), ".jsonl")
	if d.Type().IsRegular() {
		if jsonl {
			*files = append(*files, p)
		}
		return
	}
	fi, err := os.Stat(p) // follows a symlink
	switch {
	case err == nil && fi.IsDir():
		st.Symlinks = append(st.Symlinks, p)
	case err == nil && fi.Mode().IsRegular() && jsonl:
		*files = append(*files, p)
	case err == nil && fi.Mode().IsRegular():
	case jsonl:
		st.Unread = append(st.Unread, p)
	default:
		st.Other = append(st.Other, p)
	}
}

// tail is a broken last line that may still be being written; it is looked
// at again once the rest of the read is done (see recheck).
type tail struct {
	path  string
	off   int64  // where the line starts in the file
	raw   []byte // the line as first seen, without a line end
	no    int
	mtime time.Time // the file's change time at the first look
	at    time.Time // the first look
}

// readFile calls each for every assistant line with a usage; each says false
// when the line gave no call. skip is called for every line that is no
// well-formed JSON, and for every line with a usage that gave none. A broken
// last line is returned instead, to be looked at again, when it may still be
// being written: the file was changed within WatermarkMargin (after the time
// a claim is made for), the line is cut short at its end with nothing after
// the break (no line end, no next record glued on), and its text holds no
// older timestamp.
func readFile(path string, each func(l *line, lineNo int) bool, skip func(lineNo int)) (*tail, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	mtime := fi.ModTime()
	recent := clock().Sub(mtime) < WatermarkMargin
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	// Lines as bufio.ScanLines splits them, keeping where each starts and
	// whether it ended with a line end.
	var pos, start int64
	ended := false
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			start, ended = pos, true
			pos += int64(i + 1)
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			start, ended = pos, false
			pos += int64(len(data))
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	// A broken line, so far the last one. It cannot still be being written
	// when it ended with a line end (Claude Code writes a line and its end in
	// one write), when its decode fails before the end of the text (a crash
	// cut it and the next record was glued on), or when its text holds a
	// timestamp older than the margin.
	var pending *tail
	pendingOK := false
	no := 0
	for sc.Scan() {
		no++
		raw := sc.Bytes()
		b := bytes.TrimSpace(raw)
		if len(b) == 0 {
			continue
		}
		if pending != nil {
			skip(pending.no)
			pending = nil
		}
		// Every line must be whole JSON, before the cheap usage filter: a
		// line cut short (a killed process) loses its "usage" first, since
		// the content comes before it, and could have been a call.
		if !json.Valid(b) {
			pending = &tail{path: path, off: start, raw: bytes.Clone(raw), no: no, mtime: mtime}
			pendingOK = !ended && pureCut(b)
			for _, m := range tsInText.FindAllSubmatch(b, -1) {
				if t, ok := parseTS(string(m[1])); ok && clock().Sub(t) > WatermarkMargin {
					pendingOK = false
				}
			}
			continue
		}
		takeLine(b, no, each, skip)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, nil
	}
	if !recent || !pendingOK {
		skip(pending.no)
		return nil, nil
	}
	pending.at = time.Now()
	return pending, nil
}

// takeLine reads one whole JSON line b: each for an assistant line with a
// usage, skip when it gave no call.
func takeLine(b []byte, no int, each func(l *line, lineNo int) bool, skip func(lineNo int)) {
	// Cheap filter before decoding, as the script does: most lines are
	// user turns and tool results without a usage.
	if !bytes.Contains(b, []byte(`"usage"`)) {
		return
	}
	var l line
	if json.Unmarshal(b, &l) != nil {
		skip(no) // valid JSON of another shape: not a cut line
		return
	}
	if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
		return
	}
	if !each(&l, no) {
		skip(no)
	}
}

// pureCut says whether b, no well-formed JSON, is only cut short: its decode
// runs out of text (io.ErrUnexpectedEOF), as for a line still being written.
// A line with text after the break (the next record glued on after a crash)
// fails before its end with a *json.SyntaxError, and a whole value followed
// by more text decodes without error. (json.Unmarshal's SyntaxError offset
// does not tell these apart: it is the text's length for {"a":1}x too.)
func pureCut(b []byte) bool {
	var v json.RawMessage
	return errors.Is(json.NewDecoder(bytes.NewReader(b)).Decode(&v), io.ErrUnexpectedEOF)
}

// recheck looks at a broken last line again: it re-reads the file from where
// the line starts. It returns the line when it is now whole JSON (it was
// still being written at the first look), or live when it grew but is still
// cut short at its end (it is being written now). Neither when the line is
// unchanged (cut short by a crash, the file's change time bumped by a copy,
// sync or restore), now ends broken, or was rewritten.
func recheck(t *tail) (whole []byte, live bool) {
	f, err := os.Open(t.path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < t.off+int64(len(t.raw)) {
		return nil, false
	}
	got, err := bufio.NewReader(io.NewSectionReader(f, t.off, fi.Size()-t.off)).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, false
	}
	ended := err == nil
	if !bytes.HasPrefix(got, t.raw) {
		return nil, false
	}
	b := bytes.TrimSpace(got)
	switch {
	case json.Valid(b):
		return b, false
	case ended || len(got) == len(t.raw):
		return nil, false
	}
	return nil, pureCut(b)
}

// Totals sums rows per origin and model, for --dry-run and the tests.
type Totals struct {
	Rows         int   `json:"rows"`
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

func (t *Totals) add(r Row) {
	t.Rows++
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite5m += r.CacheWrite5m
	t.CacheWrite1h += r.CacheWrite1h
}

// Summary is what --dry-run prints.
type Summary struct {
	First    time.Time          `json:"first"`
	Last     time.Time          `json:"last"`
	All      Totals             `json:"all"`
	ByOrigin map[string]*Totals `json:"by_origin"`
	ByModel  map[string]*Totals `json:"by_model"`
}

// Summarize totals the rows.
func Summarize(rows []Row) Summary {
	s := Summary{ByOrigin: map[string]*Totals{}, ByModel: map[string]*Totals{}}
	for i, r := range rows {
		if i == 0 || r.TS.Before(s.First) {
			s.First = r.TS
		}
		if r.TS.After(s.Last) {
			s.Last = r.TS
		}
		s.All.add(r)
		for _, m := range []struct {
			to  map[string]*Totals
			key string
		}{{s.ByOrigin, r.Origin}, {s.ByModel, r.Model}} {
			t := m.to[m.key]
			if t == nil {
				t = &Totals{}
				m.to[m.key] = t
			}
			t.add(r)
		}
	}
	return s
}
