package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The prompt carries only what the agent may actually use.
//
// The organisation's activation used to decide alone which target-system docs
// landed in a system prompt. Every agent therefore carried the instructions for
// every enabled system around — including those whose credentials the broker
// refuses it. Wrong twice over: it invites an attempt that cannot work, and it
// is expensive. The built-in docs come to around 11,000 tokens in total, GitLab
// and GitHub about 4,000 each, and they sit in the context of every single
// turn.
func TestTargetDocsFollowAccessNotOnlyActivation(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")

	// The organisation enables two systems …
	admin.expect(http.MethodPatch, "/api/v1/targets/dev", map[string]any{"enabled": true}, http.StatusOK)
	admin.expect(http.MethodPatch, "/api/v1/targets/teams", map[string]any{"enabled": true}, http.StatusOK)

	agent, err := s.registry.Create(ctx, s.orgID, "tester", "Tester", "mock", &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	// … the agent has a line for only one of them.
	if _, err := s.registry.SaveConfig(ctx, agent.ID, map[string]string{
		"SOUL.md":   "# Tester",
		"ACCESS.md": "- system: dev scope: exec,processes",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}

	docs, err := s.targets.EnabledDocsForAgent(ctx, s.orgID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(docs, "\n")
	if !strings.Contains(joined, "dev actions") {
		t.Fatalf("the granted system belongs in the prompt: %q", joined)
	}
	if strings.Contains(joined, "create_conversation") {
		t.Fatal("a system without a line in ACCESS.md must not reach the prompt — the broker refuses it anyway")
	}

	// Granted afterwards, it is there — the docs follow the config, not a
	// deploy.
	if _, err := s.registry.SaveConfig(ctx, agent.ID, map[string]string{
		"SOUL.md":   "# Tester",
		"ACCESS.md": "- system: dev scope: exec,processes\n- system: teams scope: read,write",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}
	docs, err = s.targets.EnabledDocsForAgent(ctx, s.orgID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(docs, "\n"), "create_conversation") {
		t.Fatal("after the grant the Teams actions belong in the prompt")
	}
}

// The target docs stand in the system prompt, so their order is part of what
// keeps the prompt the same from task to task (#23). The query had no ORDER BY:
// the order was the plan's — the unique index's when Postgres read through it,
// the table's when it scanned, and there a re-uploaded manifest (an UPDATE,
// which moves the row) went last. Read by name, it is the same whatever was
// written last and however the rows are read.
func TestTargetDocsComeInNameOrder(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	manifest := func(name string) []byte {
		return []byte(`{"name":"` + name + `","actions":{"list":{"method":"GET","path":"/items"}}}`)
	}
	for _, name := range []string{"alpha-desk", "zulu-desk"} {
		if _, err := s.targets.PutManifest(ctx, s.orgID, manifest(name)); err != nil {
			t.Fatal(err)
		}
	}
	agent, err := s.registry.Create(ctx, s.orgID, "reihenfolge", "Reihenfolge", "mock", &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.registry.SaveConfig(ctx, agent.ID, map[string]string{
		"SOUL.md":   "# Reihenfolge",
		"ACCESS.md": "- system: zulu-desk scope: read\n- system: alpha-desk scope: read",
	}, &s.adminID); err != nil {
		t.Fatal(err)
	}
	order := func() string {
		t.Helper()
		docs, err := s.targets.DocsForAgent(ctx, s.orgID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range docs {
			if strings.HasSuffix(d.System, "-desk") {
				names = append(names, d.System)
			}
		}
		return strings.Join(names, ",")
	}
	if got := order(); got != "alpha-desk,zulu-desk" {
		t.Fatalf("the docs come in name order, got %s", got)
	}
	// Upload alpha again: an UPDATE, after which the table holds it behind zulu.
	if _, err := s.targets.PutManifest(ctx, s.orgID, manifest("alpha-desk")); err != nil {
		t.Fatal(err)
	}
	if got := order(); got != "alpha-desk,zulu-desk" {
		t.Fatalf("a re-uploaded manifest must not move its system in the prompt, got %s", got)
	}
}
