package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"covey/internal/gitauthor"
)

// committingRuntime is an agent that commits twice: once with `git commit` in
// its sandbox — what it has in its environment decides who that is — and once
// through the commit action, on the port the daemon gave it.
type committingRuntime struct {
	env chan []string
}

func (committingRuntime) Name() string { return "committing" }

func (r committingRuntime) Run(ctx context.Context, spec RunSpec, _ func(string, json.RawMessage)) (RunResult, error) {
	r.env <- spec.Env
	port := ""
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "COVEY_ACTION_PORT="); ok {
			port = v
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://127.0.0.1:"+port+"/actions/fakegit/commit", strings.NewReader(`{}`))
	if err != nil {
		return RunResult{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return RunResult{}, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return RunResult{Status: "done", Result: string(body)}, nil
}

// The run's identity reaches both doors an agent commits through (#106),
// starting from the config the run was assigned: its environment, for `git
// commit` in the sandbox, and the commit action, which sends no author of its
// own. The unit tests above start in the middle (an actionProxy built by
// hand); this one starts at runTask, so the two lines that hand the config on
// are covered.
func TestRunTaskCarriesTheIdentityToTheEnvironmentAndTheCommitAction(t *testing.T) {
	id := gitauthor.Identity{Name: "covey-dev (covey agent)", Email: "1+covey-bot@users.noreply.github.com"}
	for name, want := range map[string]gitauthor.Identity{"set": id, "unset": {}} {
		t.Run(name, func(t *testing.T) {
			var sent map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&sent)
			}))
			defer srv.Close()

			c := NewClient("", "", "agent-1", t.TempDir(), slog.New(slog.DiscardHandler))
			c.targets["fakegit"] = gitSystem{url: srv.URL}
			c.creds["fakegit"] = InjectCredentials{Granted: true, TTLSecs: 60}
			c.credsFetched["fakegit"] = time.Now()
			rt := committingRuntime{env: make(chan []string, 1)}
			c.runtimes = map[string]Runtime{"committing": rt}
			c.cfg = InjectConfig{Runtime: "committing", GitAuthor: want}
			standInControlPlane(t, c)

			c.runTask(context.Background(), AssignTask{TaskID: "task-g", Title: "Commit"})

			env := <-rt.env
			for _, kv := range want.Env() {
				if !containsString(env, kv) {
					t.Errorf("the run's environment lacks %s: %v", kv, env)
				}
			}
			if !want.Valid() {
				for _, kv := range env {
					if strings.HasPrefix(kv, "GIT_") {
						t.Errorf("without an identity the environment carries %s", kv)
					}
				}
			}
			who, has := sent["author"].(map[string]any)
			if has != want.Valid() || (has && (who["name"] != want.Name || who["email"] != want.Email)) {
				t.Errorf("the commit action sent author %v, want %+v", sent["author"], want)
			}
		})
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
