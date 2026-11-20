package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/observability"
	"covey/internal/secrets"
)

// The poll's token in these tests: any line of the log or any error that
// holds it is a leak.
const meterPollToken = "sk-ant-oat01-geheim-meter-token"

const (
	meterPollInit      = `{"type":"system","subtype":"init","session_id":"s2","model":"claude-haiku-4-5-20251001","tools":[],"mcp_servers":[]}`
	meterPollAssistant = `{"type":"assistant","session_id":"s2","message":{"model":"claude-haiku-4-5-20251001","role":"assistant","content":[{"type":"text","text":"ok"}]}}`
	meterPollEvent     = `{"type":"rate_limit_event","session_id":"s2","rate_limit_info":{"status":"allowed_warning","unifiedWindows":{"five_hour":{"status":"allowed_warning","resetsAt":1795100000,"utilization":0.41},"seven_day":{"status":"allowed","resetsAt":1795190400,"utilization":0.135}}}}`
	meterPollResult    = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"s2","total_cost_usd":0.0131,` +
		`"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":3,"outputTokens":5,"cacheReadInputTokens":12000,"cacheCreationInputTokens":9800,"costUSD":0.0123},` +
		`"claude-sonnet-5-5":{"inputTokens":40,"outputTokens":2,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.0008}}}`
)

// meterPollStream is one call's stdout as the CLI prints it, with a line that
// is not JSON among it.
func meterPollStream(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

var meterPollFull = meterPollStream(meterPollInit, meterPollAssistant, meterPollEvent, "warning: not json", meterPollResult)

type fakeMeterUpsert struct {
	org      uuid.UUID
	source   string
	link     observability.MeterLinkChange
	rows     []observability.ExternalRow
	reported map[string]time.Time
	now      time.Time
}

type fakeMeterFiled struct {
	org     uuid.UUID
	cred    observability.CredentialRef
	payload string
	at      time.Time
}

// fakeMeterStore records what the poll asks of the store, in order.
type fakeMeterStore struct {
	mu        sync.Mutex
	ops       []string
	due       []observability.MeterPollCandidate
	dueErr    error
	upsertErr error
	fileErr   error
	advanced  []time.Time
	upserts   []fakeMeterUpsert
	filed     []fakeMeterFiled
}

func (f *fakeMeterStore) MeterPollDue(_ context.Context, _ time.Time, fresh, active time.Duration) ([]observability.MeterPollCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, fmt.Sprintf("due %s %s", fresh, active))
	return f.due, f.dueErr
}

func (f *fakeMeterStore) AddPolledReadings(_ context.Context, org uuid.UUID, cred observability.CredentialRef,
	payload json.RawMessage, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "file")
	if f.fileErr != nil {
		return f.fileErr
	}
	f.filed = append(f.filed, fakeMeterFiled{org, cred, string(payload), at})
	return nil
}

func (f *fakeMeterStore) AdvanceMeterPollMarks(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "advance")
	f.advanced = append(f.advanced, now)
	return nil
}

func (f *fakeMeterStore) UpsertExternalUsage(_ context.Context, org uuid.UUID, source string, link observability.MeterLinkChange,
	rows []observability.ExternalRow, reported map[string]time.Time, now time.Time) (observability.ExternalUploadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "book")
	f.upserts = append(f.upserts, fakeMeterUpsert{org, source, link, rows, reported, now})
	return observability.ExternalUploadResult{}, f.upsertErr
}

// fakeMeterRunner answers every call with out and err, and keeps the tokens
// and, where clock is set, the instant of each call.
type fakeMeterRunner struct {
	mu     sync.Mutex
	out    []byte
	err    error
	tokens []string
	clock  func() time.Time
	at     []time.Time
}

func (r *fakeMeterRunner) Run(_ context.Context, token string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, token)
	if r.clock != nil {
		r.at = append(r.at, r.clock())
	}
	return r.out, r.err
}

func (r *fakeMeterRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}

// meterSecrets holds values by "key#slot"; every other method of the
// interface panics, which is the louder answer.
type meterSecrets struct {
	secrets.Store
	values map[string]string
}

func (s meterSecrets) Value(_ context.Context, _ uuid.UUID, key string, slot int) (string, error) {
	v, ok := s.values[key+"#"+strconv.Itoa(slot)]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

type meterPollRig struct {
	o      *Orchestrator
	store  *fakeMeterStore
	runner *fakeMeterRunner
	log    *bytes.Buffer
	now    time.Time
	cand   observability.MeterPollCandidate
}

func newMeterPollRig(t *testing.T, value string, out []byte, runErr error) *meterPollRig {
	t.Helper()
	r := &meterPollRig{
		store:  &fakeMeterStore{},
		runner: &fakeMeterRunner{out: out, err: runErr},
		log:    &bytes.Buffer{},
		now:    time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	r.cand = observability.MeterPollCandidate{OrgID: uuid.New(), Reason: observability.MeterPollOutside,
		Credential: observability.CredentialRef{RuntimeID: uuid.New(), Ord: 1, SecretKey: "claude_code_oauth_token",
			SecretSlot: 2, Kind: "subscription"}}
	r.store.due = []observability.MeterPollCandidate{r.cand}
	r.o = &Orchestrator{Options: Options{
		Log:       slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Secrets:   meterSecrets{values: map[string]string{"claude_code_oauth_token#2": value}},
		MeterPoll: r.runner,
	}}
	r.o.meterPoll.store = r.store
	r.o.meterPoll.now = func() time.Time { return r.now }
	return r
}

// The call is booked before its reading is filed: the split waits on the
// poll source's watermark, and must never see a reading whose call is not on
// the books. The booking is one row per model under the seat's own poll
// source, linked to the seat, from the poll's host, with the host's
// watermark at the call.
func TestMeterPollBooksThenFiles(t *testing.T) {
	r := newMeterPollRig(t, " "+meterPollToken+"\n", meterPollFull, nil)
	r.o.PollMeters(context.Background())

	want := []string{"advance", "due 10m0s 1h0m0s", "book", "file"}
	if !reflect.DeepEqual(r.store.ops, want) {
		t.Fatalf("ops = %v, want %v", r.store.ops, want)
	}
	if !reflect.DeepEqual(r.runner.tokens, []string{meterPollToken}) {
		t.Errorf("the runner got %q, want the trimmed value", r.runner.tokens)
	}
	up := r.store.upserts[0]
	if up.org != r.cand.OrgID || up.source != "meter poll: claude_code_oauth_token#2" ||
		up.source != observability.MeterPollSourceFor("claude_code_oauth_token", 2) {
		t.Errorf("booked under %v / %q", up.org, up.source)
	}
	if !up.link.Set || up.link.Credential == nil ||
		*up.link.Credential != (observability.MeterCredential{SecretKey: "claude_code_oauth_token", SecretSlot: 2}) {
		t.Errorf("link = %+v", up.link)
	}
	if !reflect.DeepEqual(up.reported, map[string]time.Time{observability.MeterPollHost: r.now}) || !up.now.Equal(r.now) {
		t.Errorf("reported = %v, now = %v", up.reported, up.now)
	}
	if len(up.rows) != 2 {
		t.Fatalf("%d rows, want one per model: %+v", len(up.rows), up.rows)
	}
	haiku, sonnet := up.rows[0], up.rows[1]
	if haiku.Model != "claude-haiku-4-5-20251001" || sonnet.Model != "claude-sonnet-5-5" {
		t.Fatalf("models %q, %q", haiku.Model, sonnet.Model)
	}
	if haiku.Host != observability.MeterPollHost || haiku.SessionID != "s2" ||
		haiku.MessageID != "s2/claude-haiku-4-5-20251001" || sonnet.MessageID != "s2/claude-sonnet-5-5" ||
		haiku.TS == nil || !haiku.TS.Equal(r.now) {
		t.Errorf("haiku row = %+v", haiku)
	}
	if *haiku.Input != 3 || *haiku.Output != 5 || *haiku.CacheRead != 12000 || *haiku.CacheWrite5m != 9800 ||
		*haiku.CacheWrite1h != 0 || haiku.ReportedUSD == nil || *haiku.ReportedUSD != 0.0123 {
		t.Errorf("haiku figures = in %d out %d cr %d w5 %d w1 %d usd %v", *haiku.Input, *haiku.Output,
			*haiku.CacheRead, *haiku.CacheWrite5m, *haiku.CacheWrite1h, haiku.ReportedUSD)
	}
	if err := observability.ValidateExternalRows(up.rows, r.now); err != nil {
		t.Errorf("the booking would be refused: %v", err)
	}
	f := r.store.filed[0]
	if f.org != r.cand.OrgID || f.cred != r.cand.Credential || f.payload != meterPollEvent || !f.at.Equal(r.now) {
		t.Errorf("filed %+v, want the reading at the booking's instant %v", f, r.now)
	}
	if strings.Contains(r.log.String(), meterPollToken) {
		t.Errorf("the token reached the log: %s", r.log.String())
	}
}

// Without a result line, or with one that names no model's usage, the call
// cannot be booked, and so its reading is not filed either: no booking, no
// reading. Said once.
func TestMeterPollWithoutResultFilesNothing(t *testing.T) {
	for name, out := range map[string][]byte{
		"no result line": meterPollStream(meterPollInit, meterPollEvent),
		"no modelUsage":  meterPollStream(meterPollInit, meterPollEvent, `{"type":"result","session_id":"s2"}`),
		"empty usage":    meterPollStream(meterPollInit, meterPollEvent, `{"type":"result","session_id":"s2","modelUsage":{}}`),
	} {
		t.Run(name, func(t *testing.T) {
			r := newMeterPollRig(t, meterPollToken, out, nil)
			start := r.now
			for i := 0; i < 2; i++ {
				r.now = start.Add(time.Duration(i) * 11 * time.Minute)
				r.o.PollMeters(context.Background())
			}
			if r.runner.calls() != 2 {
				t.Fatalf("%d calls, want 2", r.runner.calls())
			}
			if len(r.store.upserts) != 0 || len(r.store.filed) != 0 {
				t.Errorf("booked %d and filed %d without a result", len(r.store.upserts), len(r.store.filed))
			}
			logged := r.log.String()
			if n := strings.Count(logged, "reason=no_result"); n != 1 {
				t.Errorf("no_result said %d times, want once: %s", n, logged)
			}
		})
	}
}

// A call that failed files nothing, and its error — even one that carries
// the token — reaches the log scrubbed.
func TestMeterPollRunnerErrorFilesNothing(t *testing.T) {
	r := newMeterPollRig(t, meterPollToken, meterPollFull, errors.New("claude said: bad token "+meterPollToken))
	r.o.PollMeters(context.Background())
	if len(r.store.upserts) != 0 || len(r.store.filed) != 0 {
		t.Errorf("a failed call booked %d and filed %d", len(r.store.upserts), len(r.store.filed))
	}
	logged := r.log.String()
	if strings.Contains(logged, meterPollToken) {
		t.Errorf("the token reached the log: %s", logged)
	}
	if !strings.Contains(logged, "meter poll: call failed") || !strings.Contains(logged, "[redacted]") {
		t.Errorf("the failure was not said: %s", logged)
	}
	if strings.Contains(logged, "claude_code_oauth_token") {
		t.Errorf("the secret's name reached the log: %s", logged)
	}
}

// A booking the store refuses keeps the reading back: filed alone, it would
// be split as if the call had cost nothing.
func TestMeterPollUnbookedCallFilesNothing(t *testing.T) {
	r := newMeterPollRig(t, meterPollToken, meterPollFull, nil)
	r.store.upsertErr = errors.New("refused")
	r.o.PollMeters(context.Background())
	if len(r.store.filed) != 0 {
		t.Errorf("a reading was filed for an unbooked call")
	}
}

// A store error may quote the source, and the source holds the secret's name:
// both are replaced before the error reaches the log, on the booking and on
// the filing.
func TestMeterPollStoreErrorsKeepTheSecretNameOut(t *testing.T) {
	key := "claude_code_oauth_token"
	long := strings.Repeat("k", 60)
	for name, c := range map[string]struct {
		key      string
		book     bool
		wantLine string
	}{
		"booking":          {key, true, "meter poll: call not booked"},
		"filing":           {key, false, "meter poll: reading not filed"},
		"booking long key": {long, true, "meter poll: call not booked"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newMeterPollRig(t, meterPollToken, meterPollFull, nil)
			r.cand.Credential.SecretKey = c.key
			r.store.due = []observability.MeterPollCandidate{r.cand}
			r.o.Secrets = meterSecrets{values: map[string]string{c.key + "#2": meterPollToken}}
			source := observability.MeterPollSourceFor(c.key, 2)
			err := fmt.Errorf(`duplicate key (org_id, source)=(x, %s) for secret %q`, source, c.key)
			if c.book {
				r.store.upsertErr = err
			} else {
				r.store.fileErr = err
			}
			r.o.PollMeters(context.Background())
			logged := r.log.String()
			if !strings.Contains(logged, c.wantLine) || !strings.Contains(logged, "[secret]") {
				t.Fatalf("the failure was not said: %s", logged)
			}
			// A long key's source holds only part of it; neither may pass.
			if strings.Contains(logged, c.key) || strings.Contains(logged, c.key[:20]) {
				t.Errorf("the secret's name reached the log: %s", logged)
			}
		})
	}
}

// One seat is tried at most once in ten minutes by this process, whatever
// came of the try: a call that fails is not repeated every minute.
func TestMeterPollRetriesASeatAfterTenMinutes(t *testing.T) {
	r := newMeterPollRig(t, meterPollToken, nil, errors.New("exit status 1"))
	start := r.now
	r.o.PollMeters(context.Background())
	if n := r.runner.calls(); n != 1 {
		t.Fatalf("%d calls in the first round", n)
	}
	r.now = start.Add(5 * time.Minute)
	r.o.PollMeters(context.Background())
	if n := r.runner.calls(); n != 1 {
		t.Errorf("tried again after 5 minutes (%d calls)", n)
	}
	r.now = start.Add(11 * time.Minute)
	r.o.PollMeters(context.Background())
	if n := r.runner.calls(); n != 2 {
		t.Errorf("%d calls after 11 minutes, want 2", n)
	}
}

// Only a Claude OAuth token (sk-ant-oat…) is handed to the CLI. An API key,
// another engine's subscription secret (a Codex auth.json, a gateway token)
// or anything else filed as a subscription is never run, however often the
// seat comes up, and is said once.
func TestMeterPollNeverRunsWhatIsNotAnOAuthToken(t *testing.T) {
	for name, value := range map[string]string{
		"api key":        "sk-ant-api03-schluessel",
		"codex auth":     `{"OPENAI_API_KEY":null,"tokens":{"access_token":"eyJhbGciOi","refresh_token":"rt_abc"}}`,
		"gateway token":  "edu_live_9f8e7d6c5b4a",
		"bare prefix":    "sk-ant-oa",
		"prefix inside":  "Bearer sk-ant-oat01-abc",
		"other provider": "sk-proj-abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			r := newMeterPollRig(t, value, meterPollFull, nil)
			start := r.now
			for i := 0; i < 50; i++ {
				r.now = start.Add(time.Duration(i) * time.Hour)
				r.o.PollMeters(context.Background())
			}
			if n := r.runner.calls(); n != 0 {
				t.Errorf("%d calls with a value that is no Claude OAuth token", n)
			}
			logged := r.log.String()
			if n := strings.Count(logged, "reason=not_oauth"); n != 1 {
				t.Errorf("not_oauth said %d times, want once: %s", n, logged)
			}
			if strings.Contains(logged, value) {
				t.Errorf("the value reached the log")
			}
		})
	}
}

// An API key filed as a subscription has no meter to read; a seat whose
// value is gone neither. Neither is called, and each is said once.
func TestMeterPollSkipsWhatIsNoSubscriptionToken(t *testing.T) {
	for name, value := range map[string]string{"api key": "sk-ant-api03-schluessel", "no value": ""} {
		t.Run(name, func(t *testing.T) {
			r := newMeterPollRig(t, value, meterPollFull, nil)
			if value == "" {
				r.o.Secrets = meterSecrets{values: map[string]string{}}
			}
			start := r.now
			for i := 0; i < 3; i++ {
				r.now = start.Add(time.Duration(i) * 11 * time.Minute)
				r.o.PollMeters(context.Background())
			}
			if n := r.runner.calls(); n != 0 {
				t.Errorf("%d calls", n)
			}
			if n := strings.Count(r.log.String(), "meter poll: seat not read"); n != 1 {
				t.Errorf("%d warnings, want one: %s", n, r.log.String())
			}
			if value != "" && strings.Contains(r.log.String(), value) {
				t.Errorf("the value reached the log")
			}
		})
	}
}

// The watermarks move every round, with no seat due and when the question
// for due seats fails; that failure ends the round, not the loop.
func TestMeterPollAdvancesMarksEveryRound(t *testing.T) {
	r := newMeterPollRig(t, meterPollToken, meterPollFull, nil)
	r.store.due = nil
	start := r.now
	r.o.PollMeters(context.Background())
	r.now = start.Add(time.Minute)
	r.store.dueErr = errors.New("db gone")
	r.o.PollMeters(context.Background())
	if !reflect.DeepEqual(r.store.advanced, []time.Time{start, start.Add(time.Minute)}) {
		t.Errorf("advanced at %v", r.store.advanced)
	}
	if r.runner.calls() != 0 {
		t.Errorf("called without a seat due")
	}
}

// Without a runner (no claude CLI here, or COVEY_METER_POLL=off), or without
// secrets, no seat is asked for or read, but the watermarks still move every
// round: a poll source booked before the poll was switched off would
// otherwise hold its seat's later readings back for as long as its host
// counts as active.
func TestMeterPollOffStillAdvancesMarks(t *testing.T) {
	for name, off := range map[string]func(o *Orchestrator){
		"no runner":  func(o *Orchestrator) { o.MeterPoll = nil },
		"no secrets": func(o *Orchestrator) { o.Secrets = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := newMeterPollRig(t, meterPollToken, meterPollFull, nil)
			off(r.o)
			start := r.now
			r.o.PollMeters(context.Background())
			r.now = start.Add(time.Minute)
			r.o.PollMeters(context.Background())
			if !reflect.DeepEqual(r.store.ops, []string{"advance", "advance"}) {
				t.Errorf("ops = %v, want the watermarks advanced and nothing else", r.store.ops)
			}
			if !reflect.DeepEqual(r.store.advanced, []time.Time{start, start.Add(time.Minute)}) {
				t.Errorf("advanced at %v", r.store.advanced)
			}
			if r.runner.calls() != 0 {
				t.Errorf("called with the poll off")
			}
		})
	}
	r := newMeterPollRig(t, meterPollToken, meterPollFull, nil)
	none := &Orchestrator{Options: Options{MeterPoll: r.runner, Log: slog.Default()}}
	none.PollMeters(context.Background()) // neither Obs nor Secrets: no panic, no call
	if r.runner.calls() != 0 {
		t.Errorf("called without stores")
	}
}

// meterPollGaps polls once a minute from the rig's now for d and returns the
// gaps between the runner's calls.
func meterPollGaps(r *meterPollRig, d time.Duration) []time.Duration {
	start := r.now
	for m := time.Duration(0); m <= d; m += time.Minute {
		r.now = start.Add(m)
		r.o.PollMeters(context.Background())
	}
	var gaps []time.Duration
	for i := 1; i < len(r.runner.at); i++ {
		gaps = append(gaps, r.runner.at[i].Sub(r.runner.at[i-1]))
	}
	return gaps
}

// A seat whose tries file no reading is tried less and less often: the wait
// doubles from ten minutes with each such try in a row, up to six hours, and
// stays there. A call that fails and a call without a rate_limit_event count
// alike; the warning for a reason is said once.
func TestMeterPollBacksOff(t *testing.T) {
	want := []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute,
		160 * time.Minute, 320 * time.Minute, 6 * time.Hour, 6 * time.Hour}
	for name, rig := range map[string]func(t *testing.T) *meterPollRig{
		"call fails": func(t *testing.T) *meterPollRig {
			return newMeterPollRig(t, meterPollToken, nil, errors.New("exit status 1"))
		},
		"no event": func(t *testing.T) *meterPollRig {
			return newMeterPollRig(t, meterPollToken, meterPollStream(meterPollInit, meterPollResult), nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := rig(t)
			r.runner.clock = func() time.Time { return r.now }
			gaps := meterPollGaps(r, 1630*time.Minute)
			if !reflect.DeepEqual(gaps, want) {
				t.Errorf("gaps = %v, want %v", gaps, want)
			}
			if name == "no event" {
				if n := strings.Count(r.log.String(), "reason=no_rate_limit_event"); n != 1 {
					t.Errorf("no_rate_limit_event said %d times, want once", n)
				}
			}
		})
	}
}

// A try that files a reading starts the wait over: after a run of failures
// the seat is back to one try in ten minutes as soon as one call reads it.
func TestMeterPollBackoffResetsOnAReading(t *testing.T) {
	r := newMeterPollRig(t, meterPollToken, nil, errors.New("exit status 1"))
	r.runner.clock = func() time.Time { return r.now }
	// Tries at 0, 10, 30 and 70 minutes, all failing; the next is due at 150.
	meterPollGaps(r, 100*time.Minute)
	if n := r.runner.calls(); n != 4 {
		t.Fatalf("%d calls in the failing stretch, want 4", n)
	}
	r.runner.mu.Lock()
	r.runner.out, r.runner.err = meterPollFull, nil
	r.runner.mu.Unlock()
	r.now = r.now.Add(time.Minute)
	gaps := meterPollGaps(r, 80*time.Minute)
	// 150 is the first read; 160, 170 and 180 follow: the wait is ten
	// minutes again.
	want := []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute,
		10 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	if !reflect.DeepEqual(gaps, want) {
		t.Errorf("gaps = %v, want %v", gaps, want)
	}
	if len(r.store.filed) != 4 {
		t.Errorf("%d readings filed, want 4", len(r.store.filed))
	}
}

func TestParseMeterPoll(t *testing.T) {
	events, usage := parseMeterPoll(meterPollFull)
	if len(events) != 1 || string(events[0]) != meterPollEvent {
		t.Errorf("events = %q", events)
	}
	if usage == nil || usage.SessionID != "s2" || len(usage.Models) != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	if m := usage.Models["claude-sonnet-5-5"]; m.Input != 40 || m.Output != 2 || !m.HasCost || m.CostUSD != 0.0008 {
		t.Errorf("sonnet = %+v", m)
	}

	// Junk only: nothing.
	if ev, u := parseMeterPoll([]byte("not json\n{broken\n\n[1,2]\n")); ev != nil || u != nil {
		t.Errorf("junk gave %q, %+v", ev, u)
	}
	// A line that names the event in its text but is of another type is
	// not a reading.
	if ev, _ := parseMeterPoll([]byte(`{"type":"assistant","text":"\"rate_limit_event\" \"rate_limit_info\""}`)); ev != nil {
		t.Errorf("an assistant line was taken for a reading: %q", ev)
	}
}

// The cache write is split by lifetime where the CLI sends the split, and a
// figure that is not a number counts 0, not as an error. A result without a
// session id is booked under a fresh one.
func TestParseMeterPollFigures(t *testing.T) {
	_, usage := parseMeterPoll([]byte(`{"type":"result","modelUsage":{` +
		`"m1":{"inputTokens":"7","outputTokens":2.0,"cacheCreationInputTokens":300,` +
		`"cacheCreationEphemeral5mInputTokens":100,"cacheCreationEphemeral1hInputTokens":200},` +
		`"m2":"not an object","m3":{"outputTokens":-5,"costUSD":"x"}}}`))
	if usage == nil {
		t.Fatal("no usage")
	}
	m1 := usage.Models["m1"]
	if m1.Input != 0 || m1.Output != 2 || m1.CacheWrite5m != 100 || m1.CacheWrite1h != 200 || m1.HasCost {
		t.Errorf("m1 = %+v", m1)
	}
	if _, ok := usage.Models["m2"]; ok {
		t.Error("an entry that is no object was kept")
	}
	if m3 := usage.Models["m3"]; m3.Output != 0 || m3.HasCost {
		t.Errorf("m3 = %+v", m3)
	}
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	rows := usage.rows(now)
	if len(rows) != 2 || rows[0].SessionID == "" || rows[0].SessionID != rows[1].SessionID ||
		rows[0].MessageID != rows[0].SessionID+"/m1" || rows[0].ReportedUSD != nil {
		t.Errorf("rows = %+v", rows)
	}
	if err := observability.ValidateExternalRows(rows, now); err != nil {
		t.Errorf("rows refused: %v", err)
	}
	var none *meterPollUsage
	if none.rows(now) != nil {
		t.Error("rows without a result line")
	}
}

// fakeClaude writes an executable script standing in for the CLI.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The CLI gets the poll's arguments, a fresh directory as HOME, config
// directory and working directory (gone afterwards), and an environment of
// exactly the named variables — the token among them and nowhere on the
// command line.
func TestClaudeMeterPollRunsTheCLI(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-must-not-pass")
	bin := fakeClaude(t, `printf 'ARG %s\n' "$@"
echo "CWD $(pwd)"
env | sed 's/^/ENV /'
echo "noise $CLAUDE_CODE_OAUTH_TOKEN" >&2`)
	out, err := ClaudeMeterPoll{Binary: bin}.Run(context.Background(), meterPollToken)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	env := map[string]string{}
	var cwd string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch {
		case strings.HasPrefix(line, "ARG "):
			args = append(args, strings.TrimPrefix(line, "ARG "))
		case strings.HasPrefix(line, "ENV "):
			k, v, _ := strings.Cut(strings.TrimPrefix(line, "ENV "), "=")
			env[k] = v
		case strings.HasPrefix(line, "CWD "):
			cwd = strings.TrimPrefix(line, "CWD ")
		}
	}
	wantArgs := []string{"-p", "ok", "--model", "haiku", "--output-format", "stream-json", "--verbose",
		"--max-turns", "1", "--tools", "", "--system-prompt", "Answer with one word.", "--no-session-persistence"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %q", args)
	}
	for _, a := range args {
		if strings.Contains(a, meterPollToken) {
			t.Error("the token is on the command line")
		}
	}
	home := env["HOME"]
	if home == "" || env["CLAUDE_CONFIG_DIR"] != home || cwd != home || !strings.HasPrefix(filepath.Base(home), "covey-meter-poll-") {
		t.Errorf("HOME %q, CLAUDE_CONFIG_DIR %q, cwd %q", home, env["CLAUDE_CONFIG_DIR"], cwd)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the call's directory %s is still there (%v)", home, err)
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != meterPollToken || env["DISABLE_AUTOUPDATER"] != "1" ||
		env["DISABLE_TELEMETRY"] != "1" || env["PATH"] != os.Getenv("PATH") {
		t.Errorf("env = %v", env)
	}
	// The shell adds its own bookkeeping (PWD, SHLVL, _); nothing else passes.
	allowed := map[string]bool{"PATH": true, "HOME": true, "CLAUDE_CONFIG_DIR": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
		"DISABLE_AUTOUPDATER": true, "DISABLE_TELEMETRY": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}
	for k := range env {
		if !allowed[k] {
			t.Errorf("%s reached the CLI", k)
		}
	}
	if strings.Contains(string(out), "noise") {
		t.Error("stderr reached the output")
	}
}

// A failing CLI comes back as its exit status: not its stderr, not the token.
func TestClaudeMeterPollErrorIsTheExitStatus(t *testing.T) {
	bin := fakeClaude(t, `echo "invalid token $CLAUDE_CODE_OAUTH_TOKEN" >&2
echo "{\"token\":\"$CLAUDE_CODE_OAUTH_TOKEN\"}"
exit 3`)
	out, err := ClaudeMeterPoll{Binary: bin}.Run(context.Background(), meterPollToken)
	if err == nil || out != nil {
		t.Fatalf("out %q, err %v", out, err)
	}
	if err.Error() != "meter poll: claude: exit status 3" {
		t.Errorf("err = %q", err)
	}
	if _, err := (ClaudeMeterPoll{}).Run(context.Background(), meterPollToken); err == nil {
		t.Error("ran without a binary")
	}
}

// A CLI that hangs is killed at the deadline, and its directory goes too.
func TestClaudeMeterPollTimeoutKills(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "home")
	bin := fakeClaude(t, `echo "$HOME" > `+marker+`
exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ClaudeMeterPoll{Binary: bin}.Run(ctx, meterPollToken)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("returned after %s", d)
	}
	home, _ := os.ReadFile(marker)
	if h := strings.TrimSpace(string(home)); h == "" {
		t.Error("the script did not run")
	} else if _, err := os.Stat(h); !os.IsNotExist(err) {
		t.Errorf("the call's directory %s is still there", h)
	}
}

// stdout is kept up to the cap and the rest dropped, without failing the CLI.
func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{max: 5}
	for _, p := range []string{"abc", "defg", "hij"} {
		if n, err := b.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if b.String() != "abcde" {
		t.Errorf("kept %q", b.String())
	}
}

func TestMeterPollFor(t *testing.T) {
	found := func(string) (string, error) { return "/usr/local/bin/claude", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }

	for _, s := range []string{"off", "OFF", " Off ", "false", "False", "no", "NO", "0"} {
		if r, note := MeterPollFor(s, found); r != nil || note != "" {
			t.Errorf("%q = %v, %q, want off without a note", s, r, note)
		}
	}
	if r, note := MeterPollFor("auto", missing); r != nil || !strings.Contains(note, "COVEY_METER_POLL") {
		t.Errorf("auto without claude = %v, %q", r, note)
	}
	for _, s := range []string{"auto", "", " AUTO "} {
		if r, note := MeterPollFor(s, found); r != (ClaudeMeterPoll{Binary: "/usr/local/bin/claude"}) || note != "" {
			t.Errorf("%q with claude = %v, %q", s, r, note)
		}
	}
	// A path is looked up once, as given, and run as the lookup resolved it.
	var asked []string
	echo := func(p string) (string, error) { asked = append(asked, p); return p, nil }
	if r, note := MeterPollFor(" /opt/claude/bin/claude ", echo); r != (ClaudeMeterPoll{Binary: "/opt/claude/bin/claude"}) || note != "" {
		t.Errorf("a path = %v, %q", r, note)
	}
	if !reflect.DeepEqual(asked, []string{"/opt/claude/bin/claude"}) {
		t.Errorf("looked up %q", asked)
	}
	// A path that is no executable switches the poll off, and the note does
	// not repeat it: whatever was typed there stays out of the log.
	secretish := "/home/ada/sk-ant-oat01-oops/claude"
	r, note := MeterPollFor(secretish, missing)
	if r != nil || !strings.Contains(note, "meter poll off") || !strings.Contains(note, "COVEY_METER_POLL") {
		t.Errorf("a missing path = %v, %q", r, note)
	}
	if strings.Contains(note, "sk-ant") || strings.Contains(note, "ada") {
		t.Errorf("the note echoes the setting: %q", note)
	}
}

// The real lookup: an executable file passes, a file that is not executable
// and a path that does not exist switch the poll off.
func TestMeterPollForChecksThePath(t *testing.T) {
	bin := fakeClaude(t, "exit 0")
	if r, note := MeterPollFor(bin, exec.LookPath); r != (ClaudeMeterPoll{Binary: bin}) || note != "" {
		t.Errorf("an executable = %v, %q", r, note)
	}
	plain := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(plain, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{plain, filepath.Join(t.TempDir(), "missing", "claude")} {
		if r, note := MeterPollFor(p, exec.LookPath); r != nil || note == "" {
			t.Errorf("%s = %v, %q, want off with a note", p, r, note)
		}
	}
}
