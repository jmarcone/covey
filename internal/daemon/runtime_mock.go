package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mock is a scriptable runtime for tests and offline demos. It is driven by
// directives in the task body and uses the same action proxy as a real runtime
// — the vertical slice (broker, guard-rails, recording, blocked loop) is thus
// exercised completely, only without an LLM.
//
// Directives (one per line):
//
//	[mock:action <system>/<action> <json-params>]  → action through the action proxy
//	[mock:block key=<correlation-key> question=<text>]
//	[mock:fail <error-text>]
//	[mock:result <text>]
//	[mock:memory <text>]
//	[mock:reply <text>]                      → the text above the status line (#650)
//	[mock:maxturns <handover-state>]         → run ends at the turn limit
//	[mock:maxturns-always <handover-state>]  → likewise, on every resumption too
//	[mock:prompt]                            → returns the system prompt as the result
//	[mock:session]                           → returns how the run was started (prompt, dir, resume) as JSON
//	[mock:line <json>]                       → emits <json> as a runtime line, verbatim
//	[mock:cost <json>]                       → the run's cost message (daemon.Cost fields)
//
// line and cost replay what a real engine sends: a Claude Code result line
// goes through the control plane's filing exactly as recorded, and a cost
// without "basis" is what a daemon from before that field sent.
//
// Without directives: done with a generic result. On resume: done with the
// resume input as the result. If the action proxy answers pending_approval, the
// mock runtime goes blocked with its correlation_key — as instructed.
type Mock struct{}

func (Mock) Name() string { return "mock" }

// MockUsageText is what the mock answers when asked for its utilisation: the
// shape of Claude Code's `/usage`, so the whole path — request, the parser,
// the report, the filing — runs as it does for the real engine. The mock
// itself needs no credential and is never asked; an engine built on it that
// declares one is (the integration tests do).
const MockUsageText = `Current session: 4% used · resets 6:59pm (Europe/Berlin)
Current week (all models): 13% used · resets Nov 20, 4:59pm (Europe/Berlin)`

// Usage answers MockUsageText — no provider, no rate limit.
func (Mock) Usage(ctx context.Context, env []string) (Usage, error) {
	return ParseUsage(MockUsageText), nil
}

func init() {
	RegisterRuntime(RuntimeDescriptor{
		Name:        "mock",
		Label:       "Mock",
		Description: "Scriptable test runtime without a real LLM — no cost, no credentials. For demos and offline tests.",
		// No credentials: the mock needs none, and NeedsCredential() derives
		// from that rather than being declared a second time.
		//
		// The mock stands in for Claude Code and therefore declares the same
		// effort levels — otherwise the effort path in tests would run past an
		// engine that does not offer it at all.
		Capabilities: RuntimeCapabilities{Resume: true, SkillsDir: ".claude/skills",
			EffortLevels: []string{"low", "medium", "high", "xhigh", "max"},
			// And it is booked the way Claude Code is, so that the cost path
			// can be driven with recorded result lines (mock:line, mock:cost).
			ResultUsage: true},
		New: func() Runtime { return Mock{} },
		Setup: []SetupStep{
			{Text: "Set this agent's runtime to `mock`."},
			{Text: "Switch to the agent (`Agents` → agent) and put a task into its `Backlog`."},
			{Text: "The agent runs through with a scripted answer — no credential, no cost, no `/login` error."},
		},
	})
}

// The argument may carry ONE level of square brackets — otherwise no call
// could pass a JSON array, and the directive would break off in the middle of
// the parameter. A bare `]` still gets the group no further, so two directives
// on one line stay separate.
var mockDirective = regexp.MustCompile(
	`\[mock:(action|block|fail|result|memory|reply|sleep|maxturns-always|maxturns|prompt|session|line|cost)\s*((?:[^\[\]]|\[[^\]]*\])*)\]`)

// Run is run plus the one thing a real engine leaves behind: the session's
// transcript, here a marker per working directory under the home, so that a
// sticky resume (#130) finds the session exactly where Claude Code would —
// and does not find it after a run in another directory or another home.
func (m Mock) Run(ctx context.Context, spec RunSpec, onEvent func(kind string, payload json.RawMessage)) (RunResult, error) {
	res, err := m.run(ctx, spec, onEvent)
	if res.SessionID != "" {
		if path := mockSessionPath(spec, res.SessionID); path != "" {
			if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
				_ = os.WriteFile(path, nil, 0o644)
			}
		}
	}
	return res, err
}

// HasSession: the marker Run left for this home and working directory.
func (Mock) HasSession(spec RunSpec, id string) bool {
	path := mockSessionPath(spec, id)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

var mockSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// mockSessionPath mirrors Claude Code's layout: ~/.mock/sessions/<cwd>/<id>.
func mockSessionPath(spec RunSpec, id string) string {
	cwd := spec.WorkDir
	if cwd == "" {
		cwd = spec.HomeDir
	}
	// The id comes from the run (a task body can set it through mock:line),
	// so it becomes a path element only in the one plain form an engine's
	// session id takes.
	if spec.HomeDir == "" || !mockSessionIDPattern.MatchString(id) || strings.Contains(id, "..") {
		return ""
	}
	return filepath.Join(spec.HomeDir, ".mock", "sessions", claudeProjectKey(cwd), id)
}

func (Mock) run(ctx context.Context, spec RunSpec, onEvent func(kind string, payload json.RawMessage)) (RunResult, error) {
	res := RunResult{
		Status:    "done",
		SessionID: "mock-session-" + spec.TaskID,
		CostUSD:   0.0123,
		// Deliberately with the cache share dominating, as in a real run — that
		// way the integration tests fail if the cached input side is dropped
		// somewhere along the way again.
		InputTokens:         1000,
		OutputTokens:        200,
		CacheReadTokens:     40000,
		CacheCreationTokens: 3000,
		Model:               "mock-model",
		Basis:               CostBasisInvocation,
	}
	if spec.ResumeSessionID != "" {
		res.SessionID = spec.ResumeSessionID
	}
	emit := func(text string) {
		payload, _ := json.Marshal(map[string]string{"type": "assistant", "text": text})
		onEvent("runtime", payload)
	}
	emit("Task accepted: " + spec.Title)
	if strings.TrimSpace(spec.MemoryContext) != "" {
		emit("Memory context is available.")
	}
	if spec.ResumeSessionID != "" {
		emit("Resuming session " + spec.ResumeSessionID + ": " + spec.ResumeInput)
	}

	actionPort := os.Getenv("COVEY_ACTION_PORT")
	for _, env := range spec.Env {
		if v, ok := strings.CutPrefix(env, "COVEY_ACTION_PORT="); ok {
			actionPort = v
		}
	}

	var notices []string
	for _, m := range mockDirective.FindAllStringSubmatch(spec.Body, -1) {
		kind, arg := m[1], strings.TrimSpace(m[2])
		switch kind {
		case "action":
			path, params, _ := strings.Cut(arg, " ")
			if params == "" {
				params = "{}"
			}
			status, body, err := callActionProxy(ctx, actionPort, path, params)
			if err != nil {
				res.Status = "failed"
				res.Error = fmt.Sprintf("action %s: %v", path, err)
				return res, nil
			}
			emit(fmt.Sprintf("Action %s → %s", path, status))
			var answered struct {
				Data struct {
					Notice string `json:"notice"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &answered) == nil && answered.Data.Notice != "" {
				// A notice is what a real runtime would read and weigh; the mock
				// carries it into its result so that a test can see it arrived.
				notices = append(notices, answered.Data.Notice)
			}
			if status == "pending_approval" {
				// As in the platform protocol: block until approval.
				var pending struct {
					CorrelationKey string `json:"correlation_key"`
				}
				json.Unmarshal(body, &pending)
				res.Status = "blocked"
				res.CorrelationKey = pending.CorrelationKey
				res.Question = "Waiting for human approval for " + path
				return res, nil
			}
			if status == "denied" {
				emit("Action forbidden by a guard-rail, choosing another way.")
			}
			if status == "error" {
				// Execution errors (unknown system, credential denied …) are a
				// hard failure — just like with a real runtime.
				var e struct {
					Error string `json:"error"`
				}
				json.Unmarshal(body, &e)
				res.Status = "failed"
				res.Error = fmt.Sprintf("action %s: %s", path, e.Error)
				return res, nil
			}
		case "block":
			if spec.ResumeSessionID != "" {
				continue // do not block again on resume
			}
			res.Status = "blocked"
			res.CorrelationKey = mockKV(arg, "key")
			res.Question = mockKV(arg, "question")
			return res, nil
		case "fail":
			res.Status = "failed"
			res.Error = arg
			return res, nil
		case "maxturns", "maxturns-always":
			// Run at the turn limit: work happened, a result did not — the real
			// runtime fetches the handover state from the aborted session here,
			// the mock takes it from the directive. "maxturns" passes through on
			// resume (the continuation reaches a result), "maxturns-always" does
			// not (it exercises breaking off the chain).
			if kind == "maxturns" && spec.ResumeSessionID != "" {
				continue
			}
			res.Status = "incomplete"
			res.Result = arg
			res.Error = "turn limit reached (mock) — run cut off before it produced a result"
			return res, nil
		case "sleep":
			d, err := time.ParseDuration(arg)
			if err != nil {
				d = time.Second
			}
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(d):
			}
		case "line":
			if json.Valid([]byte(arg)) {
				onEvent("runtime", json.RawMessage(arg))
				// The session is the stream's, as Claude Code reports it:
				// the run's cost names the session its lines belong to.
				var l struct {
					SessionID string `json:"session_id"`
				}
				if json.Unmarshal([]byte(arg), &l) == nil && l.SessionID != "" {
					res.SessionID = l.SessionID
				}
			}
		case "cost":
			var c Cost
			if err := json.Unmarshal([]byte(arg), &c); err != nil {
				res.Status = "failed"
				res.Error = "mock:cost: " + err.Error()
				return res, nil
			}
			res.CostUSD, res.Basis, res.SessionTotalUSD = c.USD, c.Basis, c.SessionTotalUSD
			res.InputTokens, res.OutputTokens = c.InputTokens, c.OutputTokens
			res.CacheReadTokens, res.CacheCreationTokens = c.CacheReadTokens, c.CacheCreationTokens
			if c.Model != "" {
				res.Model = c.Model
			}
			if c.SessionID != "" {
				res.SessionID = c.SessionID
			}
		case "prompt":
			// Makes the assembled system prompt inspectable (platform protocol,
			// target-system docs, team directory) — otherwise it is invisible
			// from the outside.
			res.Result = spec.SystemPrompt
		case "session":
			// How the run was started, as the engine would see it: the
			// system prompt it is handed (memory context included), the
			// working directory, the session it resumes and the message it
			// gets (#130).
			prompt := spec.Title + "\n\n" + spec.Body
			if spec.ResumeSessionID != "" {
				prompt = spec.ResumeInput
			}
			b, _ := json.Marshal(map[string]string{
				"system_prompt": appendedSystemPrompt(spec), "work_dir": spec.WorkDir,
				"resume": spec.ResumeSessionID, "prompt": prompt,
			})
			res.Result = string(b)
		case "result":
			res.Result = arg
		case "memory":
			res.Memory = arg
		case "reply":
			// The text a real run writes above its status line (#650); put it
			// before a block directive, which ends the run.
			res.Reply = arg
		}
	}
	if res.Result == "" {
		if spec.ResumeSessionID != "" {
			res.Result = "Resumed and completed: " + spec.ResumeInput
		} else {
			res.Result = "Done: " + spec.Title
		}
	}
	for _, n := range notices {
		res.Result += "\nnotice: " + n
	}
	return res, nil
}

// mockKV reads key=... question=... out of a directive (question swallows the rest).
func mockKV(s, key string) string {
	idx := strings.Index(s, key+"=")
	if idx < 0 {
		return ""
	}
	v := s[idx+len(key)+1:]
	if key != "question" {
		if sp := strings.IndexByte(v, ' '); sp >= 0 {
			v = v[:sp]
		}
	}
	return strings.TrimSpace(v)
}

// mockActionBudget is how long the mock waits for an action to answer.
//
// Thirty seconds is far above what any scripted action needs — except one: the
// browser plugin starts a real Chrome and loads a page, and on a loaded runner
// that outgrows the budget. The run then fails with "Client.Timeout exceeded
// while awaiting headers", which reads like a broken action rather than a slow
// machine (#252).
//
// So it is stretched by COVEY_TEST_WAIT_FACTOR — the same lever the suite's own
// waits use (#244). One screw for "this machine is slow", not two: the second
// one is always the one nobody sets. Unset, or below 1, leaves the thirty
// seconds standing, so a local run is unchanged.
const mockActionBudget = 30 * time.Second

func mockActionTimeout() time.Duration {
	f, err := strconv.ParseFloat(os.Getenv("COVEY_TEST_WAIT_FACTOR"), 64)
	if err != nil || f < 1 {
		return mockActionBudget
	}
	return time.Duration(float64(mockActionBudget) * f)
}

// mockActionPath is what the proxy routes: /actions/{system}/{action}. The
// path comes from the task body, so anything else — a "../", a query — is
// refused before it becomes part of a URL.
var mockActionPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func callActionProxy(ctx context.Context, port, path, params string) (string, []byte, error) {
	if port == "" {
		return "", nil, fmt.Errorf("no COVEY_ACTION_PORT set")
	}
	// A number, and written back from the number: "1@example.org" would
	// otherwise turn 127.0.0.1 into userinfo and the request to another host.
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", nil, fmt.Errorf("COVEY_ACTION_PORT %q is not a port", port)
	}
	if !mockActionPath.MatchString(path) || strings.Contains(path, "..") {
		return "", nil, fmt.Errorf("action %q is not <system>/<action>", path)
	}
	url := "http://127.0.0.1:" + strconv.Itoa(n) + "/actions/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(params)))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: mockActionTimeout()}).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", nil, fmt.Errorf("proxy answer: %w", err)
	}
	return out.Status, buf.Bytes(), nil
}
