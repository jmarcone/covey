package usagepush

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Criterion 5 (#120): on a stripped excerpt of home's transcripts (main
// sessions, subagents, workflows, streamed copies), the reader gives the same
// call count and token totals per type as harness_usage.py, the script the
// operator's measurement was made with. expected.json is that script's output
// on testdata/transcripts.
func TestReadMatchesHarnessScript(t *testing.T) {
	var want struct {
		Messages                                             int
		Input, Output, CacheRead, CacheWrite5m, CacheWrite1h int64
	}
	b, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	want.Messages = int(raw["messages"].(float64))
	want.Input = int64(raw["input"].(float64))
	want.Output = int64(raw["output"].(float64))
	want.CacheRead = int64(raw["cache_read"].(float64))
	want.CacheWrite5m = int64(raw["cache_write_5m"].(float64))
	want.CacheWrite1h = int64(raw["cache_write_1h"].(float64))

	rows, st, err := Read([]string{"testdata/transcripts"}, "home", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	s := Summarize(rows)
	got := s.All
	if got.Rows != want.Messages || got.Input != want.Input || got.Output != want.Output ||
		got.CacheRead != want.CacheRead || got.CacheWrite5m != want.CacheWrite5m || got.CacheWrite1h != want.CacheWrite1h {
		t.Fatalf("reader %+v, script %+v", got, want)
	}
	if st.Copies == 0 {
		t.Fatal("the excerpt has streamed copies; none were dropped")
	}
	for _, o := range []string{OriginMain, OriginSubagent, OriginWorkflow} {
		if s.ByOrigin[o] == nil || s.ByOrigin[o].Rows == 0 {
			t.Fatalf("no %s calls read from the excerpt: %v", o, s.ByOrigin)
		}
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.MessageID] {
			t.Fatalf("message %s twice", r.MessageID)
		}
		seen[r.MessageID] = true
	}
}

func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sess1 is a session folder's name: a uuid, as Claude Code names it.
const sess1 = "11111111-2222-4333-8444-555555555555"

func TestReadRules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, sess1+".jsonl"),
		// a user turn that merely mentions "usage" in its text: not a call
		`{"type":"user","timestamp":"2026-11-18T10:00:00Z","message":{"content":"what is \"usage\"?"}}`,
		// two streamed copies of one call: the larger output wins
		`{"type":"assistant","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"secret"}],"usage":{"input_tokens":3,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40}}}}`,
		`{"type":"assistant","timestamp":"2026-11-18T10:00:02Z","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":9,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40}}}}`,
		// no cache_creation split: the whole write counts as 5-minute
		`{"type":"assistant","timestamp":"2026-11-18T10:05:00Z","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":70}}}`,
		// a split without the 5-minute key: the rest of the write is 5-minute
		`{"type":"assistant","timestamp":"2026-11-18T10:06:00Z","message":{"id":"m3","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":70,"cache_creation":{"ephemeral_1h_input_tokens":20}}}}`,
		// Claude Code's own placeholder: not a model call
		`{"type":"assistant","timestamp":"2026-11-18T10:07:00Z","message":{"id":"m4","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`not json "usage"`,
	)
	writeFile(t, filepath.Join(dir, sess1, "subagents", "agent-a.jsonl"),
		`{"type":"assistant","timestamp":"2026-11-18T10:01:00Z","message":{"id":"s1","model":"claude-haiku-5-5","usage":{"input_tokens":1,"output_tokens":1}}}`)
	writeFile(t, filepath.Join(dir, sess1, "subagents", "workflows", "wf_1", "agent-b.jsonl"),
		`{"type":"assistant","timestamp":"2026-11-18T10:02:00Z","message":{"id":"w1","model":"claude-haiku-5-5","usage":{"input_tokens":1,"output_tokens":1}}}`)

	rows, st, err := Read([]string{dir}, "box", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Row{}
	for _, r := range rows {
		by[r.MessageID] = r
	}
	if len(rows) != 5 || st.Copies != 1 || st.Synthetic != 1 {
		t.Fatalf("rows %d, stats %+v", len(rows), st)
	}
	if m1 := by["m1"]; m1.Output != 9 || m1.CacheWrite1h != 40 || m1.CacheWrite5m != 0 || m1.Origin != OriginMain || m1.SessionID != sess1 || m1.Host != "box" {
		t.Fatalf("m1 %+v", m1)
	}
	if m2 := by["m2"]; m2.CacheWrite5m != 70 || m2.CacheWrite1h != 0 {
		t.Fatalf("m2 %+v", m2)
	}
	if m3 := by["m3"]; m3.CacheWrite5m != 50 || m3.CacheWrite1h != 20 {
		t.Fatalf("m3 %+v", m3)
	}
	if by["s1"].Origin != OriginSubagent || by["w1"].Origin != OriginWorkflow || by["s1"].SessionID != sess1 || by["w1"].SessionID != sess1 {
		t.Fatalf("origins %+v %+v", by["s1"], by["w1"])
	}
	// --since keeps calls at or after it
	rows, _, _ = Read([]string{dir}, "box", time.Date(2026, 11, 18, 10, 5, 0, 0, time.UTC))
	if len(rows) != 2 {
		t.Fatalf("since: %d rows", len(rows))
	}
}

// Criterion 3 (#120): a request carries only the intake's fields; batches stay
// under the intake's row and size limits and cover every row once.
func TestBatchesCarryOnlyTheListedFields(t *testing.T) {
	rows := make([]Row, 12001)
	for i := range rows {
		rows[i] = Row{Host: "h", SessionID: "s", MessageID: "msg_" + strings.Repeat("x", 8) + string(rune('a'+i%26)) + itoa(i),
			TS: time.Date(2026, 11, 18, 0, 0, i%60, 0, time.UTC), Model: "claude-opus-5-5", Output: int64(i), Origin: OriginMain}
	}
	wm := map[string]time.Time{"h": time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)}
	bs, err := Batches("operator: home", &Meter{SecretKey: "claude_code_oauth_token"}, rows, wm)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 {
		t.Fatalf("%d batches", len(bs))
	}
	n := 0
	rowKeys := []string{"cache_read", "cache_write_1h", "cache_write_5m", "host", "input", "message_id", "model", "origin", "output", "session_id", "ts"}
	for i, b := range bs {
		if len(b) > MaxBody {
			t.Fatalf("batch of %d bytes", len(b))
		}
		var req map[string]json.RawMessage
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		want := "meter_credential,rows,source"
		if i == len(bs)-1 {
			// The watermark rides only on the last batch.
			want = "meter_credential,reported_through,rows,source"
		}
		if got := keys(req); strings.Join(got, ",") != want {
			t.Fatalf("batch %d: top-level keys %v, want %s", i, got, want)
		}
		var rs []map[string]json.RawMessage
		if err := json.Unmarshal(req["rows"], &rs); err != nil {
			t.Fatal(err)
		}
		if len(rs) > MaxRows {
			t.Fatalf("%d rows in one batch", len(rs))
		}
		for _, r := range rs {
			if got := keys(r); strings.Join(got, ",") != strings.Join(rowKeys, ",") {
				t.Fatalf("row keys %v", got)
			}
		}
		n += len(rs)
	}
	if n != len(rows) {
		t.Fatalf("%d rows sent of %d", n, len(rows))
	}
	if bs, _ := Batches("s", nil, nil, nil); len(bs) != 0 {
		t.Fatal("no rows, but a batch")
	}
	// No new calls still moves the watermark: one request, empty rows.
	bs, _ = Batches("s", nil, nil, wm)
	if len(bs) != 1 || !strings.Contains(string(bs[0]), `"rows":[]`) || !strings.Contains(string(bs[0]), `"reported_through":{"h":"2026-11-18T01:00:00Z"}`) {
		t.Fatalf("watermark-only push: %q", bs)
	}
}

func TestPostSendsTheKeyAndReadsTheCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cost/external-usage" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer covey_k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"source":"s","received":3,"inserted":1,"updated":0,"unchanged":2}`))
	}))
	defer srv.Close()
	res, err := Client{BaseURL: srv.URL + "/", Key: "covey_k"}.Post(context.Background(), []byte(`{}`))
	if err != nil || res.Received != 3 || res.Unchanged != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := (Client{BaseURL: srv.URL, Key: "wrong"}).Post(context.Background(), []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a refusal must be an error naming the status: %v", err)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// The session and origin come from each file's own place, whatever root is
// named: all projects, one project, or one session's folder (#145 review).
func TestReadPlacesFilesWhateverTheRoot(t *testing.T) {
	root := t.TempDir()
	line := func(id string) string {
		return `{"type":"assistant","timestamp":"2026-11-18T10:00:00Z","message":{"id":"` + id +
			`","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	}
	files := map[string]string{
		"-home-me-proj/" + sess1 + ".jsonl":                                  line("m-main"),
		"-home-me-proj/" + sess1 + "/subagents/agent-a.jsonl":                line("m-sub"),
		"-home-me-proj/" + sess1 + "/subagents/workflows/wf_1/agent-b.jsonl": line("m-wf"),
		"-home-me-other/s2.jsonl":                                            line("m-other"),
		"-home-me-proj/notes/subagents/agent-c.jsonl":                        line("m-odd"),
	}
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string][2]string{
		"m-main": {sess1, OriginMain}, "m-sub": {sess1, OriginSubagent},
		"m-wf": {sess1, OriginWorkflow}, "m-other": {"s2", OriginMain},
		// a folder above subagents/ that is no session folder names no session
		"m-odd": {UnknownSession, OriginSubagent},
	}
	sessRoot := filepath.Join(root, "-home-me-proj", sess1)
	for _, r := range []string{root, filepath.Join(root, "-home-me-proj"), sessRoot} {
		rows, _, err := Read([]string{r}, "h", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatalf("root %s: no rows", r)
		}
		for _, row := range rows {
			w := want[row.MessageID]
			if r == sessRoot && w[1] != OriginMain {
				w[0] = UnknownSession // the root's own name is never a session
			}
			if row.SessionID != w[0] || row.Origin != w[1] {
				t.Errorf("root %s, %s: session %q origin %q, want %q %q", r, row.MessageID, row.SessionID, row.Origin, w[0], w[1])
			}
		}
	}
}

func callLine(id, ts, extra string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"` + id +
		`","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2` + extra + `}}}`
}

// The place comes from the position below the root only: a directory called
// subagents above the root must not turn main sessions into subagent calls
// (#145 review).
func TestPlaceUsesThePositionBelowTheRoot(t *testing.T) {
	base := filepath.Join(t.TempDir(), "subagents", "workflows")
	root := filepath.Join(base, "projects")
	writeFile(t, filepath.Join(root, "-p", sess1+".jsonl"), callLine("m-main", "2026-11-18T10:00:00Z", ""))
	writeFile(t, filepath.Join(root, "-p", sess1, "subagents", "agent-a.jsonl"), callLine("m-sub", "2026-11-18T10:00:00Z", ""))
	writeFile(t, filepath.Join(root, "-p", sess1, "subagents", "workflows", "wf", "agent-b.jsonl"), callLine("m-wf", "2026-11-18T10:00:00Z", ""))
	// too deep to be either: a main session by the file name
	writeFile(t, filepath.Join(root, "-p", sess1, "subagents", "x", "y", "agent-c.jsonl"), callLine("m-deep", "2026-11-18T10:00:00Z", ""))
	rows, _, err := Read([]string{root}, "h", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{"m-main": {sess1, OriginMain}, "m-sub": {sess1, OriginSubagent}, "m-wf": {sess1, OriginWorkflow}, "m-deep": {"agent-c", OriginMain}}
	for _, r := range rows {
		if w := want[r.MessageID]; r.SessionID != w[0] || r.Origin != w[1] {
			t.Errorf("%s: %q %q, want %q %q", r.MessageID, r.SessionID, r.Origin, w[0], w[1])
		}
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	// the session folder as root: its name is the root's, never a session
	rows, _, _ = Read([]string{filepath.Join(root, "-p", sess1)}, "h", time.Time{})
	for _, r := range rows {
		if r.MessageID == "m-sub" && r.SessionID != UnknownSession {
			t.Errorf("session folder as root: %+v", r)
		}
	}
}

func TestReadFailsOnARootWithoutTranscripts(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(a, "s.jsonl"), callLine("m", "2026-11-18T10:00:00Z", ""))
	if _, _, err := Read([]string{a, b}, "h", time.Time{}); err == nil || !strings.Contains(err.Error(), "no *.jsonl") {
		t.Fatalf("an empty second root: %v", err)
	}
}

func TestReadReportsSymlinkedDirectories(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "s.jsonl"), callLine("m1", "2026-11-18T10:00:00Z", ""))
	writeFile(t, filepath.Join(other, "t.jsonl"), callLine("m2", "2026-11-18T10:00:00Z", ""))
	if err := os.Symlink(other, filepath.Join(root, "linked")); err != nil {
		t.Skip("no symlinks:", err)
	}
	rows, st, err := Read([]string{root}, "h", time.Time{})
	if err != nil || len(rows) != 1 || len(st.Symlinks) != 1 {
		t.Fatalf("rows %d, symlinks %v, %v", len(rows), st.Symlinks, err)
	}
}

// A line with a usage that cannot be read is counted; only the last line of a
// file touched in the last 30 minutes is taken for a reply still being written.
func TestReadCountsUnreadableLines(t *testing.T) {
	dir := t.TempDir()
	good := callLine("m1", "2026-11-18T10:00:00Z", "")
	mid := filepath.Join(dir, "mid.jsonl")
	writeFile(t, mid, good, `{"type":"assistant","message":{"usage":{"input_tok`, callLine("m2", "2026-11-18T10:01:00Z", ""))
	tail := filepath.Join(dir, "tail.jsonl")
	writeFile(t, tail, callLine("m3", "2026-11-18T10:02:00Z", ""), `{"type":"assistant","message":{"usage":{"input_tok`)

	_, st, err := Read([]string{dir}, "h", time.Time{})
	if err != nil || st.Skipped != 2 || st.Suspect != 1 {
		t.Fatalf("fresh files: %+v %v", st, err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(tail, old, old); err != nil {
		t.Fatal(err)
	}
	_, st, _ = Read([]string{dir}, "h", time.Time{})
	if st.Skipped != 2 || st.Suspect != 2 {
		t.Fatalf("old file with a broken last line: %+v", st)
	}
	// no id, no usable timestamp: skipped too, never silently
	writeFile(t, filepath.Join(dir, "bad.jsonl"),
		`{"type":"assistant","timestamp":"2026-11-18T10:00:00Z","message":{"model":"m","usage":{"input_tokens":1}}}`,
		`{"type":"assistant","timestamp":"soon","message":{"id":"x","model":"m","usage":{"input_tokens":1}}}`,
		good)
	_, st, _ = Read([]string{dir}, "h", time.Time{})
	if st.Skipped != 4 || st.Suspect != 4 {
		t.Fatalf("lines without id or timestamp: %+v", st)
	}
}

// Equal output: the earlier timestamp wins, so a file added later never moves
// a row already sent.
func TestDedupTieKeepsTheLaterCopy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.jsonl"), callLine("m", "2026-11-18T10:00:01Z", ""))
	writeFile(t, filepath.Join(dir, "b.jsonl"), callLine("m", "2026-11-18T10:00:02Z", ""))
	rows, _, _ := Read([]string{dir}, "h", time.Time{})
	if len(rows) != 1 || rows[0].TS.Second() != 2 {
		t.Fatalf("%+v", rows)
	}
	// the same ts in two files: the larger path
	writeFile(t, filepath.Join(dir, "a.jsonl"), callLine("m", "2026-11-18T10:00:05Z", `,"cache_read_input_tokens":1`))
	writeFile(t, filepath.Join(dir, "b.jsonl"), callLine("m", "2026-11-18T10:00:05Z", `,"cache_read_input_tokens":2`))
	rows, _, _ = Read([]string{dir}, "h", time.Time{})
	if len(rows) != 1 || rows[0].CacheRead != 2 {
		t.Fatalf("%+v", rows)
	}
	// streamed copies in one file with equal output: the later line (the script's choice)
	writeFile(t, filepath.Join(dir, "b.jsonl"), callLine("m", "2026-11-18T10:00:05Z", `,"cache_read_input_tokens":2`),
		callLine("m", "2026-11-18T10:00:07Z", `,"cache_read_input_tokens":3`))
	rows, _, _ = Read([]string{dir}, "h", time.Time{})
	if len(rows) != 1 || rows[0].TS.Second() != 7 || rows[0].CacheRead != 3 {
		t.Fatalf("%+v", rows)
	}
	// more output still wins
	writeFile(t, filepath.Join(dir, "b.jsonl"), strings.Replace(callLine("m", "2026-11-18T10:00:09Z", ""), `"output_tokens":2`, `"output_tokens":3`, 1))
	rows, _, _ = Read([]string{dir}, "h", time.Time{})
	if len(rows) != 1 || rows[0].Output != 3 {
		t.Fatalf("%+v", rows)
	}
}

// Parity with the script: an explicit null 5-minute write is 0, an absent one
// is the rest of cache_creation_input_tokens; an empty model and the other ISO
// timestamp forms are calls too.
func TestReadAcceptsWhatTheScriptAccepts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.jsonl"),
		callLine("null5m", "2026-11-18T10:00:00Z", `,"cache_creation_input_tokens":70,"cache_creation":{"ephemeral_5m_input_tokens":null,"ephemeral_1h_input_tokens":20}`),
		callLine("null1h", "2026-11-18T10:00:01Z", `,"cache_creation_input_tokens":70,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":null}`),
		callLine("naive", "2026-11-18T10:00:02.5", ""),
		callLine("space", "2026-11-18 10:00:03+01:00", ""),
		strings.Replace(callLine("nomodel", "2026-11-18T10:00:04Z", ""), `"model":"claude-opus-5-5",`, "", 1),
	)
	rows, st, err := Read([]string{dir}, "h", time.Time{})
	if err != nil || st.Skipped != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	by := map[string]Row{}
	for _, r := range rows {
		by[r.MessageID] = r
	}
	if r := by["null5m"]; r.CacheWrite5m != 0 || r.CacheWrite1h != 20 {
		t.Errorf("null 5m: %+v", r)
	}
	if r := by["null1h"]; r.CacheWrite5m != 5 || r.CacheWrite1h != 0 {
		t.Errorf("null 1h: %+v", r)
	}
	if len(by) != 5 || by["nomodel"].Model != UnknownModel || by["naive"].TS.Nanosecond() != 500_000_000 ||
		by["space"].TS.Hour() != 9 {
		t.Errorf("rows %+v", by)
	}
}

// A call the intake would refuse is held back and counted, not sent.
func TestReadHoldsBackCallsOutsideTheIntakeBounds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.jsonl"),
		callLine("ok", "2026-11-18T10:00:00Z", ""),
		callLine("huge", "2026-11-18T10:00:01Z", `,"cache_read_input_tokens":1000000001`),
		callLine("max", "2026-11-18T10:00:02Z", `,"cache_read_input_tokens":1000000000`),
		callLine("neg", "2026-11-18T10:00:03Z", `,"cache_read_input_tokens":-1`),
		callLine(strings.Repeat("x", 257), "2026-11-18T10:00:04Z", ""),
	)
	rows, st, _ := Read([]string{dir}, "h", time.Time{})
	if len(rows) != 2 || st.Invalid != 3 || len(st.InvalidWhy) != 3 {
		t.Fatalf("rows %d, %+v", len(rows), st)
	}
}

type fakeEntry struct {
	name string
	mode fs.FileMode
}

func (f fakeEntry) Name() string               { return f.name }
func (f fakeEntry) IsDir() bool                { return f.mode.IsDir() }
func (f fakeEntry) Type() fs.FileMode          { return f.mode.Type() }
func (f fakeEntry) Info() (fs.FileInfo, error) { return nil, os.ErrNotExist }

// On Windows a junction is ModeIrregular, not ModeSymlink: it is a directory
// that WalkDir does not enter, and it must be listed all the same.
func TestVisitListsJunctionsAndOtherEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "real", "s.jsonl"), callLine("m", "2026-11-18T10:00:00Z", ""))
	var st Stats
	var files []string
	visit(filepath.Join(dir, "real"), fakeEntry{"real", fs.ModeIrregular}, &st, &files)
	visit(filepath.Join(dir, "sock"), fakeEntry{"sock", fs.ModeSocket}, &st, &files)
	visit(filepath.Join(dir, "x.jsonl"), fakeEntry{"x.jsonl", fs.ModeIrregular}, &st, &files)
	visit(filepath.Join(dir, "real", "s.jsonl"), fakeEntry{"s.jsonl", 0}, &st, &files)
	if len(st.Symlinks) != 1 || len(st.Other) != 1 || len(st.Unread) != 1 || len(files) != 1 {
		t.Fatalf("%+v %v", st, files)
	}
}

// A line cut off by a crash and followed by the next record holds an old
// timestamp: it is damage even in a file written a minute ago.
func TestReadCrashGluedLastLineIsSuspect(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	recent := now.Add(-time.Minute).Format(time.RFC3339)
	f := filepath.Join(dir, "s.jsonl")
	writeFile(t, f, callLine("m1", recent, ""),
		`{"type":"assistant","timestamp":"`+now.Add(-3*time.Hour).Format(time.RFC3339)+`","message":{"usage":{"input_tok`+callLine("m2", recent, ""))
	_, st, err := Read([]string{dir}, "h", time.Time{})
	if err != nil || st.Suspect != 1 || len(st.SuspectAt) != 1 || st.SuspectAt[0] != f+":2" {
		t.Fatalf("%+v %v", st, err)
	}
	// a broken last line with only recent timestamps is still being written
	writeFile(t, f, callLine("m1", recent, ""), `{"type":"assistant","timestamp":"`+recent+`","message":{"usage":{"input_tok`)
	_, st, _ = Read([]string{dir}, "h", time.Time{})
	if st.Skipped != 1 || st.Suspect != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestReadSingleFileRoot(t *testing.T) {
	f := filepath.Join(t.TempDir(), "sess9.jsonl")
	writeFile(t, f, callLine("m", "2026-11-18T10:00:00Z", ""))
	rows, _, err := Read([]string{f}, "h", time.Time{})
	if err != nil || len(rows) != 1 || rows[0].SessionID != "sess9" || rows[0].Origin != OriginMain {
		t.Fatalf("%+v %v", rows, err)
	}
}
