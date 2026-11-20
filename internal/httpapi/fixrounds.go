package httpapi

import (
	"net/http"
	"time"
)

// fixRoundsDefault is the window GET /cost/fix-rounds reads when none is
// asked for: the last seven days (#130, AC 8).
const fixRoundsDefault = 7 * 24 * time.Hour

// fixRoundsMax bounds the window: the figure is read a week at a time, and a
// year is more than any comparison needs.
const fixRoundsMax = 366 * 24 * time.Hour

// fixRoundsWindow reads ?from= and ?to= (RFC 3339) into [from, to). Without
// ?to= the window ends now; without ?from= it starts seven days before its end.
func fixRoundsWindow(r *http.Request, now time.Time) (from, to time.Time, msg string) {
	q := r.URL.Query()
	to = now.UTC()
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, "to must be an RFC 3339 time"
		}
		to = t.UTC()
	}
	from = to.Add(-fixRoundsDefault)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, "from must be an RFC 3339 time"
		}
		from = t.UTC()
	}
	if !from.Before(to) {
		return from, to, "from must be before to"
	}
	if to.Sub(from) > fixRoundsMax {
		return from, to, "the window must not exceed 366 days"
	}
	return from, to, ""
}

// handleFixRounds: the fix rounds of the organisation finished in [from, to)
// — developer tasks on a pull request or issue they had worked on before —
// split by the sticky-session decision and by the policy applied, with the
// rounds per pull request (observability.FixRounds has the formulas).
func (s *Server) handleFixRounds(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	from, to, msg := fixRoundsWindow(r, time.Now())
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	rep, err := s.Obs.FixRounds(r.Context(), p.OrgID, from, to)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
