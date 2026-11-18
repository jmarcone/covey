package usagepush

import (
	"context"
	"encoding/json"
	"io"
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

func TestReadRules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sess1.jsonl"),
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
	writeFile(t, filepath.Join(dir, "sess1", "subagents", "agent-a.jsonl"),
		`{"type":"assistant","timestamp":"2026-11-18T10:01:00Z","message":{"id":"s1","model":"claude-haiku-5-5","usage":{"input_tokens":1,"output_tokens":1}}}`)
	writeFile(t, filepath.Join(dir, "sess1", "subagents", "workflows", "wf_1", "agent-b.jsonl"),
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
	if m1 := by["m1"]; m1.Output != 9 || m1.CacheWrite1h != 40 || m1.CacheWrite5m != 0 || m1.Origin != OriginMain || m1.SessionID != "sess1" || m1.Host != "box" {
		t.Fatalf("m1 %+v", m1)
	}
	if m2 := by["m2"]; m2.CacheWrite5m != 70 || m2.CacheWrite1h != 0 {
		t.Fatalf("m2 %+v", m2)
	}
	if m3 := by["m3"]; m3.CacheWrite5m != 50 || m3.CacheWrite1h != 20 {
		t.Fatalf("m3 %+v", m3)
	}
	if by["s1"].Origin != OriginSubagent || by["w1"].Origin != OriginWorkflow || by["s1"].SessionID != "sess1" {
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
	bs, err := Batches("operator: home", &Meter{SecretKey: "claude_code_oauth_token"}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 {
		t.Fatalf("%d batches", len(bs))
	}
	n := 0
	rowKeys := []string{"cache_read", "cache_write_1h", "cache_write_5m", "host", "input", "message_id", "model", "origin", "output", "session_id", "ts"}
	for _, b := range bs {
		if len(b) > MaxBody {
			t.Fatalf("batch of %d bytes", len(b))
		}
		var req map[string]json.RawMessage
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		if got := keys(req); strings.Join(got, ",") != "meter_credential,rows,source" {
			t.Fatalf("top-level keys %v", got)
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
	if bs, _ := Batches("s", nil, nil); len(bs) != 0 {
		t.Fatal("no rows, but a batch")
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
