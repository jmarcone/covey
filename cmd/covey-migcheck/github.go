package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// perPage is the largest page GitHub hands out.
const perPage = 100

// github reads what the check needs, and nothing it could write.
type github struct {
	base  string // API root, without a trailing slash
	repo  string // owner/name
	token string // empty: anonymous, which GitHub limits to 60 requests an hour
}

type pullRequest struct {
	Number int `json:"number"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// get decodes one JSON answer; the caller names the query.
func (g *github) get(ctx context.Context, p string, q url.Values, into any) error {
	u := g.base + "/repos/" + g.repo + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "covey-migcheck")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", p, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("GET %s: %w", p, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("GET %s: %s: %s", p, resp.Status, msg)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("GET %s: %w", p, err)
	}
	return nil
}

// listDir returns the paths of the files directly in dir on ref.
func (g *github) listDir(ctx context.Context, dir, ref string) ([]string, error) {
	var entries []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := g.get(ctx, "/contents/"+dir, url.Values{"ref": {ref}}, &entries); err != nil {
		return nil, err
	}
	// The contents API lists at most 1000 entries of a directory and does not
	// say it cut the list. A truncated main would read as "no collision" for
	// the newest migrations — exactly the ones that matter.
	if len(entries) >= 1000 {
		return nil, fmt.Errorf("%s on %s has %d entries, the contents API may have cut the list; read it with the git trees API instead", dir, ref, len(entries))
	}
	var out []string
	for _, e := range entries {
		if e.Type == "file" {
			out = append(out, path.Clean(e.Path))
		}
	}
	return out, nil
}

// openPRs lists every open pull request, drafts included.
func (g *github) openPRs(ctx context.Context) ([]pullRequest, error) {
	var all []pullRequest
	for page := 1; ; page++ {
		var prs []pullRequest
		q := url.Values{"state": {"open"}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		if err := g.get(ctx, "/pulls", q, &prs); err != nil {
			return nil, err
		}
		all = append(all, prs...)
		if len(prs) < perPage {
			return all, nil
		}
	}
}

// addedFiles lists the files a pull request adds, as GitHub diffs it against
// the PR's own base. A file the PR renamed counts under its new name.
func (g *github) addedFiles(ctx context.Context, pr int) ([]string, error) {
	var out []string
	// GitHub serves at most 3000 files of a pull request, 30 pages of 100.
	const maxPages = 30
	for page := 1; ; page++ {
		var files []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
		}
		q := url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		if err := g.get(ctx, "/pulls/"+strconv.Itoa(pr)+"/files", q, &files); err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Status == "added" || f.Status == "renamed" {
				out = append(out, f.Filename)
			}
		}
		if len(files) < perPage {
			return out, nil
		}
		if page == maxPages {
			return nil, fmt.Errorf("PR #%d changes 3000 files or more, GitHub lists no further", pr)
		}
	}
}
