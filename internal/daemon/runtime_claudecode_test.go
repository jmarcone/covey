package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeClaude creates a fake `claude` binary that writes its arguments to
// args.txt and emits a predefined stream-json protocol.
func fakeClaude(t *testing.T, script string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	content := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dir + "/args.txt\"\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestClaudeCodeAdapterFlagsAndResult(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"sess-123"}
{"type":"assistant","message":{"content":"denke nach"}}
{"type":"result","subtype":"success","session_id":"sess-123","result":"Erledigt.\nCOVEY_STATUS: {\"status\":\"done\",\"result\":\"Ticket beantwortet\",\"memory\":\"Kunde nutzt Chrome\"}","total_cost_usd":0.42,"usage":{"input_tokens":100,"output_tokens":50}}
EOF`)
	adapter := &ClaudeCode{Binary: bin}

	var events []string
	res, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t1", Title: "Ticket 42", Body: "Bitte prüfen",
		SystemPrompt: "Du bist der Support-Agent.",
		Model:        "claude-opus-4-8",
		AllowedTools: []string{"Bash", "Read"},
		MaxTurns:     30,
		HomeDir:      dir,
	}, func(kind string, payload json.RawMessage) {
		events = append(events, string(payload))
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "done" || res.Result != "Ticket beantwortet" || res.Memory != "Kunde nutzt Chrome" {
		t.Fatalf("result wrong: %+v", res)
	}
	if res.SessionID != "sess-123" {
		t.Fatalf("session_id missing: %+v", res)
	}
	if res.CostUSD != 0.42 || res.InputTokens != 100 {
		t.Fatalf("cost wrong: %+v", res)
	}
	if len(events) != 3 {
		t.Fatalf("every stream-json line must flow as an event, got %d", len(events))
	}

	args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	for _, want := range []string{"-p", "Ticket 42", "--output-format", "stream-json",
		"--append-system-prompt", "Du bist der Support-Agent.", "--allowedTools", "Bash,Read",
		"--max-turns", "30", "--model", "claude-opus-4-8", "--dangerously-skip-permissions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("flag %q missing from args:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--resume") {
		t.Fatal("without a resume session --resume must not be set")
	}
}

func TestClaudeCodeAdapterResume(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"sess-123","result":"COVEY_STATUS: {\"status\":\"done\",\"result\":\"fortgesetzt\"}","total_cost_usd":0.1,"usage":{"input_tokens":10,"output_tokens":5}}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	res, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t1", Title: "Ticket 42", Body: "ursprünglicher text",
		ResumeSessionID: "sess-123",
		ResumeInput:     "Kunde: es war Chrome",
		HomeDir:         dir,
	}, func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "done" || res.Result != "fortgesetzt" {
		t.Fatalf("resume result wrong: %+v", res)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	got := string(args)
	if !strings.Contains(got, "--resume") || !strings.Contains(got, "sess-123") {
		t.Fatalf("--resume <session_id> missing:\n%s", got)
	}
	if !strings.Contains(got, "Kunde: es war Chrome") {
		t.Fatalf("the resume input must be the new prompt:\n%s", got)
	}
	if strings.Contains(got, "ursprünglicher text") {
		t.Fatalf("on resume the old body must not be sent again:\n%s", got)
	}
	if strings.Contains(got, "--model") {
		t.Fatalf("without a model field --model must not be set:\n%s", got)
	}
}

func TestClaudeCodeAdapterBlocked(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"sess-9","result":"COVEY_STATUS: {\"status\":\"blocked\",\"correlation_key\":\"zammad:ticket:7\",\"question\":\"Warte auf Kunde\"}","total_cost_usd":0.05,"usage":{"input_tokens":5,"output_tokens":2}}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	res, err := adapter.Run(context.Background(), RunSpec{TaskID: "t", Title: "x", HomeDir: dir},
		func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "blocked" || res.CorrelationKey != "zammad:ticket:7" || res.SessionID != "sess-9" {
		t.Fatalf("blocked mapping wrong: %+v", res)
	}
}

func TestClaudeCodeAdapterExitError(t *testing.T) {
	bin, dir := fakeClaude(t, "exit 7")
	adapter := &ClaudeCode{Binary: bin}
	res, err := adapter.Run(context.Background(), RunSpec{TaskID: "t", Title: "x", HomeDir: dir},
		func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || !strings.Contains(res.Error, "exit") {
		t.Fatalf("exit ≠ 0 must yield failed: %+v", res)
	}
}

// TestClaudeCodeAdapterWorkDir secures the seam on which the sub-agent stands in
// the project checkout: cwd points into the project (only there does Claude Code
// find its CLAUDE.md, .claude/agents, skills), HOME stays the agent home (that
// is where ~/.claude, the wiki working copy and the caches live).
func TestClaudeCodeAdapterWorkDir(t *testing.T) {
	bin, home := fakeClaude(t, `
printf '%s\n' "$PWD" > "$HOME/cwd.txt"
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}
EOF`)
	project := filepath.Join(home, "repos", "p1-main")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &ClaudeCode{Binary: bin}
	if _, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t", Title: "Auftrag", HomeDir: home, WorkDir: project,
	}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "cwd.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// macOS: /var/… is a symlink to /private/var — suffix instead of equality.
	if cwd := strings.TrimSpace(string(got)); !strings.HasSuffix(cwd, filepath.Join("repos", "p1-main")) {
		t.Fatalf("cwd must lie inside the project checkout, was %q", cwd)
	}
}

// No runtime run inherits the daemon's COVEY_* variables — not even the outer
// one. Whatever a run needs, the caller passes explicitly (COVEY_ACTION_PORT,
// brokered key); nothing is inherited, otherwise the daemon token would be open
// to every subprocess.
func TestClaudeCodeAdapterDropsDaemonEnv(t *testing.T) {
	t.Setenv("COVEY_DAEMON_TOKEN", "daemon-jwt-geheim")
	t.Setenv("COVEY_AGENT_ID", "agent-1")
	bin, home := fakeClaude(t, `
printf 'token=%s agent=%s port=%s\n' "$COVEY_DAEMON_TOKEN" "$COVEY_AGENT_ID" "$COVEY_ACTION_PORT" > "$HOME/env.txt"
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig"}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	if _, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t", Title: "x", HomeDir: home, Env: []string{"COVEY_ACTION_PORT=4711"},
	}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "token= agent= port=4711" {
		t.Fatalf("the daemon environment must not be inherited, the action port must arrive: %q", got)
	}
}

// Without a WorkDir everything stays as before: the outer run starts in the home.
func TestClaudeCodeAdapterWorkDirDefaultsToHome(t *testing.T) {
	bin, home := fakeClaude(t, `
printf '%s\n' "$PWD" > "$HOME/cwd.txt"
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig"}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	if _, err := adapter.Run(context.Background(), RunSpec{TaskID: "t", Title: "x", HomeDir: home},
		func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "cwd.txt"))
	if cwd := strings.TrimSpace(string(got)); !strings.HasSuffix(cwd, filepath.Base(home)) {
		t.Fatalf("without a WorkDir the run must start in the home, was %q", cwd)
	}
}

// A task that went blocked while runs still started in the home has its
// session there. Resuming it from its scratch directory would make Claude Code
// look in the wrong project directory and lose the thread (#271).
func TestClaudeCodeAdapterResumeStartsWhereTheSessionLies(t *testing.T) {
	bin, home := fakeClaude(t, `
printf '%s\n' "$PWD" > "$HOME/cwd.txt"
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"sess-old","result":"fertig"}
EOF`)
	scratch := filepath.Join(home, "scratch", "25456bd1-83f7-487f-a7d2-2aa2f1a8dcea")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	session := func(cwd, id string) {
		dir := filepath.Join(home, ".claude", "projects", claudeProjectKey(cwd))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(id string) string {
		t.Helper()
		adapter := &ClaudeCode{Binary: bin}
		if _, err := adapter.Run(context.Background(), RunSpec{
			TaskID: "t", Title: "x", HomeDir: home, WorkDir: scratch,
			ResumeSessionID: id, ResumeInput: "weiter",
		}, func(string, json.RawMessage) {}); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(home, "cwd.txt"))
		return strings.TrimSpace(string(got))
	}

	session(home, "sess-old")
	if cwd := run("sess-old"); strings.Contains(cwd, "scratch") {
		t.Fatalf("a session recorded in the home must be resumed from the home, ran in %q", cwd)
	}

	session(scratch, "sess-new")
	if cwd := run("sess-new"); !strings.HasSuffix(cwd, filepath.Base(scratch)) {
		t.Fatalf("a session recorded in the scratch directory must stay there, ran in %q", cwd)
	}

	// Found nowhere: WorkDir, as for any run. Falling back to the home on a
	// guess would bring the heap back.
	if cwd := run("sess-unknown"); !strings.HasSuffix(cwd, filepath.Base(scratch)) {
		t.Fatalf("an unknown session must start in WorkDir, ran in %q", cwd)
	}
}

func TestClaudeProjectKey(t *testing.T) {
	for in, want := range map[string]string{
		"/home/agent":                    "-home-agent",
		"/home/agent/scratch/a1-b2":      "-home-agent-scratch-a1-b2",
		"/Users/x/Documents/Fun.Projekt": "-Users-x-Documents-Fun-Projekt",
		"/home/agent/prüfung_1":          "-home-agent-pr-fung-1",
	} {
		if got := claudeProjectKey(in); got != want {
			t.Fatalf("claudeProjectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The cached input side is the point of the billing: input_tokens counts only
// what did NOT come out of the prompt cache. With Claude Code that is a handful
// of tokens against millions of cache reads — a run booked without the two
// cache fields looks a thousand times cheaper than it is. The numbers are those
// of a real run of tester-1.
func TestClaudeCodeAdapterCountsCachedInputAndModel(t *testing.T) {
	bin, home := fakeClaude(t, `
cat <<'EOF'
{"type":"assistant","session_id":"s","message":{"model":"claude-opus-5","content":"arbeite"}}
{"type":"result","subtype":"success","session_id":"s","result":"fertig","total_cost_usd":2.39,"usage":{"input_tokens":56,"output_tokens":15804,"cache_read_input_tokens":2341568,"cache_creation_input_tokens":81936}}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	res, err := adapter.Run(context.Background(), RunSpec{TaskID: "t", Title: "x", HomeDir: home},
		func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.InputTokens != 56 || res.CacheReadTokens != 2341568 || res.CacheCreationTokens != 81936 {
		t.Fatalf("cached input side lost: %+v", res)
	}
	if got, want := res.TotalInputTokens(), int64(56+2341568+81936); got != want {
		t.Fatalf("TotalInputTokens = %d, want %d", got, want)
	}
	// The result event does not name the model — it has to be picked up from the
	// assistant events, otherwise every cost entry reads "unknown".
	if res.Model != "claude-opus-5" {
		t.Fatalf("model not recorded: %q", res.Model)
	}
}

// With an MCP config the action proxy is handed to the runtime as a tool server
// — and its tools have to stand in the SAME --allowedTools list. The flag is a
// whitelist and may appear only once; a second occurrence would overwrite the
// first, and the run would ask for permission on every action, which nobody
// answers in headless mode.
func TestClaudeCodeAdapterPassesMCPConfigInOneAllowlist(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig"}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	if _, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t", Title: "x", HomeDir: dir,
		AllowedTools: []string{"Bash", "Read"},
		MCPConfig:    `{"mcpServers":{"covey":{"type":"http","url":"http://127.0.0.1:4711/mcp"}}}`,
	}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "--mcp-config") || !strings.Contains(s, "127.0.0.1:4711/mcp") {
		t.Fatalf("the MCP server is missing from the arguments:\n%s", s)
	}
	if n := strings.Count(s, "--allowedTools"); n != 1 {
		t.Fatalf("--allowedTools must appear exactly once, appeared %d times:\n%s", n, s)
	}
	if !strings.Contains(s, "Bash,Read,mcp__covey") {
		t.Fatalf("the proxy's tools belong in the allowlist alongside the others:\n%s", s)
	}
}

// --tools carries the loading scope and must stay clean of MCP names: the flag
// knows only built-in tools. The MCP tool reaches the run through
// --allowedTools — that is the whole division of labour between the two flags.
func TestClaudeCodeAdapterLimitsBuiltinToolsWithoutMCPNames(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig"}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	if _, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t", Title: "x", HomeDir: dir,
		AllowedTools: []string{"Bash", "Read"},
		MCPConfig:    `{"mcpServers":{"covey":{"type":"http","url":"http://127.0.0.1:4711/mcp"}}}`,
	}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	s := string(got)
	if !strings.Contains(s, "--tools") {
		t.Fatalf("--tools is missing; without it every built-in tool stays in the prompt:\n%s", s)
	}
	// The line after --tools is the list itself (one argument per line).
	lines := strings.Split(s, "\n")
	var list string
	for i, l := range lines {
		if l == "--tools" && i+1 < len(lines) {
			list = lines[i+1]
		}
	}
	if list != "Bash,Read" {
		t.Fatalf("--tools should carry exactly the built-in names, got %q:\n%s", list, s)
	}
}

// Without materialized skills the Skill tool stays out — it would only drag the
// built-in skills' descriptions into every turn (+2.454 tokens, measured).
func TestClaudeCodeAdapterSkillToolFollowsMaterializedSkills(t *testing.T) {
	for _, tc := range []struct {
		name   string
		skills bool
		want   string
	}{
		{"ohne Skills", false, "Bash,Read"},
		{"mit Skills", true, "Bash,Read,Skill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig"}
EOF`)
			adapter := &ClaudeCode{Binary: bin}
			if _, err := adapter.Run(context.Background(), RunSpec{
				TaskID: "t", Title: "x", HomeDir: dir,
				AllowedTools: []string{"Bash", "Read"},
				Skills:       tc.skills,
			}, func(string, json.RawMessage) {}); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
			if !strings.Contains(string(got), "\n"+tc.want+"\n") {
				t.Fatalf("expected tool list %q:\n%s", tc.want, got)
			}
		})
	}
}

// Without an MCP config nothing changes — the shell route as before.
func TestClaudeCodeAdapterWithoutMCPStaysAsBefore(t *testing.T) {
	bin, dir := fakeClaude(t, `
cat <<'EOF'
{"type":"result","subtype":"success","session_id":"s","result":"fertig"}
EOF`)
	adapter := &ClaudeCode{Binary: bin}
	if _, err := adapter.Run(context.Background(), RunSpec{
		TaskID: "t", Title: "x", HomeDir: dir, AllowedTools: []string{"Bash"},
	}, func(string, json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if strings.Contains(string(got), "--mcp-config") || strings.Contains(string(got), "mcp__covey") {
		t.Fatalf("without an MCP config no MCP flags:\n%s", got)
	}
}

// fakeClaudeTurnLimit is a fake `claude` for the turn-limit path. The run ends
// at the turn limit; the handover turn (recognisable by --resume) runs the
// given script. Each call writes its arguments NUL-separated to main.args or
// summary.args — the system prompt spans lines, so one per line would not
// split them back reliably.
func fakeClaudeTurnLimit(t *testing.T, summary string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	content := "#!/bin/sh\n" +
		"if printf '%s\\n' \"$@\" | grep -qx -- '--resume'; then\n" +
		"printf '%s\\0' \"$@\" > \"" + dir + "/summary.args\"\n" + summary + "\n" +
		"else\n" +
		"printf '%s\\0' \"$@\" > \"" + dir + "/main.args\"\n" +
		"cat <<'EOF'\n" +
		`{"type":"result","subtype":"error_max_turns","session_id":"sess-limit","num_turns":3,"total_cost_usd":0.9}` + "\n" +
		"EOF\n" +
		"fi\n"
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

// flagValue returns the value after flag, "" without the flag.
func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// withoutFlags drops the given flags together with their values.
func withoutFlags(args []string, flags ...string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if slices.Contains(flags, args[i]) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// turnLimitSpec carries everything that ends up in the prompt prefix: appended
// system prompt with memory context, MCP config, allow-list, built-in tool set,
// model and effort.
func turnLimitSpec(home string) RunSpec {
	return RunSpec{
		TaskID: "t-limit", Title: "Big assignment", Body: "Fix the payment bug",
		SystemPrompt:  "You are the developer.\n\nYour workplace is the sandbox.",
		MemoryContext: "## Relevant from your wiki\n[[payments]]",
		MCPConfig:     `{"mcpServers":{"covey":{"command":"coveyd","args":["mcp"]}}}`,
		AllowedTools:  []string{"Bash", "Read", "Edit"},
		Model:         "claude-opus-4-8",
		Effort:        "high",
		MaxTurns:      3,
		HomeDir:       home,
	}
}

// The handover turn at the turn limit has to send the run's own prefix: the
// same appended system prompt, MCP config, tool lists, model and effort. Tools
// and system prompt stand at the head of the prompt cache; a turn that leaves
// them out misses every cached token after them and writes the whole
// conversation into the cache again. Only the prompt, --resume and the turn cap
// differ — and the tool guard, which is a setting, not part of the prompt.
func TestClaudeCodeSummaryReusesTheRunsPrefix(t *testing.T) {
	bin, dir := fakeClaudeTurnLimit(t, `cat <<'EOF'
{"type":"result","subtype":"success","session_id":"sess-limit","result":"## Done\nRepo cloned\n## Open\nThe fix\n## Next step\nEdit pay.go","total_cost_usd":0.05}
EOF`)
	adapter := &ClaudeCode{Binary: bin}

	res, err := adapter.Run(context.Background(), turnLimitSpec(dir), func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete" || !strings.Contains(res.Result, "Edit pay.go") {
		t.Fatalf("the handover state has to arrive as the result of an incomplete run: %+v", res)
	}

	main := readArgs(t, filepath.Join(dir, "main.args"))
	summary := readArgs(t, filepath.Join(dir, "summary.args"))
	for _, want := range []string{"--append-system-prompt", "--mcp-config", "--allowedTools", "--tools", "--model", "--effort"} {
		if flagValue(main, want) == "" {
			t.Fatalf("the spec is meant to put %s into the run's args:\n%q", want, main)
		}
	}
	strip := []string{"-p", "--resume", "--max-turns", "--settings"}
	if a, b := withoutFlags(main, strip...), withoutFlags(summary, strip...); !slices.Equal(a, b) {
		t.Fatalf("the handover turn has to send the run's prefix unchanged\nrun:     %q\nsummary: %q", a, b)
	}
	if got := flagValue(summary, "-p"); got != maxTurnsSummaryPrompt {
		t.Fatalf("the handover turn asks with the summary prompt, got %q", got)
	}
	if got := flagValue(summary, "--resume"); got != "sess-limit" {
		t.Fatalf("the handover turn resumes the aborted session, got %q", got)
	}
	if got := flagValue(summary, "--max-turns"); got != strconv.Itoa(maxTurnsSummaryTurns) {
		t.Fatalf("the handover turn runs with its own turn cap, got %q", got)
	}
	if got := flagValue(summary, "--settings"); got != summaryToolGuard {
		t.Fatalf("the handover turn carries the tool guard, got %q", got)
	}
}

// The guard refuses every tool call without touching the tool list: a
// PreToolUse hook for all tools that exits 2 (blocked, the message goes to the
// model). Run the hook's command for real, so that a quoting mistake in the JSON
// cannot hide behind a string comparison.
func TestClaudeCodeSummaryToolGuardRefusesEveryTool(t *testing.T) {
	var guard struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(summaryToolGuard), &guard); err != nil {
		t.Fatalf("the guard has to be valid settings JSON: %v", err)
	}
	if len(guard.Hooks) != 1 || len(guard.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("the guard is exactly one PreToolUse hook: %+v", guard)
	}
	entry := guard.Hooks["PreToolUse"][0]
	if entry.Matcher != "*" || len(entry.Hooks) != 1 || entry.Hooks[0].Type != "command" {
		t.Fatalf("the hook has to match every tool with one command: %+v", entry)
	}
	cmd := exec.Command("sh", "-c", entry.Hooks[0].Command)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"mcp__covey__run_action"}`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("the hook has to block with exit code 2, got %v", err)
	}
	if !strings.Contains(stderr.String(), "handover state") {
		t.Fatalf("the model has to learn why the call was refused: %q", stderr.String())
	}
}

// The model answers the summary prompt with a tool call anyway. The guard
// refuses it and the second turn the cap leaves is the handover — it arrives.
func TestClaudeCodeSummaryAfterARefusedToolCall(t *testing.T) {
	bin, dir := fakeClaudeTurnLimit(t, `cat <<'EOF'
{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"git status"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"No tools in the handover turn."}]}}
{"type":"result","subtype":"success","session_id":"sess-limit","num_turns":2,"result":"## Done\nHalf the fix\n## Open\nTests\n## Next step\nRun go test","total_cost_usd":0.06}
EOF`)
	res, err := (&ClaudeCode{Binary: bin}).Run(context.Background(), turnLimitSpec(dir), func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete" || !strings.Contains(res.Result, "Run go test") {
		t.Fatalf("the handover after a refused call has to arrive: %+v", res)
	}
	if res.CostUSD < 0.95 {
		t.Fatalf("the handover turn's cost belongs in the billing: %+v", res)
	}
}

// The model spends the whole cap on tool calls: no result text. The handover is
// lost, the run still reports incomplete with its session, so the control plane
// creates the follow-up task without a handover (best-effort, as before).
func TestClaudeCodeSummaryCapSpentOnTools(t *testing.T) {
	bin, dir := fakeClaudeTurnLimit(t, `cat <<'EOF'
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"Read","input":{}}]}}
{"type":"result","subtype":"error_max_turns","session_id":"sess-limit","num_turns":2,"is_error":true,"total_cost_usd":0.07}
EOF`)
	res, err := (&ClaudeCode{Binary: bin}).Run(context.Background(), turnLimitSpec(dir), func(string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete" || res.Result != "" || res.SessionID != "sess-limit" {
		t.Fatalf("without a handover the run stays incomplete with its session: %+v", res)
	}
	if !strings.Contains(res.Error, "turn limit reached (3 turns)") {
		t.Fatalf("the main run's error text has to stand: %q", res.Error)
	}
}
