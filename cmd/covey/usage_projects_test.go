package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	sessA = "0b5c2f9e-1111-4222-8333-944445555666"
	sessB = "7d1e0a3b-2222-4333-8444-a55556666777"
)

// projectsTree makes the ~/.claude/projects of a user "tester" on Windows
// with three project folders: a (a main session and its subagent), b (a call
// and a line broken long ago) and c (memory only, its transcripts cleaned up).
func projectsTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Users", "tester", ".claude", "projects")
	write := func(p string, lines ...string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	call := func(id string) string {
		return `{"type":"assistant","timestamp":"2026-11-18T10:00:00Z","cwd":"C:\\Users\\tester\\projects\\a","message":{"id":"` + id +
			`","model":"claude-opus-5-5","content":[{"type":"text","text":"see /home/tester/secret"}],"usage":{"input_tokens":1,"output_tokens":2}}}`
	}
	a := filepath.Join(root, "C--Users-tester-projects-a")
	write(filepath.Join(a, sessA+".jsonl"), call("a1"))
	write(filepath.Join(a, sessA, "subagents", "agent-x.jsonl"), call("a2"))
	b := filepath.Join(root, "c--Users-tester-projects-b")
	write(filepath.Join(b, sessB+".jsonl"), call("b1"))
	bad := filepath.Join(b, "broken.jsonl")
	write(bad, `{"type":"assistant","message":{"usage":{"input_tok`, "{}")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(bad, old, old)
	write(filepath.Join(root, "C--Users-tester-projects-c", "memory", "MEMORY.md"), "notes")
	return root
}

// --source-per-project: each project folder is pushed as its own source, with
// its own claim. A problem in one folder withholds that folder's claim only;
// a folder without transcripts still claims, so that an old claim of it does
// not hold the meter split back.
func TestUsagePushPerProjectClaimsPerSource(t *testing.T) {
	root := projectsTree(t)
	label := "C--Users-tester-projects-a=covey operator"
	bodies, out, _, err := pushTo(t, root, "--source-per-project", "--label", label, "--host", "h1", "--complete", "--meter-secret", "claude_code_oauth_token")
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		rows  string
		claim bool
	}
	by := map[string]got{}
	for _, b := range bodies {
		var ids []string
		for _, r := range b["rows"].([]any) {
			row := r.(map[string]any)
			ids = append(ids, row["message_id"].(string)+"@"+row["session_id"].(string)+":"+row["origin"].(string))
		}
		src := b["source"].(string)
		if _, dup := by[src]; dup {
			t.Fatalf("source %s in two requests", src)
		}
		if b["meter_credential"] == nil {
			t.Errorf("%s: no meter link", src)
		}
		by[src] = got{strings.Join(ids, ","), b["reported_through"] != nil}
	}
	want := map[string]got{
		"covey operator": {"a1@" + sessA + ":main,a2@" + sessA + ":subagent", true},
		"projects-b":     {"b1@" + sessB + ":main", false},
		"projects-c":     {"", true},
	}
	if len(by) != len(want) {
		t.Fatalf("sources %v", by)
	}
	for s, w := range want {
		if by[s] != w {
			t.Errorf("%s: %+v, want %+v", s, by[s], w)
		}
	}
	if !strings.Contains(out, `no reported_through sent for "projects-b": 1 lines with a usage could not be read`) ||
		strings.Contains(out, `sent for "covey operator"`) || !strings.Contains(out, "sent 3 calls in 3 requests") {
		t.Errorf("output:\n%s", out)
	}

	// without --complete, or with --since, no source claims
	for _, extra := range [][]string{nil, {"--complete", "--since", "2026-11-01T00:00:00Z"}} {
		bodies, _, _, err := pushTo(t, append([]string{root, "--source-per-project", "--source", "home", "--host", "h1"}, extra...)...)
		if err != nil || len(bodies) != 2 {
			t.Fatalf("%v: %v, %d bodies", extra, err, len(bodies))
		}
		for _, b := range bodies {
			if b["reported_through"] != nil || !strings.HasPrefix(b["source"].(string), "home: projects-") {
				t.Errorf("%v: %v", extra, b)
			}
		}
	}
}

// Only token counts and the label leave the machine: no request carries a
// path, the user name of the home directory or anything of the text.
func TestUsagePushPerProjectSendsNoPath(t *testing.T) {
	root := projectsTree(t)
	bodies, _, _, err := pushTo(t, root, "--source-per-project", "--host", "h1", "--complete")
	if err != nil || len(bodies) != 3 {
		t.Fatalf("%v, %d bodies", err, len(bodies))
	}
	for _, b := range bodies {
		raw, _ := json.Marshal(b)
		s := string(raw)
		for _, bad := range []string{"/", `\`, "tester", "Users", "C--", "c--", ".claude", "secret", "MEMORY", "broken", "agent-x"} {
			if strings.Contains(s, bad) {
				t.Errorf("a request carries %q: %s", bad, s)
			}
		}
	}
}

func TestUsagePushPerProjectDryRun(t *testing.T) {
	root := projectsTree(t)
	var out bytes.Buffer
	err := runUsage([]string{"push", root, "--source-per-project", "--complete", "--host", "h1", "--dry-run",
		"--label", "C--Users-tester-projects-a=covey operator"}, &out, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	o := out.String()
	lines := map[string]string{}
	for _, l := range strings.Split(o, "\n") {
		if strings.HasPrefix(l, "  source ") {
			name, _, _ := strings.Cut(strings.TrimPrefix(l, "  source "), "  calls")
			lines[strings.TrimSpace(name)] = l
		}
	}
	if !strings.Contains(o, "dry run, nothing sent: 3 calls") || !strings.Contains(o, "3 sources:\n") || len(lines) != 3 ||
		!strings.Contains(lines[`"covey operator"`], "calls      2 ") || !strings.Contains(lines[`"covey operator"`], "reported_through h1: ") ||
		!strings.Contains(lines[`"projects-b"`], "calls      1 ") || !strings.HasSuffix(lines[`"projects-b"`], "no reported_through") ||
		!strings.Contains(lines[`"projects-c"`], "calls      0 ") || !strings.Contains(lines[`"projects-c"`], "reported_through h1: ") {
		t.Errorf("per source:\n%s", o)
	}
	// JSON: the same per source
	out.Reset()
	if err := runUsage([]string{"push", root, "--source-per-project", "--complete", "--host", "h1", "--dry-run", "--json"}, &out, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	var j struct {
		Sources []struct {
			Source  string
			Totals  struct{ Rows int }
			Through *time.Time `json:"reported_through"`
		}
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || len(j.Sources) != 3 || j.Sources[0].Source != "projects-a" ||
		j.Sources[0].Totals.Rows != 2 || j.Sources[0].Through == nil || j.Sources[1].Through != nil {
		t.Errorf("%v %s", err, out.String())
	}

	// an unknown home directory in a folder name is refused, not sent
	if err := os.MkdirAll(filepath.Join(root, "D--work-z", sessA), 0o755); err != nil {
		t.Fatal(err)
	}
	err = runUsage([]string{"push", root, "--source-per-project", "--dry-run"}, &bytes.Buffer{}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "D--work-z") || !strings.Contains(err.Error(), "--home-prefix") {
		t.Fatalf("%v", err)
	}
}
