package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"covey/internal/observability"
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

type costHealthCache struct {
	mu    sync.Mutex
	byOrg map[uuid.UUID]observability.HealthReport
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
	s.costHealth.mu.Lock()
	rep, ok := s.costHealth.byOrg[p.OrgID]
	s.costHealth.mu.Unlock()
	if ok && now.Sub(rep.CheckedAt) >= 0 && now.Sub(rep.CheckedAt) < costHealthTTL {
		writeJSON(w, http.StatusOK, rep)
		return
	}
	rep, err := s.Obs.CostHealth(r.Context(), p.OrgID, now)
	if err != nil {
		// A database fault is not a verdict on the figures: 500, logged, and
		// the page says the checks could not run.
		if s.Log != nil {
			s.Log.Error("cost health: the checks could not run", "org", p.OrgID, "err", err)
		}
		writeErr(w, http.StatusInternalServerError, "the cost checks could not run")
		return
	}
	s.costHealth.mu.Lock()
	if s.costHealth.byOrg == nil {
		s.costHealth.byOrg = map[uuid.UUID]observability.HealthReport{}
	}
	s.costHealth.byOrg[p.OrgID] = rep
	s.costHealth.mu.Unlock()
	writeJSON(w, http.StatusOK, rep)
}
