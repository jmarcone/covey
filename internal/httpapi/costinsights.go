package httpapi

import (
	"net/http"
	"strconv"

	"covey/internal/observability"
)

// handleCostCells: the data behind the combinable cost chart — per bucket and
// agent the reported cost (booked, the engine's figure) and the measured cost
// (raw tokens priced per type with the list). The UI filters and splits it.
// ?days= and ?bucket= as on the other cost endpoints.
func (s *Server) handleCostCells(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	bucket, since := costWindow(r)
	cells, err := s.Obs.CostCellsFor(r.Context(), p.OrgID, bucket, since)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cells)
}

// handleModelUsage: the organisation's usage per agent and model as Claude
// Code reported it (tokens per type, cache writes split by TTL, the engine's
// own price) next to the same tokens priced with covey's list. ?days= as on
// the other cost endpoints; ?secret_key= (and ?secret_slot=, default 0)
// narrows it to the runs of one credential value.
func (s *Server) handleModelUsage(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	_, since := costWindow(r)
	f := observability.CredentialFilter{SecretKey: r.URL.Query().Get("secret_key")}
	if v := r.URL.Query().Get("secret_slot"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 32767 {
			writeErr(w, http.StatusBadRequest, "secret_slot must be a small non-negative number")
			return
		}
		f.SecretSlot = n
	}
	rep, err := s.Obs.ModelUsage(r.Context(), p.OrgID, since, f)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleUsageReadings: the provider's utilisation readings for the
// organisation's credentials over the window, each with the list-price cost
// booked up to that moment — the meter read against the bill it would be.
func (s *Server) handleUsageReadings(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	_, since := costWindow(r)
	rows, err := s.Obs.UsageReadings(r.Context(), p.OrgID, since)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
