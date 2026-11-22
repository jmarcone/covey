package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"covey/internal/gitauthor"
	"covey/internal/reqlog"
	"github.com/benjaminLedel/covey-plugin-sdk/target"
)

// newTestProxy builds an actionProxy whose fetchSecret is a stub over a fixed
// map — substituteSecrets then resolves without any WebSocket round-trip.
// Production wiring (startActionProxy) points fetchSecret at the real,
// uncached Client.secret instead.
func newTestProxy(secrets map[string]InjectSecret) *actionProxy {
	return &actionProxy{
		taskID: "task-1",
		fetchSecret: func(_ context.Context, key, _ string) (InjectSecret, error) {
			sec, ok := secrets[key]
			if !ok || !sec.Granted {
				return InjectSecret{}, fmt.Errorf("secret %q not granted", key)
			}
			return sec, nil
		},
	}
}

func TestSubstituteSecretsReplacesPlaceholder(t *testing.T) {
	p := newTestProxy(map[string]InjectSecret{
		"misavo_staging_pass": {Granted: true, Value: "s3cret!"},
	})
	in := json.RawMessage(`{"selector":"#password","text":"{{secret:misavo_staging_pass}}"}`)

	out, err := p.substituteSecrets(context.Background(), in)
	if err != nil {
		t.Fatalf("substituteSecrets: %v", err)
	}
	var got struct{ Text string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v (%s)", err, out)
	}
	if got.Text != "s3cret!" {
		t.Fatalf("text = %q, want %q", got.Text, "s3cret!")
	}
}

func TestSubstituteSecretsNoPlaceholderIsNoop(t *testing.T) {
	p := newTestProxy(nil)
	in := json.RawMessage(`{"selector":"#password","text":"plain text"}`)

	out, err := p.substituteSecrets(context.Background(), in)
	if err != nil {
		t.Fatalf("substituteSecrets: %v", err)
	}
	// Same bytes back — no secret was requested (nil cache would panic/deny a
	// lookup if one had been attempted), confirming the short-circuit.
	if string(out) != string(in) {
		t.Fatalf("params changed without a placeholder: got %s", out)
	}
}

func TestSubstituteSecretsMultipleDistinctKeys(t *testing.T) {
	p := newTestProxy(map[string]InjectSecret{
		"misavo_staging_user": {Granted: true, Value: "anika.otto@buergerhaus-krebeck.de"},
		"misavo_staging_pass": {Granted: true, Value: "start123"},
	})
	in := json.RawMessage(`{"steps":["{{secret:misavo_staging_user}}","{{secret:misavo_staging_pass}}"]}`)

	out, err := p.substituteSecrets(context.Background(), in)
	if err != nil {
		t.Fatalf("substituteSecrets: %v", err)
	}
	var got struct{ Steps []string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v (%s)", err, out)
	}
	if len(got.Steps) != 2 || got.Steps[0] != "anika.otto@buergerhaus-krebeck.de" || got.Steps[1] != "start123" {
		t.Fatalf("steps = %v, want both secrets resolved", got.Steps)
	}
}

func TestSubstituteSecretsUngrantedIsError(t *testing.T) {
	p := newTestProxy(map[string]InjectSecret{
		// Not Granted: the cache never stores a denial (see Client.secret), so
		// this only models "not yet cached" — the real deny path goes through
		// the WebSocket. What matters here is that substituteSecrets propagates
		// an error instead of silently typing an empty/placeholder string.
	})
	in := json.RawMessage(`{"text":"{{secret:unknown_key}}"}`)

	if _, err := p.substituteSecrets(context.Background(), in); err == nil {
		t.Fatal("expected an error for an unresolvable secret, got nil")
	}
}

func TestJSONStringEscapeHandlesQuotesAndBackslashes(t *testing.T) {
	esc := jsonStringEscape(`p@ss"w\ord`)
	full := json.RawMessage(`{"text":"` + esc + `"}`)
	var got struct{ Text string }
	if err := json.Unmarshal(full, &got); err != nil {
		t.Fatalf("escaped value does not round-trip as JSON: %v (%s)", err, full)
	}
	if got.Text != `p@ss"w\ord` {
		t.Fatalf("got %q, want %q", got.Text, `p@ss"w\ord`)
	}
}

// TestMaskSecretsInResult is #691: a value substituted into an action came
// back to the model whenever the action printed it.
func TestMaskSecretsInResult(t *testing.T) {
	values := map[string]string{`s3"cret`: "staging_pass"}
	got := maskSecrets(map[string]any{"stdout": `login with s3"cret ok`, "code": 0}, values)
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "s3") || !strings.Contains(string(raw), "{{secret:staging_pass}}") {
		t.Fatalf("result not masked: %s", raw)
	}
	clean := map[string]any{"stdout": "nothing to see"}
	if got := maskSecrets(clean, values); got == nil || got.(map[string]any)["stdout"] != "nothing to see" {
		t.Fatalf("a result without the value must come back unchanged: %v", got)
	}
	if got := maskSecretText(`exec: s3"cret rejected`, values); strings.Contains(got, "s3") {
		t.Fatalf("error text not masked: %s", got)
	}
}

// gitSystem is a target system whose action does what the github plugin's
// commit action does: POST /repos/o/r/git/commits through the client the SDK
// hands plugins, with no author of its own.
type gitSystem struct{ url string }

func (gitSystem) Name() string                                     { return "fakegit" }
func (gitSystem) ActionSubject(a string, _ json.RawMessage) string { return "fakegit:" + a }
func (gitSystem) PromptDoc() string                                { return "" }
func (g gitSystem) Execute(ctx context.Context, _ string, _ json.RawMessage, _ target.Credential) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url+"/repos/o/r/git/commits",
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

// The identity the control plane injected reaches the commit request: execute
// puts it on the action's context (#106). Without one, nothing is added.
func TestExecuteGivesTheCommitRequestTheRunsIdentity(t *testing.T) {
	id := gitauthor.Identity{Name: "covey-dev (covey agent)", Email: "1+covey-bot@users.noreply.github.com"}
	for name, want := range map[string]gitauthor.Identity{"set": id, "unset": {}} {
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
		}))
		c := NewClient("", "", "agent-1", t.TempDir(), slog.New(slog.DiscardHandler))
		c.targets["fakegit"] = gitSystem{url: srv.URL}
		c.creds["fakegit"] = InjectCredentials{Granted: true, TTLSecs: 60}
		c.credsFetched["fakegit"] = time.Now()
		// The live config says something else: the run's snapshot decides.
		c.cfg.GitAuthor = gitauthor.Identity{Name: "other (covey agent)", Email: "other@example.com"}
		p := &actionProxy{client: c, taskID: "task-1", gitAuthor: want}

		if _, _, err := p.execute(context.Background(), "fakegit", "commit", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("%s: execute: %v", name, err)
		}
		srv.Close()
		who, has := got["author"].(map[string]any)
		if has != want.Valid() || (has && (who["name"] != want.Name || who["email"] != want.Email)) {
			t.Errorf("%s: author = %v, want %+v", name, got["author"], want)
		}
		if _, ok := got["committer"]; ok != want.Valid() {
			t.Errorf("%s: committer present = %v, want %v", name, ok, want.Valid())
		}
	}
}
