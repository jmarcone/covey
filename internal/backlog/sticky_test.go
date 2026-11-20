package backlog

import "testing"

// The structured topics survive #45's normaliser unchanged but for case, and
// only they make a session sticky.
func TestStickyTopicForms(t *testing.T) {
	for in, want := range map[string]string{
		"pr:jmarcone/covey#206":       "pr:jmarcone/covey#206",
		"PR:Jmarcone/Covey#206":       "pr:jmarcone/covey#206",
		"  issue:example/app#7  ":     "issue:example/app#7",
		"pr:example/my.repo_x-y#1009": "pr:example/my.repo_x-y#1009",
	} {
		got := NormalizeTopic(in)
		if got != want || !IsStickyTopic(got) {
			t.Errorf("NormalizeTopic(%q) = %q (sticky %v), want %q", in, got, IsStickyTopic(got), want)
		}
	}
	kind, repo, n := TopicParts("pr:jmarcone/covey#206")
	if kind != "pr" || repo != "jmarcone/covey" || n != "206" {
		t.Errorf("TopicParts: %q %q %q", kind, repo, n)
	}
	// Free-form topics, title keys and malformed forms only route.
	for _, k := range []string{"#45", "cost page headline", "", "pr:42", "issue:7", "pr:example/app#0",
		"pr:example/app#042", "pr:../x#1", "pr:example/../x#1", "pr:example/app/sub#1", "pr:-x/app#1",
		"pr:example/app#1 part 2", "mr:example/app#1"} {
		if IsStickyTopic(NormalizeTopic(k)) {
			t.Errorf("%q must not be sticky", k)
		}
	}
	if k, r, n := TopicParts("cost page headline"); k != "" || r != "" || n != "" {
		t.Error("a free-form topic has no parts")
	}
}
