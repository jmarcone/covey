package httpapi

import "net/http"

// handleModelUsage: the organisation's usage per agent and model as Claude
// Code reported it (tokens per type, cache writes split by TTL, the engine's
// own price) next to the same tokens priced with covey's list. ?days= as on
// the other cost endpoints.
func (s *Server) handleModelUsage(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	_, since := costWindow(r)
	rep, err := s.Obs.ModelUsage(r.Context(), p.OrgID, since)
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
