package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"covey/internal/observability"
	"covey/internal/orchestrator"
	"covey/internal/secrets"
)

// handleCostCells: the data behind the combinable cost chart — per bucket and
// agent the reported cost (booked, the engine's figure) and the measured cost
// (raw tokens priced per type with the list); per credential and meter source
// the subscription meter's movement, priced by the plan valid when it was
// read, its coverage, and the plan rows of the window. The UI filters and
// splits it; nothing is summed across credentials here.
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
// organisation's credentials over the window — both sources (usage_cmd,
// inband), every window, the unsupported ones with their reason — each with
// the measured list price on its own credential up to that moment.
// Served as /cost/usage-readings and, for the existing page, /cost/subscription.
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

// --- Billed money (0146) ---
//
// What Anthropic charged, read from its Usage and Cost Analytics API by the
// control plane with the organisation's anthropic_admin_key. Every amount is a
// decimal string in US dollars (exact, never a float). None of these answers
// carries the key or anything derived from it.

// billingKeysDays: how far back the key list sums the bill.
const billingKeysDays = 30

// handleBilled: the bill per bucket (hour | 10hour | day | week), API key and model,
// each point with the credential the key is mapped to (none: other use of
// the account) — the chart's bars. And `window`: per mapped credential the
// bill over whole billed buckets with the reported and recomputed figures of
// the same window — the tiles, which must never set a bill over one span
// against lines over another. ?days= and ?bucket= as on the other cost
// endpoints.
func (s *Server) handleBilled(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	bucket, since := costWindow(r)
	series, err := s.Obs.BilledSeries(r.Context(), p.OrgID, bucket, since)
	if err != nil {
		mapErr(w, err)
		return
	}
	window, err := s.Obs.BilledWindowFor(r.Context(), p.OrgID, since, time.Now())
	if err != nil {
		mapErr(w, err)
		return
	}
	sync, err := s.billingSync(r)
	if err != nil {
		mapErr(w, err)
		return
	}
	out := map[string]any{"bucket": series.Bucket, "points": series.Points, "window": window, "sync": sync}
	if series.Reason != "" {
		out["reason"] = series.Reason
	}
	if series.HourlyFrom != nil {
		out["hourly_from"] = series.HourlyFrom
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReconcile: per UTC day × mapped credential × model, what Claude Code
// reported, covey's list of the same tokens, and what Anthropic billed the
// mapped API key; token diffs per type, the residual and whether the day is
// complete. ?days= as on the other cost endpoints.
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	_, since := costWindow(r)
	rec, err := s.Obs.Reconcile(r.Context(), p.OrgID, since)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// billingSync is the poller's state with whether a key is stored at all.
func (s *Server) billingSync(r *http.Request) (observability.BillingSyncStatus, error) {
	p := principalFrom(r)
	st, err := s.Obs.BillingSync(r.Context(), p.OrgID)
	if err != nil {
		return st, err
	}
	keys, err := s.Secrets.Keys(r.Context(), p.OrgID)
	if err != nil {
		return st, err
	}
	for _, k := range keys {
		if k == secrets.AdminKey {
			st.Configured = true
		}
	}
	return st, nil
}

// handleGetBillingSync: configured, last attempt and success, how far the
// data is complete, and the last error (which carries no secret).
func (s *Server) handleGetBillingSync(w http.ResponseWriter, r *http.Request) {
	st, err := s.billingSync(r)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePostBillingSync reads the organisation's bill now — the hourly
// window, and with ?full=1 the 35-day re-read too. 409 without a key; a
// failed read is a 502 with the poller's state, whose last_error says why.
func (s *Server) handlePostBillingSync(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if s.Orch == nil {
		writeErr(w, http.StatusServiceUnavailable, "the orchestrator is not running")
		return
	}
	err := s.Orch.SyncBillingOrg(r.Context(), p.OrgID, r.URL.Query().Get("full") == "1")
	if errors.Is(err, orchestrator.ErrNoAdminKey) {
		writeErr(w, http.StatusConflict, err.Error()+" — store the Admin API key (sk-ant-admin…) as anthropic_admin_key first")
		return
	}
	st, serr := s.billingSync(r)
	if serr != nil {
		mapErr(w, serr)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, st)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleBillingKeys: the API keys the bill names over the last 30 days with
// their amount, largest first, each with the credential it is mapped to; and
// every mapping, closed ones too — what a person needs to map a credential.
func (s *Server) handleBillingKeys(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	keys, err := s.Obs.BillingKeys(r.Context(), p.OrgID, time.Now().AddDate(0, 0, -billingKeysDays))
	if err != nil {
		mapErr(w, err)
		return
	}
	maps, err := s.Obs.BillingKeyMappings(r.Context(), p.OrgID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": billingKeysDays, "keys": keys, "mappings": maps})
}
