package daemon

import (
	"errors"
	"regexp"
	"strings"
)

// A topic is what several tasks are about: a pull request or an issue (#130).
// The tasks of one agent on one topic share a work directory, and the next
// one resumes the previous one's engine session while that is warm, so a fix
// round reads its context from the prompt cache instead of reading the code
// again. Written pr:<n> or issue:<n>.
//
// The rule is one, here, for every way a task gets a topic: covey/create_task
// in the sandbox (checked before the guard rails), the control plane's answer
// to it, and POST /api/v1/agents/{id}/tasks.

// MaxTopicLen caps a topic. A PR number has no more than a handful of digits;
// the cap is there so that a topic can never make a path element long.
const MaxTopicLen = 64

var topicPattern = regexp.MustCompile(`^(pr|issue):[0-9]+$`)

// ErrTopic is the refusal of a topic that is not pr:<n> or issue:<n>. Its
// text is what the agent reads, so it says the format.
var ErrTopic = errors.New(`topic must be "pr:<number>" or "issue:<number>" (at most 64 characters)`)

// ParseTopic checks a topic and returns it trimmed; "" stays "" (no topic).
func ParseTopic(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > MaxTopicLen || !topicPattern.MatchString(s) {
		return "", ErrTopic
	}
	return s, nil
}

// topicDirPrefix starts the name of every topic directory under ~/scratch.
const topicDirPrefix = "topic-"

var topicDirPattern = regexp.MustCompile(`^topic-[a-z0-9-]{1,128}$`)

// TopicDir is the name of the work directory an agent's tasks on topic share:
// topic-<agent>-<topic>, every character that is not a lower-case letter or a
// digit turned into "-". No path separator can survive that, and isTopicDir
// checks the name again where it becomes a path.
func TopicDir(agentSlug, topic string) string {
	var b strings.Builder
	b.WriteString(topicDirPrefix)
	for _, ch := range strings.ToLower(agentSlug + "-" + topic) {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// isTopicDir accepts what TopicDir makes and nothing else. The name becomes a
// path element, and the scratch sweep uses the same test to recognise what it
// may age out.
func isTopicDir(s string) bool {
	return topicDirPattern.MatchString(s)
}
