package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"covey/internal/observability"
	"covey/internal/secrets"
)

// Cost health (#178): the checks that say whether the cost page can be
// trusted right now, for the badge beside its headline and /costs/health.
//
// Computed on request and kept for five minutes per organisation: the badge
// sits on a page people reload, and eight queries over the cost tables per
// visit buy nothing the thresholds (75 minutes, two hours) could see. A
// failed computation is not kept, and answers 500 — never a green badge.

// costHealthTTL: how long a report is served before the checks run again.
const costHealthTTL = 5 * time.Minute

// costHealthTimeout bounds one computation, which runs on for the requests
// waiting on it even when the one that started it went away.
const costHealthTimeout = time.Minute

type costHealthCache struct {
	mu    sync.Mutex
	byOrg map[uuid.UUID]observability.HealthReport
	// running: the computation in progress per organisation. Requests that
	// arrive while it runs wait for it rather than start their own — the
	// page's queries and every open tab hit an expired report at once.
	running map[uuid.UUID]*costHealthRun
}

// costHealthRun is one computation; done closes when rep and err are set.
type costHealthRun struct {
	done chan struct{}
	rep  observability.HealthReport
	err  error
}

func (s *Server) costHealthNow() time.Time {
	if s.CostHealthNow != nil {
		return s.CostHealthNow()
	}
	return time.Now()
}

// handleCostHealth: {status, checked_at, checks: [{id, status, reason, facts,
// checked_at}]} — status is the worst of the checks (info never counts).
func (s *Server) handleCostHealth(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	now := s.costHealthNow()
	c := &s.costHealth
	c.mu.Lock()
	if rep, ok := c.byOrg[p.OrgID]; ok && now.Sub(rep.CheckedAt) >= 0 && now.Sub(rep.CheckedAt) < costHealthTTL {
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, rep)
		return
	}
	run, waiting := c.running[p.OrgID]
	if !waiting {
		run = &costHealthRun{done: make(chan struct{})}
		if c.running == nil {
			c.running = map[uuid.UUID]*costHealthRun{}
		}
		c.running[p.OrgID] = run
	}
	c.mu.Unlock()

	if !waiting {
		s.runCostHealth(r.Context(), run, p.OrgID, now)
	}
	select {
	case <-run.done:
	case <-r.Context().Done():
		return
	}
	if run.err != nil {
		// A database fault is not a verdict on the figures: 500, logged, and
		// the page says the checks could not run.
		if s.Log != nil && !waiting {
			s.Log.Error("cost health: the checks could not run", "org", p.OrgID, "err", run.err)
		}
		writeErr(w, http.StatusInternalServerError, "the cost checks could not run")
		return
	}
	writeJSON(w, http.StatusOK, run.rep)
}

// runCostHealth makes one computation for the requests waiting on run. It
// always ends it — also when the checks panic: the run leaves the running
// map and done closes, or every later request of the organisation would wait
// on it until its own context ended, with no answer at all. A panic is an
// error like a database fault: answered 500, logged, and not kept.
func (s *Server) runCostHealth(parent context.Context, run *costHealthRun, orgID uuid.UUID, now time.Time) {
	c := &s.costHealth
	defer func() {
		if v := recover(); v != nil {
			run.err = fmt.Errorf("panic: %v\n%s", v, debug.Stack())
		}
		c.mu.Lock()
		delete(c.running, orgID)
		// A failed computation is not kept: the next request runs the
		// checks again.
		if run.err == nil {
			if c.byOrg == nil {
				c.byOrg = map[uuid.UUID]observability.HealthReport{}
			}
			c.byOrg[orgID] = run.rep
		}
		c.mu.Unlock()
		close(run.done)
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), costHealthTimeout)
	defer cancel()
	run.rep, run.err = s.costHealthReport(ctx, orgID, now)
}

// costHealthReport runs the checks, with whether an org-wide admin key is
// stored (C7: the poller reads the bill only then).
func (s *Server) costHealthReport(ctx context.Context, orgID uuid.UUID, now time.Time) (observability.HealthReport, error) {
	var in observability.HealthInputs
	keys, err := s.Secrets.Keys(ctx, orgID)
	if err != nil {
		return observability.HealthReport{}, err
	}
	in.AdminKey = slices.Contains(keys, secrets.AdminKey)
	return s.Obs.CostHealth(ctx, orgID, now, in)
}
