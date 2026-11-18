package httpapi

import (
	"errors"
	"net/http"

	"covey/internal/observability"
)

// handleGetExternalUsage: the sessions outside covey per bucket, source,
// origin and model, for their detailed view (#120). ?days= and ?bucket= as on
// /cost/cells; ?source= narrows the cells to one source. The sources and the
// meter share are /cost/cells' own (external.sources, external.meter).
func (s *Server) handleGetExternalUsage(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	bucket, since := costWindow(r)
	source := r.URL.Query().Get("source")
	if source != "" {
		if err := observability.ValidateExternalSource(source); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	res, err := s.Obs.ExternalUsageByModel(r.Context(), p.OrgID, bucket, since, source)
	var invalid observability.ErrInvalidExternal
	switch {
	case errors.As(err, &invalid):
		writeErr(w, http.StatusBadRequest, invalid.Error())
		return
	case err != nil:
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
