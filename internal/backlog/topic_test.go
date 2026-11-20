package backlog

import "testing"

func TestTopicKey(t *testing.T) {
	cases := []struct {
		title, want string
	}{
		// The step is dropped, with or without its colon; case, spacing and
		// trailing punctuation do not make a second topic.
		{"formula: Cost page headline.", "cost page headline"},
		{"formula cost page headline", "cost page headline"},
		{"accept  Cost   page headline!", "cost page headline"},
		{"formula cost page headline?!", "cost page headline"},
		{"formula cost page headline …", "cost page headline"},
		{"formula cost page headline:", "cost page headline"},
		// An issue number is the topic, wherever it stands.
		{"issue #45: something", "#45"},
		{"review #45", "#45"},
		{"amend #2 scope after review, see #7", "#2"},
		// Fewer than three words: no topic, no routing.
		{"go", ""},
		{"go ahead", ""},
		{"go ahead .", ""},
		{"formula: go", ""},
		{"", ""},
		{"   ", ""},
		// A '#' without digits is not an issue reference.
		{"tag # important things", "# important things"},
	}
	for _, c := range cases {
		if got := TopicKey(c.title); got != c.want {
			t.Errorf("TopicKey(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestNormalizeTopic(t *testing.T) {
	cases := []struct {
		topic, want string
	}{
		// An explicit topic keeps its first word: it has no step.
		{"#17 part 3", "#17 part 3"},
		{"  #17   Part 3. ", "#17 part 3"},
		{"Cost page headline", "cost page headline"},
		{"go", "go"},
		{"...", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeTopic(c.topic); got != c.want {
			t.Errorf("NormalizeTopic(%q) = %q, want %q", c.topic, got, c.want)
		}
	}
}
