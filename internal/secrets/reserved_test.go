package secrets

import (
	"errors"
	"strings"
	"testing"
)

// The deny list: the admin key never goes towards an agent, however it is
// spelled, and an admin key's value goes under no other name.
func TestReservedAdminKey(t *testing.T) {
	for _, k := range []string{AdminKey, "ANTHROPIC_ADMIN_KEY", " anthropic_admin_key "} {
		err := CheckAgentUse(k)
		if !errors.Is(err, ErrReserved) || !strings.Contains(err.Error(), "anthropic_api_key") {
			t.Errorf("CheckAgentUse(%q) = %v", k, err)
		}
		if !Reserved(k) {
			t.Errorf("Reserved(%q) = false", k)
		}
	}
	for _, k := range []string{"anthropic_api_key", "claude_code_oauth_token", "anthropic_admin_key_token"} {
		if err := CheckAgentUse(k); err != nil {
			t.Errorf("CheckAgentUse(%q) = %v", k, err)
		}
	}
	if err := CheckValue("anthropic_api_key", " sk-ant-admin01-x"); !errors.Is(err, ErrReserved) {
		t.Errorf("an admin key under anthropic_api_key: %v", err)
	}
	if err := CheckValue("Anthropic_Admin_Key", "sk-ant-admin01-x"); err != nil {
		t.Errorf("an admin key under its own name: %v", err)
	}
	if err := CheckValue("anthropic_api_key", "sk-ant-api03-x"); err != nil {
		t.Errorf("an API key under anthropic_api_key: %v", err)
	}
}
