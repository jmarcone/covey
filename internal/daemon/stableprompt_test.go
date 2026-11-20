package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

/* Every run of an agent wrote its whole system prompt into the prompt cache
   again on its first turn, 14-25k tokens, because the prompt carried what
   belongs to the task: the wiki's hits and index, the paragraph that names
   ~/scratch/<task-id>, and Claude Code's own memory path, which holds the cwd
   (#23). These tests pin the split: the system prompt is the agent's and stays
   byte for byte the same; the task's share goes into the first user message,
   each part once, and a resume sends neither again. */

const (
	stableTaskA = "4b6f1c2e-0d3a-4e5f-8a9b-1c2d3e4f5a6b"
	stableTaskB = "9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b"
)

// stableSpec is one task of the same agent: everything that is the agent's is
// the same for both, everything that is the task's differs.
func stableSpec(taskID, title, body, wiki string) RunSpec {
	return RunSpec{
		TaskID: taskID, Title: title, Body: body,
		SystemPrompt:  "You are the developer.\n\n## Your workplace\n\nThe sandbox.",
		MemoryContext: wiki,
		ScratchDoc:    scratchParagraph(filepath.Join("/home/agent", scratchRoot, taskID)),
		MCPConfig:     `{"mcpServers":{"covey":{"type":"http","url":"http://127.0.0.1:4711/mcp"}}}`,
		AllowedTools:  []string{"Bash", "Read", "Edit"},
		Model:         "claude-opus-5-5",
		Effort:        "high",
		MaxTurns:      30,
		HomeDir:       "/home/agent",
	}
}

func stablePair() (RunSpec, RunSpec) {
	a := stableSpec(stableTaskA, "Fix the payment bug", "pay.go panics on an empty basket.",
		"## Relevant from your wiki\n\n### Payments [[payments]]\nThe gateway retries twice.\n## Your wiki (index)\n\n[[payments]] [[customers]]\n")
	b := stableSpec(stableTaskB, "Answer the customer", "Meier asks about invoice 17.",
		"## Relevant from your wiki\n\n### Customer Meier [[meier]]\nPays by invoice.\n## Your wiki (index)\n\n[[payments]] [[meier]]\n")
	return a, b
}

// assertOnce fails unless every part stands in text exactly once.
func assertOnce(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if n := strings.Count(text, p); n != 1 {
			t.Fatalf("%q stands %d times in the first message, want once:\n%s", p, n, text)
		}
	}
}

func TestClaudeCodeSystemPromptIsTheSameForEveryTask(t *testing.T) {
	a, b := stablePair()
	c := &ClaudeCode{Binary: "claude"}
	argsA, _ := c.buildArgs(a)
	argsB, _ := c.buildArgs(b)

	sysA, sysB := flagValue(argsA, "--append-system-prompt"), flagValue(argsB, "--append-system-prompt")
	if sysA != a.SystemPrompt || sysB != sysA {
		t.Fatalf("the appended system prompt has to be the agent's configuration only, the same for both tasks\nA: %q\nB: %q", sysA, sysB)
	}
	// Everything but the first message is the same: tools, MCP, model, effort.
	if x, y := withoutFlags(argsA, "-p"), withoutFlags(argsB, "-p"); !slices.Equal(x, y) {
		t.Fatalf("two tasks of one agent differ outside the first message\nA: %q\nB: %q", x, y)
	}

	for _, s := range []RunSpec{a, b} {
		args, _ := c.buildArgs(s)
		p := flagValue(args, "-p")
		assertOnce(t, p, s.Title, s.Body, s.MemoryContext, s.ScratchDoc)
		if !strings.Contains(p, "~/scratch/"+s.TaskID+"/") {
			t.Fatalf("the first message has to name the task's own directory:\n%s", p)
		}
		// The task comes last, after what the agent knows.
		if !strings.HasSuffix(p, s.Title+"\n\n"+s.Body) {
			t.Fatalf("the task has to close the first message:\n%s", p)
		}
	}
}

// A resume and the handover turn send the resume input only, and the same
// appended system prompt as the fresh run: the session holds the task already,
// and a different system prompt would cost the conversation's cached prefix on
// a CLI that does not keep the session's own record of it.
func TestClaudeCodeResumeSendsNothingOfTheTaskAgain(t *testing.T) {
	a, _ := stablePair()
	c := &ClaudeCode{Binary: "claude"}
	fresh, _ := c.buildArgs(a)

	resumed := a
	resumed.ResumeSessionID = "sess-a"
	resumed.ResumeInput = "The customer answered: Chrome."
	args, _ := c.buildArgs(resumed)
	if got := flagValue(args, "-p"); got != resumed.ResumeInput {
		t.Fatalf("a resume speaks the resume input only, got %q", got)
	}
	if got := flagValue(args, "--append-system-prompt"); got != flagValue(fresh, "--append-system-prompt") {
		t.Fatalf("a resume has to pass the fresh run's system prompt unchanged, got %q", got)
	}
	all := strings.Join(args, "\x00")
	for _, part := range []string{a.Title, a.Body, "Relevant from your wiki", "Where your files go"} {
		if strings.Contains(all, part) {
			t.Fatalf("%q is in the session already and must not be sent on resume:\n%q", part, args)
		}
	}

	summary := c.summaryArgs(a, "sess-a")
	if got := flagValue(summary, "--append-system-prompt"); got != a.SystemPrompt {
		t.Fatalf("the handover turn passes the run's system prompt, got %q", got)
	}
	if got := flagValue(summary, "-p"); got != maxTurnsSummaryPrompt {
		t.Fatalf("the handover turn asks with the summary prompt only, got %q", got)
	}
}

func TestSevenCodeConfigComesFirstAndAlone(t *testing.T) {
	a, b := stablePair()
	s := &SevenCode{}
	head := a.SystemPrompt + "\n\n---\n\n"
	for _, spec := range []RunSpec{a, b} {
		p := s.taskPrompt(spec)
		if !strings.HasPrefix(p, head) {
			t.Fatalf("the configuration has to open the message, alone, before the separator:\n%s", p)
		}
		if rest := strings.TrimPrefix(p, head); rest != taskInput(spec) {
			t.Fatalf("after the separator comes the same first message Claude Code gets\ngot:  %q\nwant: %q", rest, taskInput(spec))
		}
		assertOnce(t, p, spec.Title, spec.Body, spec.MemoryContext, spec.ScratchDoc)
	}

	resumed := a
	resumed.ResumeSessionID = "ses_a"
	resumed.ResumeInput = "The customer answered: Chrome."
	if got := s.taskPrompt(resumed); got != resumed.ResumeInput {
		t.Fatalf("a resume speaks the resume input only, got %q", got)
	}
}

func TestTaskInputWithoutContextIsTheTask(t *testing.T) {
	spec := RunSpec{Title: "Ticket 42", Body: "Bitte prüfen"}
	if got := taskInput(spec); got != "Ticket 42\n\nBitte prüfen" {
		t.Fatalf("without wiki and directory the first message is the task as before, got %q", got)
	}
}

// fakeClaudeEnv is a fake `claude` that ends the main run at the turn limit and
// answers the handover turn, and writes each process's environment to main.env
// or summary.env.
func fakeClaudeEnv(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	content := "#!/bin/sh\n" +
		"if printf '%s\\n' \"$@\" | grep -qx -- '--resume'; then\n" +
		"env > \"" + dir + "/summary.env\"\n" +
		"cat <<'EOF'\n" +
		`{"type":"result","subtype":"success","session_id":"sess-env","result":"## Done\nnothing","total_cost_usd":0.05}` + "\n" +
		"EOF\n" +
		"else\n" +
		"env > \"" + dir + "/main.env\"\n" +
		"cat <<'EOF'\n" +
		`{"type":"result","subtype":"error_max_turns","session_id":"sess-env","num_turns":3,"total_cost_usd":0.9}` + "\n" +
		"EOF\n" +
		"fi\n"
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// autoMemoryValues returns every value the environment file gives the switch.
func autoMemoryValues(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vals []string
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "CLAUDE_CODE_DISABLE_AUTO_MEMORY="); ok {
			vals = append(vals, v)
		}
	}
	return vals
}

// Claude Code's auto memory is off for every process covey starts: the run and
// its handover turn here, a sub-run because it goes through the same Run, and
// educa below. Not even the daemon's own environment can turn it back on.
func TestClaudeCodeRunsWithAutoMemoryOff(t *testing.T) {
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")
	bin, dir := fakeClaudeEnv(t)
	spec := stableSpec(stableTaskA, "Big assignment", "Fix the payment bug", "")
	spec.HomeDir, spec.MaxTurns = dir, 3
	spec.Env = []string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY=0"}
	res, err := (&ClaudeCode{Binary: bin}).Run(context.Background(), spec, func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete" {
		t.Fatalf("the fake ends at the turn limit, so the handover turn has to run: %+v", res)
	}
	for _, f := range []string{"main.env", "summary.env"} {
		if got := autoMemoryValues(t, filepath.Join(dir, f)); !slices.Equal(got, []string{"1"}) {
			t.Fatalf("%s: auto memory has to be off, once and unambiguously, got %q", f, got)
		}
	}
}

func TestEducaRunsWithAutoMemoryOff(t *testing.T) {
	bin, dir := fakeClaudeWithEnv(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s1","result":"COVEY_STATUS: {\"status\":\"done\",\"result\":\"fertig\"}","total_cost_usd":1.23,"usage":{"input_tokens":10,"output_tokens":5}}
EOF`)
	_, err := newTestEduca(bin).Run(context.Background(), RunSpec{
		TaskID: "t1", Title: "Aufgabe", Body: "Text", Model: "gpt-oss-120b", HomeDir: dir,
		Env: []string{"ANTHROPIC_AUTH_TOKEN=brokered"},
	}, func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := autoMemoryValues(t, filepath.Join(dir, "env.txt")); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("educa runs the same harness and has to run with auto memory off, got %q", got)
	}
}
