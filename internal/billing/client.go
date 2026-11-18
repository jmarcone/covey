// Package billing reads what Anthropic actually charged: the Usage and Cost
// Analytics API (GET /v1/organizations/analytics/cost and /usage, beta).
//
// It is the third figure of the cost lab (spec/06): Claude Code REPORTS a
// price per result line, covey's list RECOMPUTES it from the tokens, and this
// is the BILL — for a metered API key, what Anthropic invoiced after discounts
// and before credits, and the list price Anthropic itself computed.
//
// The rules that shape it, from the API's documentation:
//
//   - A request spans at most 31 days (24 hours with 1m buckets), starts on or
//     after 2026-01-01 and within the last 365 days, and both ends are rounded
//     DOWN to a bucket boundary. Ranges are split and aligned here, so that
//     the window a caller replaces is exactly the window that was read.
//   - Rows are paged (limit <= 1000, has_more / next_page). An HTTP 410 means
//     the data refreshed under the cursor: what was collected is discarded and
//     the read starts again from page 1.
//   - Money is a decimal string in CENTS ("41280.000000"). It stays a string
//     here, and NUMERIC in the database: never a float.
//   - Data lands 4-24 h late. data_refreshed_at marks where complete data ends;
//     empty data with data_refreshed_at null means nothing has landed yet —
//     not zero spend.
//
// The credential is the organisation's anthropic_admin_key (or a
// read:analytics key). It is set on the request inside do() and nowhere else;
// errors carry the status and a scrubbed, bounded piece of the body, never a
// header. Raw net/http in the style of internal/claudeapi: no SDK, so that
// what goes over the wire is visible in one file.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultBaseURL is the Anthropic API.
const DefaultBaseURL = "https://api.anthropic.com"

const (
	apiVersion = "2026-10-21"
	betaHeader = "analytics-usage-cost-2026-08-24"
	// pageLimit is the API's maximum rows per page.
	pageLimit = 1000
	// maxSpan is the longest range one request may cover; 1m buckets allow a day.
	maxSpan       = 31 * 24 * time.Hour
	maxSpanMinute = 24 * time.Hour
	// lookback is how far back the API answers.
	lookback = 365 * 24 * time.Hour
	// maxRestarts: how often an expired cursor (410) restarts a read.
	maxRestarts = 3
	// maxTries: attempts per request on 429, 5xx or a broken connection.
	maxTries = 5
	// maxPages guards against a server that never stops saying has_more.
	maxPages = 10000
	// errBodyBytes bounds what of a response body an error repeats.
	errBodyBytes = 300
	// maxWait caps a retry-after a server may ask for.
	maxWait = 5 * time.Minute
)

// Floor is the earliest moment the API reports.
var Floor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Bucket widths.
const (
	Minute = "1m"
	Hour   = "1h"
	Day    = "1d"
)

// Client reads one organisation's analytics. The zero values of BaseURL,
// HTTP, Now and Sleep are the production ones; tests set them.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Key is the Admin API key or read:analytics key. Never logged, never in
	// an error.
	Key   string
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Query is one read. Start and End are aligned to Bucket (Start down, End up)
// and clamped to what the API answers; Windows says what that makes of them.
type Query struct {
	Start, End time.Time
	// Bucket is 1m, 1h or 1d.
	Bucket string
	// GroupBy: model, workspace_id, api_key_id, service_tier, ... and on /cost
	// also cost_type and token_type.
	GroupBy      []string
	APIKeyIDs    []string
	WorkspaceIDs []string
}

// Window is a half-open range [Start, End).
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Group is a row's dimensions; a dimension the API reports as null is "".
type Group struct {
	APIKeyID    string `json:"api_key_id"`
	WorkspaceID string `json:"workspace_id"`
	Model       string `json:"model"`
	ServiceTier string `json:"service_tier"`
	CostType    string `json:"cost_type,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
}

// CostRow is one bucket x dimension combination of /cost. The amounts are
// cents as the API wrote them: exact decimal strings.
type CostRow struct {
	Start, End      time.Time
	Group           Group
	AmountCents     string
	ListAmountCents string
	Currency        string
}

// UsageRow is one bucket x dimension combination of /usage.
type UsageRow struct {
	Start, End        time.Time
	Group             Group
	UncachedInput     int64
	CacheWrite5m      int64
	CacheWrite1h      int64
	CacheRead         int64
	Output            int64
	WebSearchRequests int64
	Requests          int64
}

// Read is what a query returned beyond its rows.
type Read struct {
	// Window is the range actually read, after alignment and clamping: the
	// range a caller may replace with the rows. Zero when nothing was asked.
	Window Window
	// DataRefreshedAt: complete data ends here (the earliest of the requests'
	// values). nil when no request said.
	DataRefreshedAt *time.Time
	// Landed is false when every request came back empty with
	// data_refreshed_at null: nothing has arrived yet, which is not zero.
	Landed bool
	// Requests counts the HTTP requests made, retries included.
	Requests int
}

// CostResult is a /cost read.
type CostResult struct {
	Read
	Rows []CostRow
}

// UsageResult is a /usage read.
type UsageResult struct {
	Read
	Rows []UsageRow
}

// APIError is a non-success answer. It never carries a header, and the body
// is cut to errBodyBytes with the key scrubbed out.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("analytics API: HTTP %d", e.Status)
	}
	return fmt.Sprintf("analytics API: HTTP %d: %s", e.Status, e.Body)
}

// ErrCursorExpired: the read restarted maxRestarts times and the data kept
// refreshing under it.
var ErrCursorExpired = errors.New("analytics API: the page cursor kept expiring (HTTP 410)")

// errGone is one 410, internal: the chunk restarts.
var errGone = errors.New("cursor expired")

// Cost reads /cost.
func (c *Client) Cost(ctx context.Context, q Query) (CostResult, error) {
	raw, read, err := c.collect(ctx, "cost", q)
	if err != nil {
		return CostResult{}, err
	}
	out := CostResult{Read: read, Rows: make([]CostRow, 0, len(raw))}
	for _, r := range raw {
		row, err := parseCost(r)
		if err != nil {
			return CostResult{}, err
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// Usage reads /usage.
func (c *Client) Usage(ctx context.Context, q Query) (UsageResult, error) {
	raw, read, err := c.collect(ctx, "usage", q)
	if err != nil {
		return UsageResult{}, err
	}
	out := UsageResult{Read: read, Rows: make([]UsageRow, 0, len(raw))}
	for _, r := range raw {
		row, err := parseUsage(r)
		if err != nil {
			return UsageResult{}, err
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// Windows splits a query into the ranges the requests will cover: Start
// aligned down and End up to the bucket, Start no earlier than the API's floor
// (2026-01-01, 365 days back — itself aligned UP, so the first request is
// never refused), End no later than the end of the current bucket, each piece
// at most 31 days (a day for 1m buckets). Empty when nothing is left.
func (c *Client) Windows(q Query) ([]Window, error) {
	step, span, err := bucketStep(q.Bucket)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	start := q.Start.UTC().Truncate(step)
	end := ceil(q.End.UTC(), step)
	floor := Floor
	if back := now.Add(-lookback); back.After(floor) {
		floor = back
	}
	floor = ceil(floor, step)
	if start.Before(floor) {
		start = floor
	}
	if limit := ceil(now, step); end.After(limit) {
		end = limit
	}
	var out []Window
	for s := start; s.Before(end); {
		e := s.Add(span)
		if e.After(end) {
			e = end
		}
		out = append(out, Window{Start: s, End: e})
		s = e
	}
	return out, nil
}

// bucketStep is a bucket's length and the longest request span for it.
func bucketStep(bucket string) (step, span time.Duration, err error) {
	switch bucket {
	case Minute:
		return time.Minute, maxSpanMinute, nil
	case Hour:
		return time.Hour, maxSpan, nil
	case Day:
		// 31 whole days: a multiple of the bucket, so no piece ends mid-day.
		return 24 * time.Hour, maxSpan, nil
	}
	return 0, 0, fmt.Errorf("analytics API: unknown bucket width %q (1m, 1h or 1d)", bucket)
}

// ceil rounds t up to a multiple of step (UTC, as the API's buckets are).
func ceil(t time.Time, step time.Duration) time.Time {
	if f := t.Truncate(step); !f.Equal(t) {
		return f.Add(step)
	}
	return t
}

// collect reads every page of every window. A 410 discards the window's rows
// and restarts it from page 1, at most maxRestarts times.
func (c *Client) collect(ctx context.Context, endpoint string, q Query) ([]json.RawMessage, Read, error) {
	wins, err := c.Windows(q)
	if err != nil {
		return nil, Read{}, err
	}
	var (
		read Read
		all  []json.RawMessage
	)
	if len(wins) == 0 {
		return nil, read, nil
	}
	read.Window = Window{Start: wins[0].Start, End: wins[len(wins)-1].End}
	for _, w := range wins {
		var (
			rows      []json.RawMessage
			refreshed *time.Time
		)
		for restart := 0; ; restart++ {
			rows, refreshed, err = c.window(ctx, endpoint, q, w, &read.Requests)
			if !errors.Is(err, errGone) {
				break
			}
			if restart >= maxRestarts {
				return nil, Read{}, ErrCursorExpired
			}
		}
		if err != nil {
			return nil, Read{}, err
		}
		if len(rows) > 0 || refreshed != nil {
			read.Landed = true
		}
		if refreshed != nil && (read.DataRefreshedAt == nil || refreshed.Before(*read.DataRefreshedAt)) {
			r := refreshed.UTC()
			read.DataRefreshedAt = &r
		}
		all = append(all, rows...)
	}
	return all, read, nil
}

// window pages through one request range.
func (c *Client) window(ctx context.Context, endpoint string, q Query, w Window, requests *int) ([]json.RawMessage, *time.Time, error) {
	params := url.Values{}
	params.Set("starting_at", w.Start.Format(time.RFC3339))
	params.Set("ending_at", w.End.Format(time.RFC3339))
	params.Set("time_bucket_width", q.Bucket)
	params.Set("limit", strconv.Itoa(pageLimit))
	for _, g := range q.GroupBy {
		params.Add("group_by[]", g)
	}
	for _, k := range q.APIKeyIDs {
		params.Add("api_key_ids[]", k)
	}
	for _, ws := range q.WorkspaceIDs {
		params.Add("workspace_ids[]", ws)
	}
	var (
		rows      []json.RawMessage
		refreshed *time.Time
	)
	for n := 0; n < maxPages; n++ {
		p, err := c.do(ctx, endpoint, params, requests)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, p.Data...)
		// Every page reads the same snapshot; the last one's value stands.
		refreshed = p.DataRefreshedAt
		if !p.HasMore {
			return rows, refreshed, nil
		}
		if p.NextPage == nil || *p.NextPage == "" {
			return nil, nil, errors.New("analytics API: has_more without a next_page")
		}
		params.Set("page", *p.NextPage)
	}
	return nil, nil, fmt.Errorf("analytics API: more than %d pages", maxPages)
}

// page is one response.
type page struct {
	Data            []json.RawMessage `json:"data"`
	HasMore         bool              `json:"has_more"`
	NextPage        *string           `json:"next_page"`
	DataRefreshedAt *time.Time        `json:"data_refreshed_at"`
}

// do makes one request, retrying 429, 5xx and broken connections (retry-after,
// else exponential backoff) up to maxTries. The key is set here and only here.
func (c *Client) do(ctx context.Context, endpoint string, params url.Values, requests *int) (page, error) {
	target := strings.TrimRight(c.baseURL(), "/") + "/v1/organizations/analytics/" + endpoint + "?" + params.Encode()
	for try := 1; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return page{}, err
		}
		req.Header.Set("anthropic-version", apiVersion)
		req.Header.Set("anthropic-beta", betaHeader)
		req.Header.Set("x-api-key", c.Key)
		*requests++
		resp, err := c.client().Do(req)
		if err != nil {
			if ctx.Err() != nil || try >= maxTries {
				return page{}, errors.New("analytics API: " + c.scrub(err.Error()))
			}
			if err := c.sleep(ctx, backoff(try)); err != nil {
				return page{}, err
			}
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			var p page
			if err := json.Unmarshal(body, &p); err != nil {
				return page{}, errors.New("analytics API: unreadable answer: " + c.scrub(err.Error()))
			}
			return p, nil
		case resp.StatusCode == http.StatusGone:
			return page{}, errGone
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || rerr != nil:
			if try >= maxTries {
				return page{}, &APIError{Status: resp.StatusCode, Body: c.snippet(body)}
			}
			wait, ok := retryAfter(resp.Header.Get("retry-after"), c.now())
			if !ok {
				wait = backoff(try)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return page{}, err
			}
		default:
			return page{}, &APIError{Status: resp.StatusCode, Body: c.snippet(body)}
		}
	}
}

// retryAfter reads seconds or an HTTP date.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	} else {
		return 0, false
	}
	if d < 0 {
		d = 0
	}
	if d > maxWait {
		d = maxWait
	}
	return d, true
}

// backoff is 1 s, 2 s, 4 s, 8 s, ...
func backoff(try int) time.Duration {
	d := time.Second << (try - 1)
	if d > maxWait {
		d = maxWait
	}
	return d
}

// snippet is what an error may say of a body: the key scrubbed, blanks
// folded, at most errBodyBytes, cut on a character boundary.
func (c *Client) snippet(body []byte) string {
	s := strings.Join(strings.Fields(c.scrub(string(body))), " ")
	if len(s) > errBodyBytes {
		cut := errBodyBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// scrub removes the key from a text that came back from the network, in
// case a server or a proxy echoes it.
func (c *Client) scrub(s string) string {
	if k := strings.TrimSpace(c.Key); k != "" {
		s = strings.ReplaceAll(s, k, "[redacted]")
	}
	return s
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- rows ---

type rawGroup struct {
	APIKeyID    *string `json:"api_key_id"`
	WorkspaceID *string `json:"workspace_id"`
	Model       *string `json:"model"`
	ServiceTier *string `json:"service_tier"`
	CostType    *string `json:"cost_type"`
	TokenType   *string `json:"token_type"`
}

func (g rawGroup) group() Group {
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return Group{APIKeyID: s(g.APIKeyID), WorkspaceID: s(g.WorkspaceID), Model: s(g.Model),
		ServiceTier: s(g.ServiceTier), CostType: s(g.CostType), TokenType: s(g.TokenType)}
}

type rawRow struct {
	StartingAt time.Time `json:"starting_at"`
	EndingAt   time.Time `json:"ending_at"`
	Group      rawGroup  `json:"group"`
	Cost       *struct {
		Amount     json.RawMessage `json:"amount"`
		ListAmount json.RawMessage `json:"list_amount"`
		Currency   string          `json:"currency"`
	} `json:"cost"`
	Usage *struct {
		UncachedInput int64 `json:"uncached_input_tokens"`
		CacheCreation struct {
			Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		} `json:"cache_creation"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		Output        int64 `json:"output_tokens"`
		ServerToolUse struct {
			WebSearch int64 `json:"web_search_requests"`
		} `json:"server_tool_use"`
		Requests *int64 `json:"requests"`
	} `json:"usage"`
}

func parseCost(raw json.RawMessage) (CostRow, error) {
	var r rawRow
	if err := json.Unmarshal(raw, &r); err != nil {
		return CostRow{}, errors.New("analytics API: unreadable cost row: " + err.Error())
	}
	if r.Cost == nil {
		return CostRow{}, errors.New("analytics API: a cost row without cost")
	}
	amount, err := cents(r.Cost.Amount)
	if err != nil {
		return CostRow{}, fmt.Errorf("analytics API: cost.amount: %w", err)
	}
	list, err := cents(r.Cost.ListAmount)
	if err != nil {
		return CostRow{}, fmt.Errorf("analytics API: cost.list_amount: %w", err)
	}
	currency := r.Cost.Currency
	if currency == "" {
		currency = "USD"
	}
	return CostRow{Start: r.StartingAt.UTC(), End: r.EndingAt.UTC(), Group: r.Group.group(),
		AmountCents: amount, ListAmountCents: list, Currency: currency}, nil
}

func parseUsage(raw json.RawMessage) (UsageRow, error) {
	var r rawRow
	if err := json.Unmarshal(raw, &r); err != nil {
		return UsageRow{}, errors.New("analytics API: unreadable usage row: " + err.Error())
	}
	if r.Usage == nil {
		return UsageRow{}, errors.New("analytics API: a usage row without usage")
	}
	u := r.Usage
	row := UsageRow{Start: r.StartingAt.UTC(), End: r.EndingAt.UTC(), Group: r.Group.group(),
		UncachedInput: u.UncachedInput, CacheWrite5m: u.CacheCreation.Ephemeral5m,
		CacheWrite1h: u.CacheCreation.Ephemeral1h, CacheRead: u.CacheRead, Output: u.Output,
		WebSearchRequests: u.ServerToolUse.WebSearch}
	if u.Requests != nil {
		row.Requests = *u.Requests
	}
	return row, nil
}

// decimal is what a cent amount may look like.
var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// cents keeps an amount as the exact text it came as. The API writes a string;
// a bare JSON number is taken by its literal digits, never through a float.
func cents(raw json.RawMessage) (string, error) {
	v := strings.TrimSpace(string(raw))
	if strings.HasPrefix(v, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		v = strings.TrimSpace(s)
	}
	if !decimal.MatchString(v) {
		if v == "" || v == "null" {
			return "", errors.New("missing")
		}
		return "", fmt.Errorf("%q is not a decimal amount", v)
	}
	return v, nil
}
