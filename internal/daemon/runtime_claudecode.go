package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClaudeCode drives Claude Code headless through `claude -p` (spec/12): prompt
// in, stream-json events out, `--resume <session_id>` for the blocked→working
// edge.
type ClaudeCode struct {
	// Binary is the CLI path; default "claude", overridable via the ENV var
	// COVEY_CLAUDE_BIN (that is how we test the adapter against a fake binary).
	Binary string
	// CredentialHint rewrites a credential failure into the terms of the ENGINE
	// that uses this harness. Nil = Anthropic's own wording below, which is what
	// claude-code needs. An engine that drives the same binary against another
	// endpoint sets its own (educa-ai, spec/23): telling somebody to run
	// `claude setup-token` is wrong advice when the token comes from a gateway,
	// and wrong advice costs more time than none. Returning "" leaves the raw
	// text standing.
	CredentialHint func(raw string) string
}

// The CLI this adapter runs, and the variable that names another path for it.
// One place, read by the constructor below and declared in the descriptor, so
// that whoever asks the registry "what does this engine need in the sandbox?"
// gets the same answer the run uses.
const (
	claudeDefaultBinary = "claude"
	claudeBinEnv        = "COVEY_CLAUDE_BIN"
)

func NewClaudeCode() *ClaudeCode {
	bin := os.Getenv(claudeBinEnv)
	if bin == "" {
		bin = claudeDefaultBinary
	}
	return &ClaudeCode{Binary: bin}
}

func init() {
	RegisterRuntime(RuntimeDescriptor{
		Name:        "claude-code",
		Label:       "Claude Code",
		Description: "Real Claude Code sandbox (claude -p, headless). Needs an Anthropic credential under Secrets.",
		Credentials: []RuntimeCredential{
			// API key before subscription token: whoever has both should use
			// the one they are billed for on purpose, not by accident.
			{Kind: CredAPIKey, Label: "API key",
				Secret: "anthropic_api_key", EnvVar: "ANTHROPIC_API_KEY"},
			{Kind: CredSubscription, Label: "Subscription",
				Secret: "claude_code_oauth_token", EnvVar: "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		CLI: RuntimeCLI{Name: claudeDefaultBinary, Env: claudeBinEnv},
		Capabilities: RuntimeCapabilities{Resume: true, SkillsDir: ".claude/skills",
			// The levels of `--effort`, in the binary's own spelling and order.
			// They are declared here rather than in the HTTP layer so that an
			// agent on another engine is not offered a control its engine does
			// not have (spec/12).
			EffortLevels: []string{"low", "medium", "high", "xhigh", "max"},
			ResultUsage:  true},
		New: func() Runtime { return NewClaudeCode() },
		Setup: []SetupStep{
			{
				Text: "Obtain a credential — one of the two variants:",
				Items: []string{
					"Subscription (Pro/Max): run `claude setup-token` in a terminal → token with the prefix `sk-ant-oat…`. Uses your subscription quota, no separate billing.",
					"API (pay-per-token): API key `sk-ant-api…` from the Anthropic console. Billed separately.",
				},
			},
			{
				Text: "Store it under `Secrets` — key depending on the variant:",
				Items: []string{
					"Subscription token → key `claude_code_oauth_token`",
					"API key → key `anthropic_api_key`",
				},
			},
			{Text: "Set this agent's runtime to `claude-code`."},
			{Text: "Put a task into the backlog or `Wake` the agent — the control plane brokers the credential into the sandbox at runtime, short-lived."},
		},
	})
}

func (c *ClaudeCode) Name() string { return "claude-code" }

// streamEvent is the generic view of a stream-json line, as decodeStreamEvent
// reads it.
type streamEvent struct {
	Type         string
	Subtype      string
	SessionID    string
	Result       string
	IsError      bool
	Errors       []string
	NumTurns     int
	TotalCostUSD float64
	// TerminalReason names why the run ended ("completed", "max_turns",
	// "api_error", …). For the turn limit it is the more reliable source than
	// subtype, because there only error_max_turns arrives.
	TerminalReason string
	// Usage: input_tokens counts ONLY the uncached input. With Claude Code
	// practically the entire context comes out of the prompt cache, so that
	// field stays in the low three digits while the run reads millions of
	// tokens. Without the two cache fields the input side of the billing is
	// off by three orders of magnitude — measured on a single run: 56
	// input_tokens against 2,341,568 cache_read_input_tokens.
	Usage resultUsage
	// Model comes from message.model on the assistant events. The result event
	// does NOT name it, which is why cost entries used to end up with an empty
	// model and the cost breakdown reported everything as "unknown".
	Model string
	// SinceLast (model_usage_since_last_result) is what happened since the
	// previous result line of the session — this invocation's own spend, over
	// every model, the side calls to a smaller model included (Usage above
	// names the main model only). Booking is done from it; nil where the line
	// has no such field (an older CLI). The line's modelUsage is the
	// session's running total and is not read here: the control plane files
	// it per model (observability.RecordRuntime), the daemon books deltas.
	SinceLast map[string]ccModelUsage
}

// resultUsage is a result line's top-level usage: the main model's, per
// invocation.
type resultUsage struct {
	InputTokens         lenientInt `json:"input_tokens"`
	OutputTokens        lenientInt `json:"output_tokens"`
	CacheReadTokens     lenientInt `json:"cache_read_input_tokens"`
	CacheCreationTokens lenientInt `json:"cache_creation_input_tokens"`
}

// ccModelUsage is one model's entry in model_usage_since_last_result. The
// thinking tokens are already inside OutputTokens (list price × outputTokens
// reproduces costUSD to the last digit), so they are not read separately —
// adding them would price thinking twice.
type ccModelUsage struct {
	CostUSD              lenientFloat `json:"costUSD"`
	InputTokens          lenientInt   `json:"inputTokens"`
	OutputTokens         lenientInt   `json:"outputTokens"`
	CacheReadInputTokens lenientInt   `json:"cacheReadInputTokens"`
	// The cache write split by lifetime (the two are priced differently), and
	// the plain sum, which is all an older CLI sends.
	CacheCreation5mInputTokens lenientInt `json:"cacheCreationEphemeral5mInputTokens"`
	CacheCreation1hInputTokens lenientInt `json:"cacheCreationEphemeral1hInputTokens"`
	CacheCreationInputTokens   lenientInt `json:"cacheCreationInputTokens"`
	WebSearchRequests          lenientInt `json:"webSearchRequests"`
}

// cacheCreation is the entry's cache write: 5 min + 1 h, or the plain sum where
// the split is missing.
func (m ccModelUsage) cacheCreation() int64 {
	if split := m.CacheCreation5mInputTokens + m.CacheCreation1hInputTokens; split > 0 {
		return int64(split)
	}
	return int64(m.CacheCreationInputTokens)
}

// lenientInt and lenientFloat read a figure of a result line the way the
// control plane's SQL does (observability's jsonInt): a JSON number, whole or
// not (12.0, 1.2e3), counts; anything else — a string, null, an object, a
// number no int64 holds — is 0, never an error. The line is the engine's to
// shape, and one odd figure must not cost the run its status or its booking:
// with plain int64 fields a single 12.0 made the whole result line undecodable.
type (
	lenientInt   int64
	lenientFloat float64
)

func (n *lenientInt) UnmarshalJSON(b []byte) error {
	f := math.Round(float64(jsonNumber(b)))
	if math.Abs(f) >= 1<<63 {
		f = 0
	}
	*n = lenientInt(f)
	return nil
}

func (f *lenientFloat) UnmarshalJSON(b []byte) error {
	*f = jsonNumber(b)
	return nil
}

// jsonNumber is b as a JSON number, 0 for anything else.
func jsonNumber(b []byte) lenientFloat {
	var f float64
	if json.Unmarshal(b, &f) != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0
	}
	return lenientFloat(f)
}

// decodeStreamEvent reads a stream-json line field by field. ok is false only
// for a line that is not a JSON object. Each field is decoded on its own and a
// field of an unexpected shape stays at its zero value: the status fields
// (type, subtype, result, …) must survive whatever the usage figures look
// like, because a result line that fails to decode is a run without a result —
// no status, no handover, no booking.
func decodeStreamEvent(line []byte) (ev streamEvent, ok bool) {
	var f map[string]json.RawMessage
	if json.Unmarshal(line, &f) != nil || f == nil {
		return ev, false
	}
	ev.Type = jsonField[string](f, "type")
	ev.Subtype = jsonField[string](f, "subtype")
	ev.SessionID = jsonField[string](f, "session_id")
	ev.Result = jsonField[string](f, "result")
	ev.IsError = jsonField[bool](f, "is_error")
	ev.Errors = jsonField[[]string](f, "errors")
	ev.NumTurns = int(jsonField[lenientInt](f, "num_turns"))
	ev.TotalCostUSD = float64(jsonField[lenientFloat](f, "total_cost_usd"))
	ev.TerminalReason = jsonField[string](f, "terminal_reason")
	ev.Usage = jsonField[resultUsage](f, "usage")
	ev.Model = jsonField[struct {
		Model string `json:"model"`
	}](f, "message").Model
	if raw, ok := f["model_usage_since_last_result"]; ok {
		ev.SinceLast = decodeModelUsage(raw)
	}
	return ev, true
}

// jsonField decodes one field; a missing field or one of another shape is the
// zero value.
func jsonField[T any](f map[string]json.RawMessage, key string) T {
	var v T
	if raw, ok := f[key]; ok && json.Unmarshal(raw, &v) != nil {
		var zero T
		return zero
	}
	return v
}

// decodeModelUsage reads a per-model map entry by entry: an entry that is not
// an object is skipped, the others keep their figures. nil when the field is
// not an object (null included) — the line is then read like one from a CLI
// without the field.
func decodeModelUsage(raw json.RawMessage) map[string]ccModelUsage {
	var entries map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil
	}
	out := make(map[string]ccModelUsage, len(entries))
	for model, e := range entries {
		var m ccModelUsage
		if json.Unmarshal(e, &m) == nil {
			out[model] = m
		}
	}
	return out
}

// tokenSums is the token side of a process over all models.
type tokenSums struct {
	input         int64
	output        int64
	cacheRead     int64
	cacheCreation int64
}

// outcome is the raw result of a single `claude` process, before it is mapped
// onto a RunResult.
type outcome struct {
	sessionID  string
	resultText string
	subtype    string
	terminal   string
	model      string
	numTurns   int
	isError    bool
	errors     []string
	// deltaUSD and tokens are what THIS process spent, summed over all of its
	// result lines — never a session's running total (see addResult).
	deltaUSD float64
	tokens   tokenSums
	// lastTotalUSD is the session's running total after the last result line
	// (or the baseline handed in): what the next result line of the session
	// is measured against, and the cross-check sent along with the cost.
	lastTotalUSD float64
	// haveBaseline says lastTotalUSD is known — 0 for a fresh session, handed
	// in for the handover, and after every result line.
	haveBaseline bool
	// basis is CostBasisInvocation, or CostBasisCumulative when a line could
	// only be booked as the session's whole running total (see addResult).
	basis     string
	sawResult bool
	waitErr   error
}

// truncated recognizes the run that ended at the turn limit: Claude Code then
// reports subtype=error_max_turns or terminal_reason=max_turns — and, unlike
// with any other error, **no** result field. Without special handling such a
// run lands in the backlog as "failed", with no error text and no interim state.
func (o outcome) truncated() bool {
	return o.subtype == "error_max_turns" || o.terminal == "max_turns"
}

// maxTurnsSummaryPrompt fetches the interim state from the aborted session.
// Deliberately terse: one answer against the already cached context, and cheap
// compared to the run that would otherwise be lost — but only as long as the
// handover turn sends the same prefix as the run (see summaryArgs). A first
// version resumed with nothing but --resume and --model, so Claude Code sent
// its default tool set instead of the run's --tools; the system prompt was
// already identical (checked with CLI 2.1.325), but the changed tools broke the
// prefix and every cached token after them missed. The turn then wrote the
// whole conversation into the cache again (measured on 84f8f55e: 63,276
// cache-write tokens, 0.53 USD for a single turn). Its cost belongs in the
// billing either way, hence summarize() adds the turn's own spend to res.
const maxTurnsSummaryPrompt = `Your run was cut off at the turn limit — you are NOT finished.
Do not continue working now and do not call any tools. Answer only with a short
handover state (max. 15 lines, Markdown) in exactly this structure:

## Done
## Open
## Next step

Write it so that a colleague without your context can carry on from it: concrete
names (issue, MR, branch, file, path), no hints.`

// maxTurnsSummaryTimeout caps the summary run. It is one answer without tool
// use; if it takes longer, something is broken and the task should rather fail
// without a handover than occupy the sandbox. A variable only so that a test
// can run into it.
var maxTurnsSummaryTimeout = 2 * time.Minute

// maxTurnsSummaryTurns leaves room for one refused tool call. The handover turn
// carries the run's tools (they are part of the cached prefix), so the model can
// answer the summary prompt with a tool call despite being told not to. The
// guard below refuses it, and the second turn is the answer. With a cap of 1 the
// refused call alone would end the turn as error_max_turns, without a result.
const maxTurnsSummaryTurns = 2

// summaryToolGuard refuses every tool call in the handover turn: a PreToolUse
// hook for all tools (built-in and MCP) that exits 2, which blocks the call and
// hands the message to the model. It goes in through --settings because hooks
// are not part of the prompt — unlike --tools or --disallowedTools, which take
// the tools out of the request and with them the cache prefix of the whole
// conversation.
const summaryToolGuard = `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command",` +
	`"command":"echo 'No tools in the handover turn. Answer only with the handover state.' >&2; exit 2"}]}]}}`

func (c *ClaudeCode) buildArgs(spec RunSpec) ([]string, string) {
	prompt := spec.Title + "\n\n" + spec.Body
	if spec.ResumeSessionID != "" {
		// Resumption: Claude Code restores the context itself, we only feed in
		// the correlated event as the new input.
		prompt = spec.ResumeInput
		if prompt == "" {
			prompt = "The event you were waiting for has occurred. Continue the task."
		}
	}
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions"}
	if spec.ResumeSessionID != "" {
		args = append(args, "--resume", spec.ResumeSessionID)
	}
	systemPrompt := spec.SystemPrompt
	if spec.MemoryContext != "" {
		systemPrompt += "\n\n" + spec.MemoryContext
	}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	// The action proxy as an MCP server. Its tools have to stand in the SAME
	// --allowedTools list: the flag is a whitelist, it may appear only once, and
	// without the entry the run would ask for permission on every action — which
	// nobody answers in headless mode.
	allowed := spec.AllowedTools
	if spec.MCPConfig != "" {
		args = append(args, "--mcp-config", spec.MCPConfig)
		allowed = append(append([]string{}, allowed...), "mcp__covey")
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	// --tools next to --allowedTools, and the two are not the same thing:
	// --allowedTools decides what the run MAY use, --tools what EXISTS at all.
	// Only the latter keeps a tool's schema out of the prompt — and the prompt
	// is read afresh on every turn, so it is the schemas, not the calls, that
	// carry the cost. Without this flag a run drags the runtime's whole built-in
	// set along (Cron*, Workflow, ScheduleWakeup, RemoteTrigger, Task*, … — none
	// of which a covey agent ever touches).
	//
	// MCP tools stay out of it: --tools knows only built-in names, and the
	// action proxy reaches the run through --allowedTools.
	if builtin := BuiltinTools(spec.AllowedTools, spec.Skills); len(builtin) > 0 {
		args = append(args, "--tools", strings.Join(builtin, ","))
	}
	if spec.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(spec.MaxTurns))
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	return args, prompt
}

// runDir is the working directory of the `claude` process: WorkDir, or the home
// without one.
//
// With one exception. Claude Code keeps a session's transcript per working
// directory, and `--resume <id>` looks only in the directory of the cwd it is
// started in. A task that went blocked while runs still started in the home
// (before #271) has its session there; resuming it from its scratch directory
// would find nothing and lose the thread. Such a resume starts where its session
// lies.
func runDir(spec RunSpec) string {
	if spec.WorkDir == "" {
		return spec.HomeDir
	}
	if spec.ResumeSessionID != "" &&
		!hasSession(spec.HomeDir, spec.WorkDir, spec.ResumeSessionID) &&
		hasSession(spec.HomeDir, spec.HomeDir, spec.ResumeSessionID) {
		return spec.HomeDir
	}
	return spec.WorkDir
}

// hasSession reports whether Claude Code holds the transcript of session id for
// runs started in cwd: ~/.claude/projects/<cwd>/<id>.jsonl, where <cwd> is the
// path with every character that is not an ASCII letter or digit replaced by
// "-". Both the path as given and its resolved form are tried, because the
// process sees the resolved one (macOS: /var is /private/var).
func hasSession(home, cwd, id string) bool {
	if home == "" || cwd == "" || id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	candidates := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		candidates = append(candidates, real)
	}
	for _, c := range candidates {
		path := filepath.Join(home, ".claude", "projects", claudeProjectKey(c), id+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// claudeProjectKey replaces per character, not per byte: Claude Code does it on
// a JavaScript string, where "ü" is one character and becomes one "-".
func claudeProjectKey(cwd string) string {
	var b strings.Builder
	for _, ch := range cwd {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// stream starts a `claude` process, passes every stream-json line 1:1 into the
// recording and collects the raw result from it.
//
// baseline is the session's total_cost_usd before this process: 0 for a fresh
// session, the main run's last total for the handover, nil when it is not known
// (a resume from an earlier task). Only a CLI without
// model_usage_since_last_result needs it, see addResult.
func (c *ClaudeCode) stream(ctx context.Context, spec RunSpec, args []string, baseline *float64, onEvent func(kind string, payload json.RawMessage)) (outcome, error) {
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	// Working directory and home are separate: Claude Code looks for CLAUDE.md,
	// .claude/agents, skills and commands relative to the cwd — that way a
	// sub-run in the project checkout gets its harness while keeping the agent
	// home.
	cmd.Dir = runDir(spec)
	// Environment without the daemon's COVEY_* variables (see childEnv): the run
	// gets only what the caller explicitly hands it — otherwise a hook or MCP
	// server from the project would inherit the daemon token.
	cmd.Env = childEnv(spec.Env...)
	cmd.Env = append(cmd.Env, "HOME="+spec.HomeDir)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return outcome{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return outcome{}, fmt.Errorf("starting claude: %w", err)
	}

	out := outcome{basis: CostBasisInvocation}
	if baseline != nil {
		out.lastTotalUSD, out.haveBaseline = *baseline, true
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Every stream-json line goes out 1:1 as a recording event.
		raw := json.RawMessage(append([]byte(nil), line...))
		onEvent("runtime", raw)

		ev, ok := decodeStreamEvent(line)
		if !ok {
			continue // tolerate non-JSON lines, the recording has them anyway
		}
		if ev.SessionID != "" {
			out.sessionID = ev.SessionID
		}
		// The model is only named on the way past (system/init and every
		// assistant event), never on the result. Whoever wants it has to pick it
		// up here.
		if ev.Model != "" {
			out.model = ev.Model
		}
		if ev.Type == "result" {
			// The status fields are the LAST line's: a process can write more
			// than one result line (a run woken again after its turn limit),
			// and what it ended with is the later one.
			out.sawResult = true
			out.resultText = ev.Result
			out.subtype = ev.Subtype
			out.terminal = ev.TerminalReason
			out.numTurns = ev.NumTurns
			out.isError = ev.IsError
			out.errors = ev.Errors
			// Money and tokens are summed over EVERY line, each contributing
			// only what is new since the line before.
			out.addResult(ev)
		}
	}
	out.waitErr = cmd.Wait()
	if scanErr := scanner.Err(); scanErr != nil && out.waitErr == nil {
		out.waitErr = scanErr
	}
	return out, nil
}

// addResult books one result line's own share of the spend.
//
// total_cost_usd and modelUsage are the SESSION's running totals, carried
// across --resume. Booked as they stand, a resumed session is paid again for
// everything before it, and the handover after a turn limit counts the main
// run a second time — ×1.95 over one evening's runs.
//
// With model_usage_since_last_result (CLI 2.1.x) the line says its own share:
// the delta per model, over all models, money and tokens alike. Summed over a
// session's lines it equals the last line's total exactly.
//
// Without it (an older CLI) the money is the difference of total_cost_usd to
// the session's total before the line, and the tokens are the line's usage —
// per invocation already, but the main model's only. The difference needs the
// total before; the first line of a resumed process whose caller did not know
// it has none and books the whole running total. That overcounts, and it is
// said rather than hidden: basis becomes CostBasisCumulative.
func (o *outcome) addResult(ev streamEvent) {
	if ev.SinceLast != nil {
		for _, m := range ev.SinceLast {
			o.deltaUSD += float64(m.CostUSD)
			o.tokens.input += int64(m.InputTokens)
			o.tokens.output += int64(m.OutputTokens)
			o.tokens.cacheRead += int64(m.CacheReadInputTokens)
			o.tokens.cacheCreation += m.cacheCreation()
		}
	} else {
		switch {
		case !o.haveBaseline:
			o.deltaUSD += ev.TotalCostUSD
			o.basis = CostBasisCumulative
		case ev.TotalCostUSD >= o.lastTotalUSD:
			o.deltaUSD += ev.TotalCostUSD - o.lastTotalUSD
		default:
			// Below the baseline: this total does not carry the session's
			// history (a CLI from before the running total), so it is the
			// process's own figure.
			o.deltaUSD += ev.TotalCostUSD
		}
		o.tokens.input += int64(ev.Usage.InputTokens)
		o.tokens.output += int64(ev.Usage.OutputTokens)
		o.tokens.cacheRead += int64(ev.Usage.CacheReadTokens)
		o.tokens.cacheCreation += int64(ev.Usage.CacheCreationTokens)
	}
	o.lastTotalUSD, o.haveBaseline = ev.TotalCostUSD, true
}

func (c *ClaudeCode) Run(ctx context.Context, spec RunSpec, onEvent func(kind string, payload json.RawMessage)) (RunResult, error) {
	args, _ := c.buildArgs(spec)
	// A fresh session starts at 0. A resumed one carries a total from runs this
	// process knows nothing about — nil, and the lines' own deltas decide.
	var baseline *float64
	if spec.ResumeSessionID == "" {
		baseline = new(float64)
	}
	out, err := c.stream(ctx, spec, args, baseline, onEvent)
	if err != nil {
		return RunResult{}, err
	}

	res := RunResult{
		SessionID: out.sessionID,
		// This run's own spend over all models (see addResult). The model
		// stays the main one: one entry per run.
		CostUSD:             out.deltaUSD,
		InputTokens:         out.tokens.input,
		OutputTokens:        out.tokens.output,
		CacheReadTokens:     out.tokens.cacheRead,
		CacheCreationTokens: out.tokens.cacheCreation,
		Model:               out.model,
		Basis:               out.basis,
		SessionTotalUSD:     out.lastTotalUSD,
	}
	resultText := out.resultText
	if out.isError {
		res.Status = "failed"
		// Not every error subtype fills result — then errors[] carries the text.
		res.Error = out.resultText
		if res.Error == "" {
			res.Error = strings.Join(out.errors, "; ")
		}
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	// Turn limit: no result, but a half-finished run. Instead of letting it fail
	// silently we fetch the handover state from the session and report it as
	// "incomplete" — the control plane turns that into a follow-up task that
	// carries on exactly here (spec/03).
	if out.truncated() {
		res.Status = "incomplete"
		res.Error = fmt.Sprintf("turn limit reached (%d turns) — run cut off before it produced a result", out.numTurns)
		res.Result = c.summarize(ctx, spec, out, onEvent, &res)
		return res, nil
	}
	if out.waitErr != nil && !out.sawResult {
		// Exit ≠ 0 without a result event: the hard failure path (spec/12).
		res.Status = "failed"
		res.Error = fmt.Sprintf("claude exit: %v", out.waitErr)
		return res, nil
	}
	if res.Status == "failed" {
		if c.CredentialHint != nil {
			// The engine on top of this harness words its own credential
			// failures; the Anthropic-specific rewrites below do not apply to it.
			if hint := c.CredentialHint(res.Error); hint != "" {
				res.Error = hint
			}
			return res, nil
		}
		// "Not logged in · Please run /login" means: no credential arrived in the
		// sandbox. Untranslated this reads like a covey login problem — hence the
		// actionable hint here.
		if strings.Contains(res.Error, "/login") {
			res.Error = fmt.Sprintf(
				"Claude Code has no credential in the sandbox (%q). "+
					"Store `anthropic_api_key` (API key) or `claude_code_oauth_token` "+
					"(subscription, via `claude setup-token`) in covey under Secrets.",
				res.Error)
		}
		// "Invalid bearer token" (401): the brokered subscription token has
		// expired or been revoked — untranslated that sounds like a covey bug.
		if strings.Contains(res.Error, "Invalid bearer token") ||
			strings.Contains(res.Error, "OAuth token has expired") {
			res.Error = fmt.Sprintf(
				"The stored subscription token is rejected by the Anthropic API (%q). "+
					"Subscription tokens expire and are revoked on a new login — run "+
					"`claude setup-token` in a terminal and save the new token in covey under "+
					"Secrets as `claude_code_oauth_token`.",
				res.Error)
		}
		return res, nil
	}
	applyStatus(&res, resultText)
	return res, nil
}

// summaryArgs builds the handover turn from the run's own arguments: the same
// appended system prompt, MCP config, tool lists, model and effort, so that the
// request begins with exactly the prefix the run left in the prompt cache. Only
// the prompt, --resume and the turn cap differ; the tool guard comes on top as a
// setting, outside the prompt.
func (c *ClaudeCode) summaryArgs(spec RunSpec, sessionID string) []string {
	s := spec
	s.ResumeSessionID = sessionID
	s.ResumeInput = maxTurnsSummaryPrompt
	s.MaxTurns = maxTurnsSummaryTurns
	args, _ := c.buildArgs(s)
	return append(args, "--settings", summaryToolGuard)
}

// summarize asks the session cut off at the turn limit for its handover state:
// one answer via --resume on the already cached context, tool calls refused.
// The result becomes the task's interim state and the brief of its follow-up
// task.
//
// Best-effort: if the extra run fails (no session ID, timeout, error, the turn
// cap spent on tool calls), the main run's error text stands — the follow-up
// task is then created without a handover, but it is created. What the extra
// run spent is added to the result so that billing stays complete — its OWN
// spend only: the session's running total already contains the main run, and
// the main run is booked.
//
// It runs with the run's spec (summaryArgs), so in the run's directory (runDir)
// and with its environment: --resume finds the session where the run left it.
func (c *ClaudeCode) summarize(ctx context.Context, spec RunSpec, main outcome, onEvent func(kind string, payload json.RawMessage), res *RunResult) string {
	if main.sessionID == "" {
		return ""
	}
	sspec := spec
	sspec.ResumeSessionID = main.sessionID
	sspec.ResumeInput = maxTurnsSummaryPrompt
	sspec.MaxTurns = maxTurnsSummaryTurns
	args := c.summaryArgs(spec, main.sessionID)
	var baseline *float64
	if main.haveBaseline {
		baseline = &main.lastTotalUSD
	}
	sctx, cancel := context.WithTimeout(ctx, maxTurnsSummaryTimeout)
	defer cancel()

	out, err := c.stream(sctx, sspec, args, baseline, onEvent)
	res.CostUSD += out.deltaUSD
	res.InputTokens += out.tokens.input
	res.OutputTokens += out.tokens.output
	res.CacheReadTokens += out.tokens.cacheRead
	res.CacheCreationTokens += out.tokens.cacheCreation
	if out.basis == CostBasisCumulative {
		res.Basis = CostBasisCumulative
	}
	if out.sawResult {
		res.SessionTotalUSD = out.lastTotalUSD
	}
	if res.Model == "" {
		res.Model = out.model
	}
	if err != nil || out.isError {
		return ""
	}
	return strings.TrimSpace(out.resultText)
}
