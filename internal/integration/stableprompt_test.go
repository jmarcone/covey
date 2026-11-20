package integration

import (
	"context"
	"strings"
	"testing"

	"covey/internal/memory"
)

/* Every run of an agent wrote its system prompt into the prompt cache anew on
   its first turn, because the prompt carried the task's share: the wiki's hits
   and index and the paragraph naming ~/scratch/<task-id> (#23). Checked here on
   the whole path, control plane to daemon to runtime: two tasks of one agent
   get the same system prompt to the byte, and the task's share arrives in the
   first user message, each part once. */

func TestTheSystemPromptIsTheSameForEveryTaskOfAnAgent(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	agent := s.newSupportAgent("stabiler-prompt")
	// A page, so that the dispatch has wiki context to send.
	if _, err := s.mem.Write(ctx, agent.ID, memory.PageInput{
		Slug: "kunde-meier", Title: "Kunde Meier",
		Body: "Meier zahlt per Rechnung und schreibt meist freitags.", Source: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	_, first := laufLassen(t, s, agent, "Rechnung 17 prüfen", "Meier fragt nach der Rechnung.\n[mock:prompt]")
	_, second := laufLassen(t, s, agent, "Passwort zurücksetzen", "Ein Kunde kommt nicht mehr hinein.\n[mock:prompt]")
	if !strings.Contains(first, agent.DisplayName) || !strings.Contains(first, "COVEY_STATUS") {
		t.Fatalf("the run did not show its system prompt:\n%s", kürzen(first))
	}
	if first != second {
		t.Fatalf("two tasks of one agent have to get the same system prompt\nfirst:\n%s\n\nsecond:\n%s",
			kürzen(first), kürzen(second))
	}
	for _, task := range []string{"## Relevant from your wiki", "## Your wiki (index)", "## Where your files go", "~/scratch/"} {
		if strings.Contains(first, task) {
			t.Fatalf("%q belongs to the task and must not stand in the system prompt:\n%s", task, kürzen(first))
		}
	}

	task, input := laufLassen(t, s, agent, "Mahnung an Meier", "Meier hat Rechnung 17 nicht bezahlt.\n[mock:input]")
	for _, part := range []string{
		"Mahnung an Meier", "Meier hat Rechnung 17 nicht bezahlt.",
		"## Relevant from your wiki", "### Kunde Meier [[kunde-meier]]", "## Your wiki (index)",
		"## Where your files go", "~/scratch/" + task.ID.String() + "/",
	} {
		if n := strings.Count(input, part); n != 1 {
			t.Fatalf("%q stands %d times in the first user message, want once:\n%s", part, n, kürzen(input))
		}
	}
}
