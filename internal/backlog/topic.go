package backlog

import (
	"regexp"
	"strings"
	"unicode"
)

// issueRef is an issue or pull request number in a title ("#45").
var issueRef = regexp.MustCompile(`#\d+`)

// TopicKey is the topic a task title names, the key covey/create_task routes
// by (#45): one topic has one open task at an agent, and an addendum to it
// becomes a note on that task rather than a second task with a session of
// its own.
//
// Titles follow "<step> <topic>" ("formula cost page headline", "accept cost
// page headline", "issue #17: Agents as a main view"). The first "#<digits>"
// in the title is the topic when there is one: a step names its issue
// however the rest of the title is worded. Otherwise the topic is the title
// without its first word (the step, with or without a trailing ':'),
// lower-cased, its whitespace collapsed and its trailing punctuation
// removed, so that "formula: Cost page headline." and "formula cost page
// headline" meet.
//
// A title of fewer than three words has no topic (""): with the step taken
// off, one word ("go", "hold") says nothing about what it is about, and
// routing on it would join unrelated work.
func TopicKey(title string) string {
	if ref := issueRef.FindString(title); ref != "" {
		return ref
	}
	words := strings.Fields(trimTrailingPunct(collapse(title)))
	if len(words) < 3 {
		return ""
	}
	return strings.Join(words[1:], " ")
}

// NormalizeTopic is an explicit topic as the agent gave it, brought to the
// form TopicKey produces — lower-cased, whitespace collapsed, trailing
// punctuation removed — but whole: an explicit topic has no step to drop
// ("#17 part 3" stays "#17 part 3").
func NormalizeTopic(s string) string {
	return trimTrailingPunct(collapse(s))
}

func collapse(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// trimTrailingPunct removes punctuation from the end of the whole string, and
// the space a removed mark leaves ("go ahead ." → "go ahead").
func trimTrailingPunct(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
}
