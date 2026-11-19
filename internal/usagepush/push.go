package usagepush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxRows and MaxBody are the intake's limits per request
// (observability.ExternalMaxRows, ExternalMaxBodyBytes). A batch stays under
// both, with room to spare on the body.
const (
	MaxRows = 5000
	MaxBody = 2<<20 - 64<<10
)

// Meter names the subscription credential the source shares a meter with.
type Meter struct {
	SecretKey  string `json:"secret_key"`
	SecretSlot int    `json:"secret_slot"`
}

// Request is the body of one POST. Its fields are exactly the intake's.
type Request struct {
	Source          string `json:"source"`
	MeterCredential *Meter `json:"meter_credential,omitempty"`
	Rows            []Row  `json:"rows"`
	// ReportedThrough is the watermark per host: every call of that host up
	// to this time has been sent. The intake splits a meter reading only
	// when every active host of the source has reported past it.
	ReportedThrough map[string]time.Time `json:"reported_through,omitempty"`
}

// Result is the intake's answer to one POST.
type Result struct {
	Source    string `json:"source"`
	Received  int    `json:"received"`
	Inserted  int    `json:"inserted"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
}

// Batches cuts rows into requests that each stay under MaxRows and MaxBody.
// The meter link travels with every batch (the intake keeps the last one, and
// they are all the same). The watermark travels only with the last batch, so
// it is stored only once every earlier batch was accepted; with no rows and a
// watermark there is one request that carries just the watermark.
func Batches(source string, meter *Meter, rows []Row, through map[string]time.Time) ([][]byte, error) {
	var out [][]byte
	if len(rows) == 0 && len(through) > 0 {
		b, err := json.Marshal(Request{Source: source, MeterCredential: meter, Rows: []Row{}, ReportedThrough: through})
		if err != nil {
			return nil, err
		}
		return [][]byte{b}, nil
	}
	for start := 0; start < len(rows); {
		end := start + MaxRows
		if end > len(rows) {
			end = len(rows)
		}
		for {
			req := Request{Source: source, MeterCredential: meter, Rows: rows[start:end]}
			if end == len(rows) {
				req.ReportedThrough = through
			}
			b, err := json.Marshal(req)
			if err != nil {
				return nil, err
			}
			if len(b) <= MaxBody {
				out = append(out, b)
				break
			}
			if end-start == 1 {
				return nil, fmt.Errorf("one row is larger than %d bytes", MaxBody)
			}
			end = start + (end-start)/2
		}
		start = end
	}
	return out, nil
}

// Client posts batches to a covey install.
type Client struct {
	BaseURL string // e.g. http://localhost:8494
	Key     string // covey_… API key; never logged
	HTTP    *http.Client
}

// Post sends one batch and returns the intake's counts.
func (c Client) Post(ctx context.Context, body []byte) (Result, error) {
	var res Result
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/api/v1/cost/external-usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err == nil {
		// Error messages name the URL without any user:password in it.
		url = req.URL.Redacted()
	}
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := hc.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("POST %s: %s: %s", url, resp.Status, strings.TrimSpace(string(msg)))
	}
	if err := json.Unmarshal(msg, &res); err != nil {
		return res, fmt.Errorf("POST %s: unreadable answer: %w", url, err)
	}
	return res, nil
}
