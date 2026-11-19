package httpapi

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

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

// meterWindow is a provider window name as Claude Code prints it.
var meterWindow = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// handleAddUsageReading files a meter reading a person read off the provider
// (source manual): {runtime_id, ord, window_kind, percent, resets_at,
// fetched_at?}. percent is the utilisation in percent (78 for 0.78);
// resets_at is required — without it a lower reading would read as a new
// month (or week), and it must lie after fetched_at, by at most 8 days for
// the seven_day* and five_hour* windows and 32 days for the others;
// fetched_at, when the figure was read, defaults to now and may not lie
// ahead. A second reading of the credential and window at the same
// fetched_at is refused (409), not silently dropped.
func (s *Server) handleAddUsageReading(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var in struct {
		RuntimeID  uuid.UUID  `json:"runtime_id"`
		Ord        *int       `json:"ord"`
		WindowKind string     `json:"window_kind"`
		Percent    *float64   `json:"percent"`
		ResetsAt   *time.Time `json:"resets_at"`
		FetchedAt  *time.Time `json:"fetched_at"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch {
	case in.RuntimeID == uuid.Nil || in.Ord == nil || *in.Ord < 0:
		writeErr(w, http.StatusBadRequest, "runtime_id and ord name the credential")
		return
	case !meterWindow.MatchString(in.WindowKind):
		writeErr(w, http.StatusBadRequest, "window_kind must be a meter window such as overage or seven_day")
		return
	case in.Percent == nil || math.IsNaN(*in.Percent) || *in.Percent < 0 || *in.Percent > 1000:
		writeErr(w, http.StatusBadRequest, "percent must be the utilisation in percent, 0 to 1000")
		return
	case in.ResetsAt == nil || in.ResetsAt.IsZero():
		writeErr(w, http.StatusBadRequest, "resets_at is required: the reset the provider names (resetsAt)")
		return
	}
	at := time.Now()
	if in.FetchedAt != nil {
		if in.FetchedAt.After(at.Add(5 * time.Minute)) {
			writeErr(w, http.StatusBadRequest, "fetched_at may not lie ahead")
			return
		}
		at = *in.FetchedAt
	}
	// The reset lies ahead of the reading, and within the window's length:
	// a week (with a day's slack) for the weekly and five-hour windows, a
	// month for the others. A reset in the past or a year ahead is a typo,
	// and filed it would split or swallow a month.
	horizon, span := 32*24*time.Hour, "32 days"
	if strings.HasPrefix(in.WindowKind, "seven_day") || strings.HasPrefix(in.WindowKind, "five_hour") {
		horizon, span = 8*24*time.Hour, "8 days"
	}
	if !in.ResetsAt.After(at) {
		writeErr(w, http.StatusBadRequest, "resets_at must lie after fetched_at")
		return
	}
	if in.ResetsAt.Sub(at) > horizon {
		writeErr(w, http.StatusBadRequest, "resets_at may lie at most "+span+" after fetched_at for window "+in.WindowKind)
		return
	}
	err := s.Obs.AddManualReading(r.Context(), p.OrgID, in.RuntimeID, *in.Ord, in.WindowKind, *in.Percent, in.ResetsAt, at.UTC())
	if errors.Is(err, observability.ErrNoCredential) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, observability.ErrReadingExists) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		mapErr(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
