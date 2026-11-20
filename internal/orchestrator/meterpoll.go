package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"covey/internal/claudeapi"
	"covey/internal/observability"
	"covey/internal/secrets"
)

// covey's own meter poll (#153).
//
// The subscription meter is read in band only: a rate_limit_event in the
// stream of a run (handleDaemonMessage → AddInbandReadings). While the fleet
// is paused or idle nothing streams, the meter goes unread, and the outside
// sessions on the same seat (#120) have no step to take their share of. The
// poll closes that gap from the control plane: for every seat the store says
// is due (observability.MeterPollDue), it runs the cheapest call that makes
// Claude Code print a rate_limit_event — one Haiku turn — and files what it
// read.
//
// The order is fixed: the call is BOOKED first, under a source of its own
// ("meter poll: <secret>#<slot>", linked to the seat it read), and the reading
// is filed after. The split waits on each linked source's watermark; filed the
// other way round, a reading could be split before the call that moved the
// meter is on the books.
//
// The call runs the claude CLI on the control plane's machine
// (COVEY_METER_POLL). The distroless image has none, and there the poll is off
// and says so once at start. The token goes into the child's environment and
// nowhere else: not into a log line, not into an error, not into a row.
//
// With the poll off the loop still runs: the watermarks of poll sources booked
// earlier move up every round (AdvanceMeterPollMarks), or those sources would
// hold their seats' later readings back from the split for good.

const (
	// meterPollEvery: how often the store is asked which seats are due. The
	// asking is cheap; what is run is bounded by meterPollRetry and
	// meterPollMaxWait.
	meterPollEvery = time.Minute
	// meterPollDelay: the first round waits, like the billing sync, so that a
	// restart does not open with a burst of model calls.
	meterPollDelay = 3 * time.Minute
	// meterPollFresh: a seat with a supported reading younger than this is
	// not due.
	meterPollFresh = 10 * time.Minute
	// meterPollActive: an outside source counts as active when its host
	// pushed within this.
	meterPollActive = time.Hour
	// meterPollRetry: this process tries one seat at most once in this time,
	// whatever came of the last try — a call that fails is not repeated every
	// minute. Each further try in a row that files no reading doubles the
	// wait, up to meterPollMaxWait; a try that files one starts over.
	meterPollRetry = 10 * time.Minute
	// meterPollMaxWait caps that wait: a seat that never yields a reading (a
	// revoked token, a CLI that prints no rate_limit_event) is still tried
	// four times a day, not every ten minutes for ever.
	meterPollMaxWait = 6 * time.Hour
	// meterPollTimeout bounds one call of the CLI.
	meterPollTimeout = 60 * time.Second
	// meterPollMaxOut caps what is kept of the CLI's stdout. One Haiku turn
	// prints a few kilobytes; anything past the cap is dropped, not buffered.
	meterPollMaxOut = 1 << 20
)

// meterPollStore is what the poll needs of the observability store; tests
// stand in a fake.
type meterPollStore interface {
	MeterPollDue(ctx context.Context, now time.Time, fresh, active time.Duration) ([]observability.MeterPollCandidate, error)
	AddPolledReadings(ctx context.Context, orgID uuid.UUID, cred observability.CredentialRef, payload json.RawMessage, at time.Time) error
	AdvanceMeterPollMarks(ctx context.Context, now time.Time) error
	UpsertExternalUsage(ctx context.Context, orgID uuid.UUID, source string, link observability.MeterLinkChange,
		rows []observability.ExternalRow, reported map[string]time.Time, now time.Time) (observability.ExternalUploadResult, error)
}

var _ meterPollStore = (*observability.Store)(nil)

// MeterPollRunner makes one call that reads the meter of the credential
// token stands for, and returns the CLI's stdout (stream-json lines). An
// implementation must keep the token out of the error it returns; the caller
// scrubs it once more before logging.
type MeterPollRunner interface {
	Run(ctx context.Context, token string) ([]byte, error)
}

// meterPollState is the poll's memory in this process: when each seat was
// last tried and how many tries in a row filed nothing, and what was already
// said at warn level. A second control plane keeps its own; the bound is per
// process.
type meterPollState struct {
	mu     sync.Mutex
	tried  map[meterPollSeat]meterPollTry
	warned map[string]bool
	// now and store are for tests: nil means time.Now and o.Obs.
	now   func() time.Time
	store meterPollStore
}

type meterPollSeat struct {
	org  uuid.UUID
	key  string
	slot int
}

// meterPollTry is one seat's last try and the tries before it, in a row,
// that filed no reading.
type meterPollTry struct {
	at     time.Time
	misses int
}

// wait is how long after its last try the seat is left alone: meterPollRetry
// after a try that filed a reading, doubled for each miss in a row before the
// last, at most meterPollMaxWait.
func (t meterPollTry) wait() time.Duration {
	w := meterPollRetry
	for i := 1; i < t.misses && w < meterPollMaxWait; i++ {
		w *= 2
	}
	return min(w, meterPollMaxWait)
}

// attempt records a try of seat at now and reports whether one is due: the
// wait after the last try has passed.
func (s *meterPollState) attempt(seat meterPollSeat, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tried == nil {
		s.tried = map[meterPollSeat]meterPollTry{}
	}
	t, ok := s.tried[seat]
	if ok && now.Sub(t.at) < t.wait() {
		return false
	}
	t.at = now
	s.tried[seat] = t
	return true
}

// settle records what the try attempt let through came to: a reading filed
// starts the wait over, anything else lengthens it.
func (s *meterPollState) settle(seat meterPollSeat, filed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tried[seat]
	if filed {
		t.misses = 0
	} else if t.wait() < meterPollMaxWait {
		t.misses++
	}
	s.tried[seat] = t
}

// firstWarning reports whether seat has not yet been warned about for reason.
func (s *meterPollState) firstWarning(seat meterPollSeat, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.warned == nil {
		s.warned = map[string]bool{}
	}
	k := seat.org.String() + "\x00" + seat.key + "#" + strconv.Itoa(seat.slot) + "\x00" + reason
	if s.warned[k] {
		return false
	}
	s.warned[k] = true
	return true
}

func (o *Orchestrator) meterPollClock() time.Time {
	if o.meterPoll.now != nil {
		return o.meterPoll.now().UTC()
	}
	return time.Now().UTC()
}

// meterPollStore returns the store the poll works on, nil when there is none.
func (o *Orchestrator) meterPollStore() meterPollStore {
	if o.meterPoll.store != nil {
		return o.meterPoll.store
	}
	if o.Obs != nil {
		return o.Obs
	}
	return nil
}

// meterPollLoop asks once a minute which seats are due, and polls them. It
// runs with the poll off as well: PollMeters still advances the watermarks.
func (o *Orchestrator) meterPollLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(meterPollDelay):
	}
	t := time.NewTicker(meterPollEvery)
	defer t.Stop()
	o.PollMeters(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.PollMeters(ctx)
		}
	}
}

// PollMeters runs one round: the poll sources' watermarks move up to now,
// then every seat that is due and whose wait since its last try has passed is
// read. Exported so that a test or a command can run a round without waiting
// for the loop. Without a store nothing happens; without a runner or secrets
// only the watermarks move.
func (o *Orchestrator) PollMeters(ctx context.Context) {
	st := o.meterPollStore()
	if st == nil {
		return
	}
	now := o.meterPollClock()
	// Every round, with or without a seat due and with the poll off: a poll
	// source booked earlier would otherwise hold its seat's later readings
	// back from the split for as long as its host counts as active.
	if err := st.AdvanceMeterPollMarks(ctx, now); err != nil {
		o.Log.Warn("meter poll: watermarks not advanced", "err", err)
	}
	if o.MeterPoll == nil || o.Secrets == nil {
		return
	}
	due, err := st.MeterPollDue(ctx, now, meterPollFresh, meterPollActive)
	if err != nil {
		o.Log.Warn("meter poll: due seats not read", "err", err)
		return
	}
	for _, c := range due {
		if ctx.Err() != nil {
			return
		}
		o.pollMeter(ctx, st, c, now)
	}
}

// pollMeter reads one seat: the call, its booking, then its readings.
func (o *Orchestrator) pollMeter(ctx context.Context, st meterPollStore, c observability.MeterPollCandidate, now time.Time) {
	cred := c.Credential
	seat := meterPollSeat{org: c.OrgID, key: cred.SecretKey, slot: cred.SecretSlot}
	if cred.SecretKey == "" || !o.meterPoll.attempt(seat, now) {
		return
	}
	filed := false
	defer func() { o.meterPoll.settle(seat, filed) }()
	// Runtime and position identify the credential in the log; the secret's
	// name stays out of it (CodeQL treats any SecretKey access as sensitive,
	// see usage.go), and so does the store's error, which names the key.
	skip := func(reason string) {
		if o.meterPoll.firstWarning(seat, reason) {
			o.Log.Warn("meter poll: seat not read", "org", c.OrgID, "runtime", cred.RuntimeID,
				"ord", cred.Ord, "reason", reason)
		}
	}
	value, err := o.Secrets.Value(ctx, c.OrgID, cred.SecretKey, cred.SecretSlot)
	value = strings.TrimSpace(value)
	switch {
	case errors.Is(err, secrets.ErrNotFound) || (err == nil && value == ""):
		skip("no_value")
		return
	case err != nil:
		// A store that failed says nothing about the next try: said every
		// time (the wait bounds how often), not once.
		o.Log.Warn("meter poll: seat not read", "org", c.OrgID, "runtime", cred.RuntimeID,
			"ord", cred.Ord, "reason", "secret_unreadable")
		return
	case !claudeapi.IsOAuth(value, false):
		// Only a Claude OAuth token (sk-ant-oat…) has a meter to read. An API
		// key filed as a subscription would be billed per use, and a value of
		// any other shape is no secret for the claude CLI to be handed: the
		// declared kind does not vouch for it, hence false.
		skip("not_oauth")
		return
	}

	out, err := o.MeterPoll.Run(ctx, value)
	if err != nil {
		o.Log.Warn("meter poll: call failed", "org", c.OrgID, "runtime", cred.RuntimeID, "ord", cred.Ord,
			"err", strings.ReplaceAll(err.Error(), value, "[redacted]"))
		return
	}
	events, usage := parseMeterPoll(out)
	// The call's own instant on this clock: its booking, its watermark and
	// its readings all carry it, so none of them is dated by another clock.
	at := o.meterPollClock()

	// No result line, or one without modelUsage: what the call cost is not
	// known, so it cannot be booked, and a reading of a movement the books do
	// not hold would be split as if the poll had cost nothing.
	rows := usage.rows(at)
	if len(rows) == 0 {
		skip("no_result")
		return
	}
	source := observability.MeterPollSourceFor(cred.SecretKey, cred.SecretSlot)
	link := observability.MeterLinkChange{Set: true,
		Credential: &observability.MeterCredential{SecretKey: cred.SecretKey, SecretSlot: cred.SecretSlot}}
	if _, err := st.UpsertExternalUsage(ctx, c.OrgID, source,
		link, rows, map[string]time.Time{observability.MeterPollHost: at}, at); err != nil {
		// Not booked, not filed, for the same reason.
		o.Log.Warn("meter poll: call not booked", "org", c.OrgID, "runtime", cred.RuntimeID, "ord", cred.Ord,
			"err", meterPollScrub(err, source, cred.SecretKey))
		return
	}
	for _, ev := range events {
		if err := st.AddPolledReadings(ctx, c.OrgID, cred, ev, at); err != nil {
			o.Log.Warn("meter poll: reading not filed", "org", c.OrgID, "runtime", cred.RuntimeID, "ord", cred.Ord,
				"err", meterPollScrub(err, source, cred.SecretKey))
			continue
		}
		filed = true
	}
	if len(events) == 0 {
		skip("no_rate_limit_event")
	}
}

// meterPollScrub is a store error as it may be logged: the poll source's
// name and the secret's name, which the source holds, replaced. The source
// goes first, since a long one holds only part of the key.
func meterPollScrub(err error, source, secretKey string) string {
	msg := strings.ReplaceAll(err.Error(), source, observability.MeterPollSource+": [secret]")
	return strings.ReplaceAll(msg, secretKey, "[secret]")
}

// meterPollUsage is what the call's result line says it cost, per model.
type meterPollUsage struct {
	SessionID string
	Models    map[string]meterPollModel
}

// meterPollModel is one model's entry in a result line's modelUsage.
type meterPollModel struct {
	Input, Output, CacheRead   int64
	CacheWrite5m, CacheWrite1h int64
	CostUSD                    float64
	HasCost                    bool
}

// parseMeterPoll reads the CLI's stream-json: every rate_limit_event line,
// and the result line's per-model usage (nil when no result line came). Lines
// that are not JSON, or of another type, are skipped.
func parseMeterPoll(out []byte) ([]json.RawMessage, *meterPollUsage) {
	var (
		events []json.RawMessage
		usage  *meterPollUsage
	)
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var head struct {
			Type       string          `json:"type"`
			SessionID  string          `json:"session_id"`
			ModelUsage json.RawMessage `json:"modelUsage"`
		}
		if json.Unmarshal(line, &head) != nil {
			continue
		}
		switch head.Type {
		case "rate_limit_event":
			if observability.IsRateLimitEvent(line) {
				events = append(events, json.RawMessage(bytes.Clone(line)))
			}
		case "result":
			// One call, one result line; should a second come, the last
			// one's totals are the session's.
			usage = &meterPollUsage{SessionID: head.SessionID, Models: decodeMeterPollModels(head.ModelUsage)}
		}
	}
	return events, usage
}

// decodeMeterPollModels reads modelUsage entry by entry, as the daemon does
// (runtime_claudecode.go): an entry that is not an object is skipped, a
// figure that is not a number counts 0. The cache write is the 5-minute and
// 1-hour split where the CLI sends it, the plain sum (as 5-minute) where not.
func decodeMeterPollModels(raw json.RawMessage) map[string]meterPollModel {
	var entries map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	out := make(map[string]meterPollModel, len(entries))
	for model, e := range entries {
		var f map[string]json.RawMessage
		if model == "" || json.Unmarshal(e, &f) != nil || f == nil {
			continue
		}
		m := meterPollModel{
			Input:     meterPollInt(f["inputTokens"]),
			Output:    meterPollInt(f["outputTokens"]),
			CacheRead: meterPollInt(f["cacheReadInputTokens"]),
		}
		w5, w1 := meterPollInt(f["cacheCreationEphemeral5mInputTokens"]), meterPollInt(f["cacheCreationEphemeral1hInputTokens"])
		if w5+w1 > 0 {
			m.CacheWrite5m, m.CacheWrite1h = w5, w1
		} else {
			m.CacheWrite5m = meterPollInt(f["cacheCreationInputTokens"])
		}
		if v, ok := meterPollNumber(f["costUSD"]); ok && v >= 0 {
			m.CostUSD, m.HasCost = v, true
		}
		out[model] = m
	}
	return out
}

// meterPollNumber reads a JSON number, whole or not; anything else is not one.
func meterPollNumber(raw json.RawMessage) (float64, bool) {
	var v float64
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v != v {
		return 0, false
	}
	return v, true
}

// meterPollInt reads a token count: a number, never negative, 0 otherwise.
func meterPollInt(raw json.RawMessage) int64 {
	v, ok := meterPollNumber(raw)
	if !ok || v < 0 || v > 1<<62 {
		return 0
	}
	return int64(v)
}

// rows books the call as outside usage: one row per model, from the poll's
// host, under the result's session (a fresh id when it named none).
func (u *meterPollUsage) rows(at time.Time) []observability.ExternalRow {
	if u == nil || len(u.Models) == 0 {
		return nil
	}
	session := u.SessionID
	if session == "" {
		session = uuid.NewString()
	}
	models := make([]string, 0, len(u.Models))
	for m := range u.Models {
		models = append(models, m)
	}
	sort.Strings(models)
	rows := make([]observability.ExternalRow, 0, len(models))
	for _, model := range models {
		m := u.Models[model]
		ts := at
		r := observability.ExternalRow{
			Host: observability.MeterPollHost, SessionID: session, MessageID: session + "/" + model,
			TS: &ts, Model: model,
			Input: meterPollRef(m.Input), Output: meterPollRef(m.Output), CacheRead: meterPollRef(m.CacheRead),
			CacheWrite5m: meterPollRef(m.CacheWrite5m), CacheWrite1h: meterPollRef(m.CacheWrite1h),
		}
		if m.HasCost {
			r.ReportedUSD = meterPollRef(m.CostUSD)
		}
		rows = append(rows, r)
	}
	return rows
}

func meterPollRef[T any](v T) *T { return &v }

// meterPollArgs: one turn of the smallest model, printed as stream-json (which
// needs --verbose under -p) so that the rate_limit_event line is in it. The
// call is made as small as the CLI lets it be, since it is paid in meter
// points: no tools (their definitions are most of a default prompt), a
// one-line system prompt instead of Claude Code's own, and no session kept.
var meterPollArgs = []string{"-p", "ok", "--model", "haiku", "--output-format", "stream-json", "--verbose",
	"--max-turns", "1", "--tools", "", "--system-prompt", "Answer with one word.", "--no-session-persistence"}

// ClaudeMeterPoll runs the claude CLI for the poll.
//
// Each call gets a fresh, empty directory as HOME, CLAUDE_CONFIG_DIR and
// working directory, removed afterwards: the call reads no one's settings,
// MCP servers or CLAUDE.md, and leaves no transcript in a person's ~/.claude
// that `covey usage push` would upload a second time. The environment is
// built from nothing; the token is in it and nowhere on the command line.
// stderr is discarded — it is the CLI's text, and the error is the exit
// status only.
type ClaudeMeterPoll struct {
	Binary string
}

// Run makes the call and returns its stdout.
func (c ClaudeMeterPoll) Run(ctx context.Context, token string) ([]byte, error) {
	if c.Binary == "" {
		return nil, errors.New("meter poll: no claude binary configured")
	}
	ctx, cancel := context.WithTimeout(ctx, meterPollTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "covey-meter-poll-")
	if err != nil {
		return nil, fmt.Errorf("meter poll: %w", err)
	}
	defer os.RemoveAll(dir)

	cmd := exec.CommandContext(ctx, c.Binary, meterPollArgs...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"CLAUDE_CONFIG_DIR=" + dir,
		"CLAUDE_CODE_OAUTH_TOKEN=" + token,
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
	}
	out := &cappedBuffer{max: meterPollMaxOut}
	cmd.Stdout = out
	// A child the CLI started may hold stdout open after the CLI is killed;
	// the wait for it is bounded too.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("meter poll: claude: %w", ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("meter poll: claude: %s", exit.ProcessState.String())
		}
		return nil, fmt.Errorf("meter poll: claude: %w", err)
	}
	return out.Bytes(), nil
}

// cappedBuffer keeps the first max bytes written to it and drops the rest,
// without failing the writer.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// MeterPollFor resolves COVEY_METER_POLL: "off" (or "false", "no", "0", in
// any case) switches the poll off, "auto" (or nothing) takes `claude` from
// PATH, anything else is the binary's path, checked once here. The note is
// the one line to log at start, empty when there is nothing to say; it never
// repeats the setting, which is whatever somebody typed into the
// environment.
func MeterPollFor(setting string, lookPath func(string) (string, error)) (MeterPollRunner, string) {
	switch s := strings.TrimSpace(setting); strings.ToLower(s) {
	case "off", "false", "no", "0":
		return nil, ""
	case "", "auto":
		bin, err := lookPath("claude")
		if err != nil {
			return nil, "meter poll off: no claude CLI on PATH (COVEY_METER_POLL)"
		}
		return ClaudeMeterPoll{Binary: bin}, ""
	default:
		bin, err := lookPath(s)
		if err != nil {
			return nil, "meter poll off: COVEY_METER_POLL names no executable claude CLI"
		}
		return ClaudeMeterPoll{Binary: bin}, ""
	}
}
