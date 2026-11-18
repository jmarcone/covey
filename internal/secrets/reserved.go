package secrets

import (
	"errors"
	"strings"
)

// AdminKey is the organisation's Anthropic Admin API key (sk-ant-admin…), or
// a key scoped to read:analytics. It reads what Anthropic billed for the WHOLE
// organisation (the Usage and Cost Analytics API, spec/06) and, as an admin
// key, could manage it. It lives in the control plane and nowhere else.
//
// The name deliberately does not end in _token: the credential loop probes
// every *_token through a target-system plugin (orchestrator/credlifetime.go),
// and this key belongs to no plugin.
const AdminKey = "anthropic_admin_key"

// adminPrefix is how an Admin API key starts, whatever key it was filed under.
const adminPrefix = "sk-ant-admin"

// ErrReserved: the key (or the value) may not go where it was sent. Its own
// sentinel so the API answers it as a refused request (400), with the hint.
var ErrReserved = errors.New("reserved secret")

// ReservedError carries the sentence a person needs; it matches ErrReserved.
type ReservedError struct {
	Key  string
	Hint string
}

func (e *ReservedError) Error() string        { return e.Hint }
func (e *ReservedError) Is(target error) bool { return target == ErrReserved }

// reserved is THE deny list: keys that are org-wide only. Never assigned to an
// agent, never an agent's own secret, never a runtime credential — so never
// injected into a sandbox. Assign, PutAgent and the runtimes' AddCredential
// all ask CheckAgentUse; there is no second list.
var reserved = map[string]string{
	AdminKey: "anthropic_admin_key is the organisation's Admin API key: it reads the billing of the whole " +
		"organisation and stays in the control plane. It cannot be assigned to an agent, stored as an " +
		"agent's own secret, or used as a runtime credential. For agents, store an API key (sk-ant-api…) " +
		"under anthropic_api_key.",
}

// canonical is the form a key is compared in: a key spelled with other case or
// stray blanks is the same key to a person, so it must be to the list too.
func canonical(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

// Reserved says whether key is org-wide only.
func Reserved(key string) bool {
	_, ok := reserved[canonical(key)]
	return ok
}

// CheckAgentUse refuses a reserved key on every path that would bring a value
// to an agent: an assignment, an agent's own secret, a runtime credential.
func CheckAgentUse(key string) error {
	if hint, ok := reserved[canonical(key)]; ok {
		return &ReservedError{Key: key, Hint: hint}
	}
	return nil
}

// CheckValue refuses an Admin API key stored under any other key. The other
// Anthropic keys are runtime credentials and reach sandboxes; an admin key
// filed as anthropic_api_key by mistake would go with them.
func CheckValue(key, value string) error {
	if canonical(key) == AdminKey {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(value), adminPrefix) {
		return &ReservedError{Key: key, Hint: "This is an Anthropic Admin API key (sk-ant-admin…). It may only be " +
			"stored as the organisation's anthropic_admin_key, never under " + strings.TrimSpace(key) +
			", which can reach an agent's sandbox."}
	}
	return nil
}
