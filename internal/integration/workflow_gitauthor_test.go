package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benjaminLedel/covey-plugin-sdk/target"

	"covey/internal/gitauthor"
	"covey/internal/reqlog"
)

// commitTestSystem does what the github plugin's commit action does: it
// creates a commit over the API, through the client the SDK hands plugins,
// with no author of its own.
type commitTestSystem struct{}

func init() {
	target.Register(target.Descriptor{
		Name: "committest", Label: "Committest", Kind: "builtin", System: commitTestSystem{},
	})
}

func (commitTestSystem) Name() string { return "committest" }
func (commitTestSystem) ActionSubject(action string, _ json.RawMessage) string {
	return "committest:" + action
}
func (commitTestSystem) PromptDoc() string { return "" }

func (commitTestSystem) Execute(ctx context.Context, _ string, _ json.RawMessage, cred target.Credential) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cred.BaseURL, "/")+"/repos/o/r/git/commits",
		strings.NewReader(`{"message":"m","tree":"t"}`))
	if err != nil {
		return nil, err
	}
	resp, err := reqlog.Client("github", 5*time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	return nil, resp.Body.Close()
}

// A workflow action step runs in the control plane, not in a daemon, and
// takes the agent's identity from the context ActAs builds (#106): the commit
// it creates must carry it as author and committer, as one from an agent's
// sandbox does.
func TestWorkflowActionStepCommitsAsTheAgent(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.orch.GitAuthorEmail = "1+covey-bot@users.noreply.github.com"

	var mu sync.Mutex
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	admin := login(t, s, "admin@test.local", "admin-passwort")
	admin.expect(http.MethodPatch, "/api/v1/targets/committest", map[string]any{"enabled": true}, http.StatusOK)
	agent, err := s.registry.Create(ctx, s.orgID, "wf-committer", "Workflow committer", "mock", &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.registry.SaveConfig(ctx, agent.ID, map[string]string{
		"SOUL.md": "# Committer", "ACCESS.md": "- system: committest scope: write",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"committest_url": srv.URL, "committest_token": "dummy-token"} {
		if err := s.secrets.Put(ctx, s.orgID, k, v); err != nil {
			t.Fatal(err)
		}
		if err := s.secrets.Assign(ctx, s.orgID, k, agent.ID); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.orch.ActAs(ctx, agent, "committest", "commit", json.RawMessage(`{}`), ""); err != nil {
		t.Fatalf("ActAs: %v", err)
	}

	want := gitauthor.ForAgent("wf-committer", s.orch.GitAuthorEmail)
	mu.Lock()
	defer mu.Unlock()
	for _, role := range []string{"author", "committer"} {
		who, ok := sent[role].(map[string]any)
		if !ok || who["name"] != want.Name || who["email"] != want.Email {
			t.Errorf("%s = %v, want %+v", role, sent[role], want)
		}
	}
}
