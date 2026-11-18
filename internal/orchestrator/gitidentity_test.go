package orchestrator

import (
	"testing"

	"covey/internal/agents"
	"covey/internal/gitauthor"
)

// What an agent's commits are attributed to is decided here, on the
// configuration the daemon receives (#106): the daemon puts it on every
// action's context. Without the address in the configuration, nothing is sent.
func TestInjectConfigCarriesTheAgentsGitIdentity(t *testing.T) {
	agent := agents.Agent{Slug: "covey-dev"}

	o := &Orchestrator{Options: Options{GitAuthorEmail: "1+covey-bot@users.noreply.github.com"}}
	got := o.injectConfig(agent, "prompt", 7, nil).GitAuthor
	if want := gitauthor.ForAgent("covey-dev", o.GitAuthorEmail); got != want || !got.Valid() {
		t.Errorf("GitAuthor = %+v, want %+v", got, want)
	}
	if got.Name != "covey-dev (covey agent)" {
		t.Errorf("name = %q", got.Name)
	}

	if got := (&Orchestrator{}).injectConfig(agent, "prompt", 7, nil).GitAuthor; got.Valid() {
		t.Errorf("without COVEY_GIT_AUTHOR_EMAIL the config carries %+v", got)
	}
}

// A workflow step runs in the control plane and takes the identity from the
// same place as the agent's sandbox does.
func TestWorkflowStepsAndSandboxShareOneIdentity(t *testing.T) {
	o := &Orchestrator{Options: Options{GitAuthorEmail: "a@b.example"}}
	agent := agents.Agent{Slug: "covey-devops"}
	if o.gitIdentity(agent) != o.injectConfig(agent, "", 1, nil).GitAuthor {
		t.Error("the sandbox and the workflow step would attribute a commit differently")
	}
}
