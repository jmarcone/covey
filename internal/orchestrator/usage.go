package orchestrator

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// Reported utilisation, asked centrally and cached.
//
// The provider's usage endpoint has a rate limit of its own, and asking once
// per run across a fleet would hit it — after which the engine serves figures
// up to an hour old and eventually none at all. Three agents on one seat get
// the same answer anyway, so the question is asked PER CREDENTIAL and the
// answer is held briefly (spec/07, D13).
//
// The cache is deliberately short. The figure exists to say "this seat is
// nearly full"; a few minutes of staleness does not change that answer, and a
// longer memory would start hiding a seat that has just run dry.
const usageCacheTTL = 5 * time.Minute

// usageFailureTTL is how long an answer WITHOUT a figure is remembered. Only
// an engine that cannot ask at all (no_reporter) is remembered for good; a
// question that failed — the binary, the credential, a text we could not read,
// a daemon that had no engine to ask — says nothing about the next one, and
// remembering it forever is how one bad first answer silenced the meter for
// the life of the process.
const usageFailureTTL = time.Hour

// usageAsk is how long a run may be held up by the question. It is asked in the
// background and never in the critical path, so this only bounds the goroutine.
const usageAsk = 60 * time.Second

type usageEntry struct {
	usage daemon.Usage
	at    time.Time
	// unsupported remembers an answer without a figure, so we stop asking
	// rather than paying for the round trip on every wake; reason says for how
	// long (usageFailureTTL).
	unsupported bool
	reason      string
}

type usageCache struct {
	mu  sync.Mutex
	by  map[usageKey]usageEntry
	now func() time.Time
	// warned holds what was already said at warn level: once per credential
	// and reason, not on every wake.
	warned map[string]bool
}

type usageKey struct {
	runtime uuid.UUID
	ord     int
}

func newUsageCache() *usageCache {
	return &usageCache{by: map[usageKey]usageEntry{}, now: time.Now, warned: map[string]bool{}}
}

func (c *usageCache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c *usageCache) get(k usageKey) (usageEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.by[k]
	if !ok {
		return usageEntry{}, false
	}
	age := c.clock().Sub(e.at)
	if e.unsupported {
		// An engine that cannot ask stays remembered without an expiry — that
		// property does not change between two runs. Every other failure is
		// worth asking about again.
		return e, e.reason == daemon.UsageNoReporter || age < usageFailureTTL
	}
	return e, age < usageCacheTTL
}

func (c *usageCache) put(k usageKey, e usageEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.at.IsZero() {
		e.at = c.clock()
	}
	c.by[k] = e
}

// firstWarning reports whether this credential has not yet been warned about
// for this reason.
func (c *usageCache) firstWarning(cred, reason string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.warned == nil {
		c.warned = map[string]bool{}
	}
	k := cred + "\x00" + reason
	if c.warned[k] {
		return false
	}
	c.warned[k] = true
	return true
}

// Usage returns the engine's own figure for a credential, if it has one and it
// is still fresh. The second return value says whether anything is known at
// all; the caller falls back to the platform's own estimate.
func (o *Orchestrator) Usage(runtimeID uuid.UUID, ord int) (daemon.Usage, bool) {
	if o.usage == nil {
		return daemon.Usage{}, false
	}
	e, fresh := o.usage.get(usageKey{runtimeID, ord})
	if !fresh || e.unsupported || !e.usage.Reported() {
		return daemon.Usage{}, false
	}
	return e.usage, true
}

// refreshUsage asks the daemon of a running agent what its credential has
// consumed. Fire and forget: the answer arrives as an ordinary protocol message
// and is filed by handleDaemonMessage, so nothing here waits.
//
// Asked as soon as the link is up, before the first task: then the loop that
// reads the reply runs anyway, and a question that goes wrong costs nothing.
// At that moment the daemon has no config yet — it comes with the task — so
// the question NAMES the engine; a daemon that looked it up in its config
// answered "not supported" on every first wake.
func (o *Orchestrator) refreshUsage(ctx context.Context, link DaemonLink, engine string, runtimeID uuid.UUID, ord int) {
	if o.usage == nil || runtimeID == uuid.Nil || ord < 0 {
		return
	}
	if e, fresh := o.usage.get(usageKey{runtimeID, ord}); fresh && (e.unsupported || e.usage.Reported()) {
		return // still good, or an answer without a figure that is not due again yet
	}
	askCtx, cancel := context.WithTimeout(ctx, usageAsk)
	defer cancel()
	_ = o.sendMsg(askCtx, link, daemon.TypeRequestUsage,
		daemon.RequestUsage{RequestID: uuid.NewString(), Runtime: engine})
}

// noteUsage files an answer against the credential the session is running on.
// An answer without a figure is said at warn level once per credential and
// reason — a meter that silently reads nothing is a hole in every figure built
// on it — and then kept quiet.
func (o *Orchestrator) noteUsage(rep daemon.UsageReport, cred observability.CredentialRef) {
	if o.usage == nil || cred.RuntimeID == uuid.Nil || cred.Ord < 0 {
		return
	}
	o.usage.put(usageKey{cred.RuntimeID, cred.Ord},
		usageEntry{usage: rep.Usage, unsupported: !rep.Supported, reason: rep.Reason})
	if rep.Supported {
		o.Log.Info("utilisation reported", "runtime", cred.RuntimeID, "ord", cred.Ord,
			"window_pct", rep.Usage.WindowPercent, "week_pct", rep.Usage.WeekPercent,
			"stale", rep.Usage.Stale)
		return
	}
	if o.usage.firstWarning(credentialName(cred), rep.Reason) {
		// The reason is a code; rep.Error is the daemon's own text and goes
		// nowhere but here, unprinted.
		o.Log.Warn("utilisation not reported", "runtime", cred.RuntimeID, "ord", cred.Ord,
			"secret", cred.SecretKey, "slot", cred.SecretSlot, "reason", rep.Reason)
	}
}

// credentialName identifies a credential for the warn-once memory: by its
// secret where known (a Reorder moves positions, not values), by its position
// otherwise.
func credentialName(c observability.CredentialRef) string {
	if c.SecretKey != "" {
		return c.SecretKey + "#" + strconv.Itoa(c.SecretSlot)
	}
	return c.RuntimeID.String() + "@" + strconv.Itoa(c.Ord)
}

// keepUsageReading files the provider's answer as history (cost insights,
// migrations 0142/0145), next to the in-memory cache that serves the capacity
// layer. Unsupported answers are kept too, with their reason code, so a
// credential that cannot be asked is visible as such rather than as a gap.
// rep.Error is never stored: it is the daemon's text, and the reason says
// what a reader needs.
//
// A stale answer is filed with the time the engine says it loaded the figure,
// not the time it was asked — and flagged, so the meter's arithmetic skips it.
func (o *Orchestrator) keepUsageReading(ctx context.Context, orgID uuid.UUID, rep daemon.UsageReport, cred observability.CredentialRef) {
	if o.Obs == nil || cred.RuntimeID == uuid.Nil || cred.Ord < 0 {
		return
	}
	u := rep.Usage
	r := observability.MeterReading{Credential: cred, Source: observability.SourceUsageCmd,
		WindowKind: observability.WindowSevenDay, Supported: rep.Supported, Reason: rep.Reason,
		WindowPercent: -1, WeekPercent: -1, Stale: u.Stale, FetchedAt: u.Fetched}
	if rep.Supported {
		r.WindowPercent, r.WeekPercent = u.WindowPercent, u.WeekPercent
		r.WindowResets, r.WeekResets = u.WindowResets, u.WeekResets
		if u.WeekResetsAt != nil {
			r.ResetsAt = *u.WeekResetsAt
		}
		if u.Stale && r.Reason == "" {
			r.Reason = daemon.UsageStale
		}
	} else {
		r.Stale, r.FetchedAt = false, time.Time{}
	}
	if err := o.Obs.AddUsageReading(ctx, orgID, r); err != nil {
		o.Log.Warn("usage reading not kept", "runtime", cred.RuntimeID, "ord", cred.Ord, "err", err)
	}
}

// reportedUtilisation is the capacity layer's view of what the provider said —
// the share of the current window, if the engine could ask and the answer is
// recent enough to act on.
//
// A STALE figure is withheld rather than passed on with a flag. It is up to an
// hour old (the engine serves it from its own cache when the provider's
// endpoint is rate limited), and the decision it would feed is "skip this
// credential" — taking a seat out of play on an hour-old number would cost
// exactly the capacity this whole mechanism exists to use up.
func (o *Orchestrator) reportedUtilisation(runtimeID uuid.UUID, ord int) (float64, bool) {
	u, ok := o.Usage(runtimeID, ord)
	if !ok || u.Stale || u.WindowPercent < 0 {
		return 0, false
	}
	return u.WindowPercent, true
}
