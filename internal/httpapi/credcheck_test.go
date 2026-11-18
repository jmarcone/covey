package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckCredentialWrongSlot(t *testing.T) {
	// Values filed into the wrong slot are detected without touching the network.
	c := checkCredential(context.Background(), "claude_code_oauth_token", "sk-ant-api03-xxx")
	if !c.Checked || c.Valid || !strings.Contains(c.Hint, "anthropic_api_key") {
		t.Errorf("API key in the OAuth slot not detected: %+v", c)
	}
	c = checkCredential(context.Background(), "anthropic_api_key", "sk-ant-oat01-xxx")
	if !c.Checked || c.Valid || !strings.Contains(c.Hint, "claude_code_oauth_token") {
		t.Errorf("OAuth token in the API key slot not detected: %+v", c)
	}
}

func TestCheckCredentialLive(t *testing.T) {
	var gotAuth, gotBeta, gotAPIKey string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	orig := anthropicBaseURL
	anthropicBaseURL = srv.URL
	defer func() { anthropicBaseURL = orig }()

	// OAuth token: bearer + beta header, 200 → valid.
	c := checkCredential(context.Background(), "claude_code_oauth_token", "sk-ant-oat01-live")
	if !c.Checked || !c.Valid {
		t.Errorf("valid OAuth token: %+v", c)
	}
	if gotAuth != "Bearer sk-ant-oat01-live" || gotBeta == "" {
		t.Errorf("wrong OAuth headers: auth=%q beta=%q", gotAuth, gotBeta)
	}

	// API key: x-api-key header.
	c = checkCredential(context.Background(), "anthropic_api_key", "sk-ant-api03-live")
	if !c.Checked || !c.Valid || gotAPIKey != "sk-ant-api03-live" {
		t.Errorf("valid API key: %+v (x-api-key=%q)", c, gotAPIKey)
	}

	// 401 → invalid, with an actionable hint.
	status = http.StatusUnauthorized
	c = checkCredential(context.Background(), "claude_code_oauth_token", "sk-ant-oat01-dead")
	if !c.Checked || c.Valid || !strings.Contains(c.Hint, "claude setup-token") {
		t.Errorf("expired OAuth token: %+v", c)
	}

	// Server error → stored, but unverified (fail-open for storing).
	status = http.StatusBadGateway
	c = checkCredential(context.Background(), "anthropic_api_key", "sk-ant-api03-x")
	if c.Checked {
		t.Errorf("5xx must not count as checked: %+v", c)
	}

	// Unknown keys are not checked.
	c = checkCredential(context.Background(), "zammad_token", "abc")
	if c.Checked || c.Hint != "" {
		t.Errorf("unknown key was checked: %+v", c)
	}
}

// anthropic_admin_key takes an Admin API key, or a key the analytics API
// accepts (read:analytics); a key of another kind is refused without the
// network, and an admin key is refused under every other name.
func TestCheckCredentialAdminKey(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ key, value, hint string }{
		{"anthropic_admin_key", "sk-ant-api03-xxx", "anthropic_api_key"},
		{"anthropic_admin_key", "sk-ant-oat01-xxx", "claude_code_oauth_token"},
		{"anthropic_api_key", "sk-ant-admin01-xxx", "anthropic_admin_key"},
		{"claude_code_oauth_token", "sk-ant-admin01-xxx", "anthropic_admin_key"},
	} {
		got := checkCredential(ctx, c.key, c.value)
		if !got.Checked || got.Valid || !strings.Contains(got.Hint, c.hint) {
			t.Errorf("%s = %s…: %+v, want refused pointing at %s", c.key, c.value[:10], got, c.hint)
		}
	}

	var path, beta, apiKey string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, beta, apiKey = r.URL.Path, r.Header.Get("anthropic-beta"), r.Header.Get("x-api-key")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	orig := anthropicBaseURL
	anthropicBaseURL = srv.URL
	defer func() { anthropicBaseURL = orig }()

	// The probe is the poller's own call: one hour of /cost.
	if got := checkCredential(ctx, "anthropic_admin_key", "sk-ant-admin01-live"); !got.Checked || !got.Valid {
		t.Errorf("valid admin key: %+v", got)
	}
	if path != "/v1/organizations/analytics/cost" || beta != "analytics-usage-cost-2026-08-24" || apiKey != "sk-ant-admin01-live" {
		t.Errorf("probe: path %q, beta %q, key %q", path, beta, apiKey)
	}
	// Another prefix counts only when the analytics API takes it.
	if got := checkCredential(ctx, "anthropic_admin_key", "rak-analytics-live"); !got.Checked || !got.Valid {
		t.Errorf("read:analytics key: %+v", got)
	}
	status = http.StatusForbidden
	if got := checkCredential(ctx, "anthropic_admin_key", "rak-analytics-live"); !got.Checked || got.Valid || !strings.Contains(got.Hint, "read:analytics") {
		t.Errorf("a key without read:analytics: %+v", got)
	}
	status = http.StatusUnauthorized
	if got := checkCredential(ctx, "anthropic_admin_key", "sk-ant-admin01-dead"); !got.Checked || got.Valid {
		t.Errorf("rejected admin key: %+v", got)
	}
	status = http.StatusBadGateway
	if got := checkCredential(ctx, "anthropic_admin_key", "sk-ant-admin01-x"); got.Checked {
		t.Errorf("5xx must not count as checked: %+v", got)
	}
}
