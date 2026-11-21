package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	// ReportedThrough: per host, the time through which its calls have been
	// sent ({"laptop": "2026-11-18T19:40:00Z"}). An upload of no rows that
	// moves it says "nothing new up to here": without it, the cost page
	// does not split the meter after the host's last call.
	ReportedThrough map[string]time.Time `json:"reported_through"`
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
		// \"prompt\"") or the type it wanted, but not every one is free of
		// the body's values: a syntax error quotes the character it stopped
		// at, and a time that does not parse is quoted whole
		// (time.ParseError). They go back only to the sender of that body.
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// Anything after the object, a stray bracket included, is refused:
	// More() is false at a closing ']' or '}', Token() is not.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: one JSON object expected")
		return
	}
	// The same 400 as any other source the rules refuse.
	if err := uploadSourceAllowed(in.Source); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	res, err := s.Obs.UpsertExternalUsage(r.Context(), p.OrgID, in.Source, link, in.Rows, in.ReportedThrough, time.Now())
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

// uploadSourceAllowed refuses an upload into one of covey's own meter poll
// sources (#153). covey books its polls there and vouches for their
// watermark (AdvanceMeterPollMarks); calls an uploader slipped in would be
// covered by that watermark without anything saying they were all sent, and
// would hide in what reads as covey's own call. The test is wider than
// IsMeterPollSource: any name that, trimmed and in lower case, begins with
// "meter poll" is refused, so that no upload passes for covey's own in the
// list of sources either. Deleting such a source stays allowed: the next poll
// books it anew.
func uploadSourceAllowed(source string) error {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), observability.MeterPollSource) {
		return observability.ErrInvalidExternal{Reason: fmt.Sprintf("source %q is reserved for covey's own meter poll; "+
			"push under another source name", source)}
	}
	return nil
}

// handleDeleteExternalUsage removes every row of ?source=, its meter link and
// its hosts' watermarks.
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
