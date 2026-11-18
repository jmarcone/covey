package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"covey/internal/observability"
)

// --- Usage outside covey (#120, 0150) ---
//
// An uploader (`covey usage push`) sends the token counts of Claude Code
// sessions that run outside covey, one row per model call. The rows go to the
// organisation of the caller's seat — a browser session or an API key — and
// are read back as the `external` field of /cost/cells. They never become a
// cost entry, a budget or a guard rail's figure (observability/external.go).

// externalUpload is the request body. Nothing else is accepted: an unknown
// field is refused, so no transcript text can ride along unnoticed.
type externalUpload struct {
	Source string `json:"source"`
	// MeterCredential: absent leaves the source's stored link alone, null
	// removes it, an object sets it.
	MeterCredential json.RawMessage             `json:"meter_credential"`
	Rows            []observability.ExternalRow `json:"rows"`
}

// handlePostExternalUsage stores an upload: an upsert on (organisation,
// source, message_id), so sending the same rows again changes nothing and a
// superset adds only the new ones. 400 for anything the rules refuse, 413
// above ExternalMaxBodyBytes.
func (s *Server) handlePostExternalUsage(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, observability.ExternalMaxBodyBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, "the body is larger than 2 MiB; send fewer rows per request")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var in externalUpload
	if err := dec.Decode(&in); err != nil {
		// The decoder's message names the field ("json: unknown field
		// \"prompt\"") and carries none of the body's values.
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if dec.More() {
		writeErr(w, http.StatusBadRequest, "invalid body: one JSON object expected")
		return
	}
	var link observability.MeterLinkChange
	if len(in.MeterCredential) > 0 {
		link.Set = true
		if !bytes.Equal(bytes.TrimSpace(in.MeterCredential), []byte("null")) {
			md := json.NewDecoder(bytes.NewReader(in.MeterCredential))
			md.DisallowUnknownFields()
			var mc observability.MeterCredential
			if err := md.Decode(&mc); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid meter_credential: "+err.Error())
				return
			}
			link.Credential = &mc
		}
	}
	res, err := s.Obs.UpsertExternalUsage(r.Context(), p.OrgID, in.Source, link, in.Rows, time.Now())
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

// handleDeleteExternalUsage removes every row of ?source= and its meter link.
func (s *Server) handleDeleteExternalUsage(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	res, err := s.Obs.DeleteExternalUsage(r.Context(), p.OrgID, r.URL.Query().Get("source"))
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
