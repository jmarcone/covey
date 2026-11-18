package workflow

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The definition of "PR review to merge-ready" (#131) lives in
// workflows/pr-review.workflow.yaml and is installed on the running covey
// from that file. Parse and Check alone would let most of what matters here
// go unnoticed: a missing mode reads as off, a missing only_if turns the gate
// into a plain schedule, a typo'd key is dropped, and Check never compares a
// condition's field with the result of the step it reads. So the findings
// below look at those things themselves.

const prReviewFile = "../../workflows/pr-review.workflow.yaml"

func prReviewSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(prReviewFile)
	if err != nil {
		t.Fatalf("the definition is part of the repository: %v", err)
	}
	return string(b)
}

// prReviewFindings is everything wrong with a text as the definition of the
// PR review. An empty answer means the file is as it has to be.
func prReviewFindings(src string) []string {
	var out []string
	bad := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	// Unknown keys: yaml.Unmarshal drops them, so Parse would not notice.
	var f file
	dec := yaml.NewDecoder(strings.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		bad("top level: %v", err)
	}
	for i := 0; i+1 < len(f.Steps.Content); i += 2 {
		id := f.Steps.Content[i].Value
		raw, err := yaml.Marshal(f.Steps.Content[i+1])
		if err != nil {
			bad("step %s: %v", id, err)
			continue
		}
		var fs fileStep
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&fs); err != nil {
			bad("step %s: %v", id, err)
		}
	}

	d, err := Parse([]byte(src))
	if err != nil {
		return append(out, "parse: "+err.Error())
	}
	for _, p := range Check(d) {
		bad("check: %s %s: %s", p.Step, p.Code, p.Text)
	}

	if d.Name != "PR review to merge-ready" {
		bad("workflow name is %q", d.Name)
	}
	if d.Mode != ModeWatch {
		bad("mode is %q, the watch week needs watch", d.Mode)
	}
	if d.Owner != "admin@covey.local" {
		bad("owner is %q", d.Owner)
	}
	if got := d.Roles["Review"]; len(got) != 1 || got[0] != "covey-reviewer" {
		bad("role Review is %v", got)
	}
	if got := d.Roles["Author"]; len(got) != 1 || got[0] != "covey-dev" {
		bad("role Author is %v", got)
	}

	step := func(id string, kind Kind) *Step {
		s := d.Steps[id]
		if s == nil || s.Kind != kind {
			bad("step %s is not a %s", id, kind)
			return &Step{}
		}
		return s
	}
	has := func(list []string, want ...string) bool {
		for _, w := range want {
			found := false
			for _, v := range list {
				found = found || v == w
			}
			if !found {
				return false
			}
		}
		return true
	}

	tr := step("trigger", KindTrigger)
	if tr.OnlyIf != "github:pulls" || tr.As != "covey-dev" || tr.Every != 10*time.Minute {
		bad("trigger: only_if %q as %q every %s", tr.OnlyIf, tr.As, tr.Every)
	}
	if tr.Daily != "" || tr.Webhook != "" || tr.Token {
		bad("trigger has a second source: daily %q webhook %q token %v", tr.Daily, tr.Webhook, tr.Token)
	}
	if tr.Next[ExitOut] != "review" {
		bad("trigger leads to %q", tr.Next[ExitOut])
	}

	rv := step("review", KindAgent)
	if rv.Role != "Review" || !has(rv.Result, "verdict", "majors") {
		bad("review: role %q result %v", rv.Role, rv.Result)
	}
	if rv.NotSameAs != "fix" {
		bad("review not_same_as is %q", rv.NotSameAs)
	}
	for _, word := range []string{"approve", "changes"} {
		if !strings.Contains(rv.Task, word) {
			bad("the review task does not name the verdict %q", word)
		}
	}

	// An agent that runs out of time must be noticed: both limits stay.
	if rv.Limit != 2*time.Hour {
		bad("review limit is %s, want 2h", rv.Limit)
	}

	fx := step("fix", KindAgent)
	if fx.Role != "Author" || !has(fx.Result, "head") || !fx.Sticky || fx.Next[ExitOut] != "review" {
		bad("fix: role %q result %v sticky %v next %q", fx.Role, fx.Result, fx.Sticky, fx.Next[ExitOut])
	}
	if fx.Limit != 3*time.Hour {
		bad("fix limit is %s, want 3h", fx.Limit)
	}

	vd := step("verdict", KindCondition)
	if vd.On != "review.verdict" || vd.MaxRounds != 3 {
		bad("verdict: on %q max_rounds %d", vd.On, vd.MaxRounds)
	}
	want := map[string]string{"approve": "card", "changes": "fix", "else": "stuck", "limit": "stuck"}
	for exit, to := range want {
		if vd.Next[exit] != to {
			bad("verdict exit %q leads to %q, want %q", exit, vd.Next[exit], to)
		}
	}
	if len(vd.Next) != len(want) {
		bad("verdict has %d exits, want %d: %v", len(vd.Next), len(want), vd.Next)
	}
	// A loop exit counts a round: only "changes" may lead back.
	if loops := LoopExits(d, "verdict"); len(loops) != 1 || loops[0] != "changes" {
		bad("verdict exits that count a round: %v, want only changes", loops)
	}

	card := step("card", KindApproval)
	if card.Who != "admin@covey.local" || !has(card.Options, "merge-ready", "changes") || len(card.Options) != 2 {
		bad("card: who %q options %v", card.Who, card.Options)
	}
	if card.Next["merge-ready"] != "ready" || card.Next["changes"] != "back" || len(card.Next) != 2 {
		bad("card leads to %v", card.Next)
	}
	stuck := step("stuck", KindApproval)
	if stuck.Who != "admin@covey.local" || !has(stuck.Options, "retry", "drop") || len(stuck.Options) != 2 {
		bad("stuck: who %q options %v", stuck.Who, stuck.Options)
	}
	if stuck.Next["retry"] != "again" || stuck.Next["drop"] != "dropped" || len(stuck.Next) != 2 {
		bad("stuck leads to %v", stuck.Next)
	}
	for _, id := range []string{"ready", "back", "again", "dropped"} {
		step(id, KindEnd)
	}

	// Every {{step.field}} in a text must be something that step reports. The
	// host gives a case of the gate only no and title; render leaves anything
	// else visible as {{case.x}}.
	caseFields := []string{"no", "title"}
	for _, id := range d.Order {
		s := d.Steps[id]
		for _, text := range []string{s.Task, s.Ask} {
			for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
				ref, field := m[1], m[2]
				if ref == "case" {
					if !has(caseFields, field) {
						bad("step %s reads {{case.%s}}, the host gives only %v", id, field, caseFields)
					}
					continue
				}
				if o := d.Steps[ref]; o == nil || !has(o.Result, field) {
					bad("step %s reads {{%s.%s}}, which no step reports", id, ref, field)
				}
			}
		}
	}
	// The condition must read a field its step reports.
	if o := d.Steps["review"]; o == nil || !has(o.Result, strings.TrimPrefix(vd.On, "review.")) {
		bad("verdict reads %q, which review does not report", vd.On)
	}
	return out
}

func TestThePRReviewDefinitionIsAsTheIssueSays(t *testing.T) {
	if f := prReviewFindings(prReviewSource(t)); len(f) > 0 {
		t.Fatalf("workflows/pr-review.workflow.yaml:\n  %s", strings.Join(f, "\n  "))
	}
}

// Taking any required key out, or mistyping it, must make the test fail.
func TestThePRReviewTestFailsWhenARequiredKeyIsMissing(t *testing.T) {
	src := prReviewSource(t)
	for _, c := range []struct{ name, old, new string }{
		{"no mode", "\nmode: watch\n", "\n"},
		{"mode steer", "mode: watch", "mode: steer"},
		{"no owner", "owner: \"admin@covey.local\"\n", ""},
		{"no only_if", "    only_if: \"github:pulls\"\n", ""},
		{"no as", "    as: \"covey-dev\"\n", ""},
		{"no every", "    every: \"10m\"\n", ""},
		{"no verdict result", "    result: [verdict, majors]\n", ""},
		{"no majors result", "[verdict, majors]", "[verdict]"},
		{"no head result", "    result: [head]\n", ""},
		{"no not_same_as", "    not_same_as: fix\n", ""},
		{"no sticky", "    sticky: true\n", ""},
		{"no max_rounds", "    max_rounds: 3\n", ""},
		{"no limit exit", "      \"limit\": stuck\n", ""},
		{"no else exit", "      \"else\": stuck\n", ""},
		{"approve loops back", "\"approve\": card", "\"approve\": fix"},
		{"no card who", "    title: \"Operator validation\"\n    who: \"admin@covey.local\"\n", "    title: \"Operator validation\"\n"},
		{"no stuck who", "    title: \"No approval\"\n    who: \"admin@covey.local\"\n", "    title: \"No approval\"\n"},
		{"no card option", "      \"changes\": back\n", ""},
		{"retry loops back", "\"retry\": again", "\"retry\": fix"},
		{"typo in the condition's field", "on: \"review.verdict\"", "on: \"review.verdikt\""},
		{"typo in a key", "    only_if:", "    onlyif:"},
		{"a key that does not exist", "    every: \"10m\"\n", "    every: \"10m\"\n    schedule: \"10m\"\n"},
		{"a placeholder nobody reports", "{{review.majors}}", "{{review.major}}"},
		{"a case field the host does not give (task)", "Review {{case.no}}.", "Review {{case.number}}."},
		{"a case field the host does not give (card)", "ask: \"{{case.no}} approved", "ask: \"{{case.number}} approved"},
		{"a verdict exit added", "      \"limit\": stuck\n", "      \"limit\": stuck\n      \"approved\": ready\n"},
		{"a timeout exit on the card", "      \"changes\": back\n", "      \"changes\": back\n      \"timeout\": dropped\n"},
		{"a timeout exit on stuck", "      \"drop\": dropped\n", "      \"drop\": dropped\n      \"timeout\": dropped\n"},
		{"no review limit", "    limit: \"2h\"\n", ""},
		{"no fix limit", "    limit: \"3h\"\n", ""},
	} {
		if !strings.Contains(src, c.old) {
			t.Errorf("%s: %q is not in the file, the case tests nothing", c.name, c.old)
			continue
		}
		if f := prReviewFindings(strings.Replace(src, c.old, c.new, 1)); len(f) == 0 {
			t.Errorf("%s: the findings stay empty", c.name)
		}
	}
}

// ---------- the rounds, walked through the engine ----------

func prReviewEngine(t *testing.T, mode Mode, w *world) *Engine {
	t.Helper()
	d, err := Parse([]byte(prReviewSource(t)))
	if err != nil {
		t.Fatal(err)
	}
	d.Mode = mode
	e, err := New(d, w, newClock().now)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func startPRReview(t *testing.T, e *Engine) string {
	t.Helper()
	r, err := e.Start("github:pull:jmarcone/covey#136", map[string]string{"no": "pull:jmarcone/covey#136", "title": "PR review"})
	must(t, err)
	return r.ID
}

func TestInWatchModeThePRReviewOnlyRecordsTheCase(t *testing.T) {
	w := &world{}
	e := prReviewEngine(t, ModeWatch, w)
	id := startPRReview(t, e)
	r := at(t, e, id)
	expect(t, r, "review", StateWatching)
	if len(w.tasks)+len(w.actions)+len(w.questions)+len(w.messages) != 0 {
		t.Fatalf("a watching run sent out tasks %v, actions %v, questions %v, messages %v", w.tasks, w.actions, w.questions, w.messages)
	}
	if got := logOf(r); got != "  trigger: started for github:pull:jmarcone/covey#136\n  review: seen here\n" {
		t.Errorf("log:\n%s", got)
	}
}

func TestAnApprovalInTheThirdRoundStillReachesTheOperator(t *testing.T) {
	w := &world{}
	e := prReviewEngine(t, ModeSteer, w)
	id := startPRReview(t, e)
	for round := 1; round <= 2; round++ {
		expect(t, at(t, e, id), "review", StateWorking)
		must(t, e.Done(id, map[string]string{"verdict": "changes", "majors": "2"}))
		expect(t, at(t, e, id), "fix", StateWorking)
		must(t, e.Done(id, map[string]string{"head": fmt.Sprintf("sha%d", round)}))
	}
	must(t, e.Done(id, map[string]string{"verdict": "approve", "majors": "0"}))
	expect(t, at(t, e, id), "card", StateWaiting)
	if len(w.questions) != 1 || w.questions[0].To != "admin@covey.local" || strings.Join(w.questions[0].Options, ",") != "merge-ready,changes" {
		t.Fatalf("questions = %+v", w.questions)
	}
	if got := w.handedOut(); got != "review:covey-reviewer fix:covey-dev review:covey-reviewer fix:covey-dev review:covey-reviewer" {
		t.Errorf("handed out %q", got)
	}

	must(t, e.Decide(id, "merge-ready"))
	expect(t, at(t, e, id), "ready", StateDone)
}

// The run ends at the operator's "changes"; the loop counter belongs to a run
// and is never reset. That a new run follows is the host's call, not this
// test's: the gate drops a PR that covey-bot commented last and the host takes
// a change seen in the first firing after the end as the run's own (see the
// header of the file), so Start is called by hand here and only shows that a
// new run, once there is one, has fresh rounds.
func TestTheOperatorsChangesEndTheRunAndANewRunStartsFreshRounds(t *testing.T) {
	w := &world{}
	e := prReviewEngine(t, ModeSteer, w)
	id := startPRReview(t, e)
	must(t, e.Done(id, map[string]string{"verdict": "approve", "majors": "0"}))
	must(t, e.Decide(id, "changes"))
	expect(t, at(t, e, id), "back", StateDone)
	if _, open := e.OpenRun("github:pull:jmarcone/covey#136"); open {
		t.Fatal("the case is still held by the finished run")
	}
	// A new run for the case, however it comes about, starts at one round.
	again := startPRReview(t, e)
	if again == id {
		t.Fatal("no new run")
	}
	expect(t, at(t, e, again), "review", StateWorking)
	if n := at(t, e, again).Loops["verdict"]; n != 0 {
		t.Errorf("the new run starts with %d rounds used", n)
	}
}

func TestThreeRoundsWithoutApprovalReachTheLimitCard(t *testing.T) {
	w := &world{}
	e := prReviewEngine(t, ModeSteer, w)
	id := startPRReview(t, e)
	for round := 1; round <= 2; round++ {
		must(t, e.Done(id, map[string]string{"verdict": "changes", "majors": "1"}))
		expect(t, at(t, e, id), "fix", StateWorking)
		must(t, e.Done(id, map[string]string{"head": fmt.Sprintf("sha%d", round)}))
	}
	// Three reviews, two fix loops: the third "changes" is the limit.
	must(t, e.Done(id, map[string]string{"verdict": "changes", "majors": "1"}))
	expect(t, at(t, e, id), "stuck", StateWaiting)
	if got := w.handedOut(); got != "review:covey-reviewer fix:covey-dev review:covey-reviewer fix:covey-dev review:covey-reviewer" {
		t.Errorf("handed out %q", got)
	}
	if q := w.questions[len(w.questions)-1]; q.Step != "stuck" || q.To != "admin@covey.local" || strings.Join(q.Options, ",") != "retry,drop" {
		t.Errorf("question = %+v", q)
	}

	// Retry from inside the loop would buy no round, so it ends the run.
	must(t, e.Decide(id, "retry"))
	expect(t, at(t, e, id), "again", StateDone)
}

func TestDroppingAtTheLimitCardEndsTheRun(t *testing.T) {
	w := &world{}
	e := prReviewEngine(t, ModeSteer, w)
	id := startPRReview(t, e)
	for round := 1; round <= 2; round++ {
		must(t, e.Done(id, map[string]string{"verdict": "changes", "majors": "1"}))
		must(t, e.Done(id, map[string]string{"head": "sha"}))
	}
	must(t, e.Done(id, map[string]string{"verdict": "changes", "majors": "1"}))
	must(t, e.Decide(id, "drop"))
	expect(t, at(t, e, id), "dropped", StateDone)
}

func TestAVerdictOtherThanApproveOrChangesGoesToTheLimitCard(t *testing.T) {
	for _, verdict := range []string{"lgtm", "", "Approve", "approve with comments"} {
		w := &world{}
		e := prReviewEngine(t, ModeSteer, w)
		id := startPRReview(t, e)
		must(t, e.Done(id, map[string]string{"verdict": verdict, "majors": "0"}))
		r := at(t, e, id)
		if r.State == StateFailed {
			t.Fatalf("verdict %q failed the run:\n%s", verdict, logOf(r))
		}
		expect(t, r, "stuck", StateWaiting)
	}
}
