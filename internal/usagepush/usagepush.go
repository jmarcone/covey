// Package usagepush reads Claude Code transcripts on the machine they were
// written on and turns them into the token-only rows that
// POST /api/v1/cost/external-usage takes (#120, part B).
//
// Only counts leave the machine: the message id, the session id, the model,
// the timestamp and the five token counts. No prompt, response, tool input or
// tool output is ever read into a Row.
//
// The parsing is a port of covey-experiment scripts/measure/harness_usage.py,
// the script the operator's measurement was made with, so that covey's figures
// and the measurement agree:
//   - every *.jsonl under the root, recursively (main sessions, subagents/,
//     workflows/);
//   - a row is an "assistant" line whose message carries an id and a usage;
//   - streamed copies of one message id: the copy with the most output tokens
//     wins, a later copy with equal output replaces an earlier one;
//   - cache writes: cache_creation.ephemeral_1h_input_tokens is the 1-hour
//     write, ephemeral_5m_input_tokens the 5-minute write; without the split,
//     cache_creation_input_tokens minus the 1-hour part counts as 5-minute.
package usagepush

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Row is one model call, in exactly the shape the intake takes. Nothing else
// is sent: the intake refuses unknown fields, and this type has no room for
// text.
type Row struct {
	Host         string    `json:"host"`
	SessionID    string    `json:"session_id"`
	MessageID    string    `json:"message_id"`
	TS           time.Time `json:"ts"`
	Model        string    `json:"model"`
	Input        int64     `json:"input"`
	Output       int64     `json:"output"`
	CacheRead    int64     `json:"cache_read"`
	CacheWrite5m int64     `json:"cache_write_5m"`
	CacheWrite1h int64     `json:"cache_write_1h"`
	Origin       string    `json:"origin"`
}

// Origins of a call, from the transcript's path.
const (
	OriginMain     = "main"
	OriginSubagent = "subagent"
	OriginWorkflow = "workflow"
)

// SyntheticModel is the model Claude Code writes on placeholder messages it
// makes up itself (an interrupted or failed turn). They carry zero tokens and
// were never a model call, so they are not sent.
const SyntheticModel = "<synthetic>"

// transcript line, only the fields we read. Everything else in the line
// (content, tool calls) is never decoded into a Go value.
type line struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// Stats says what a read found besides the rows.
type Stats struct {
	Files     int // transcript files read
	Lines     int // assistant lines with a usage
	Copies    int // streamed copies dropped by the dedup
	Synthetic int // placeholder messages left out
	Skipped   int // lines with a usage but no id, model or valid timestamp
}

// Read walks each root for *.jsonl and returns one Row per message id, sorted
// by (ts, message id). host is stored on every row. Rows before since are
// left out (zero since keeps everything).
func Read(roots []string, host string, since time.Time) ([]Row, Stats, error) {
	var st Stats
	type kept struct {
		row Row
	}
	seen := map[string]*kept{}
	for _, root := range roots {
		var files []string
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, st, fmt.Errorf("read %s: %w", root, err)
		}
		sort.Strings(files)
		for _, f := range files {
			rel, err := filepath.Rel(root, f)
			if err != nil {
				return nil, st, err
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			session := strings.TrimSuffix(parts[0], ".jsonl")
			origin := originOf(parts)
			st.Files++
			if err := readFile(f, func(l *line) {
				m := l.Message
				st.Lines++
				if m.ID == "" || m.Model == "" {
					st.Skipped++
					return
				}
				ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
				if err != nil {
					st.Skipped++
					return
				}
				u := m.Usage
				var w1h int64
				var w5m = u.CacheCreationInputTokens
				if cc := u.CacheCreation; cc != nil {
					if cc.Ephemeral1h != nil {
						w1h = *cc.Ephemeral1h
					}
					if cc.Ephemeral5m != nil {
						w5m = *cc.Ephemeral5m
					} else {
						w5m = u.CacheCreationInputTokens - w1h
					}
				}
				if w5m < 0 {
					w5m = 0
				}
				r := Row{
					Host: host, SessionID: session, MessageID: m.ID, TS: ts.UTC(), Model: m.Model,
					Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens,
					CacheWrite5m: w5m, CacheWrite1h: w1h, Origin: origin,
				}
				if prev, ok := seen[m.ID]; ok {
					st.Copies++
					if r.Output >= prev.row.Output {
						prev.row = r
					}
					return
				}
				seen[m.ID] = &kept{row: r}
			}); err != nil {
				return nil, st, fmt.Errorf("read %s: %w", f, err)
			}
		}
	}
	rows := make([]Row, 0, len(seen))
	for _, k := range seen {
		if k.row.Model == SyntheticModel {
			st.Synthetic++
			continue
		}
		if !since.IsZero() && k.row.TS.Before(since) {
			continue
		}
		rows = append(rows, k.row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].TS.Equal(rows[j].TS) {
			return rows[i].TS.Before(rows[j].TS)
		}
		return rows[i].MessageID < rows[j].MessageID
	})
	return rows, st, nil
}

// originOf names the origin by the transcript's path below the root:
// <session>.jsonl is the main session; anything under a workflows/ directory
// is a workflow agent; anything else below the session directory (subagents/)
// is a subagent.
func originOf(parts []string) string {
	if len(parts) == 1 {
		return OriginMain
	}
	for _, p := range parts[:len(parts)-1] {
		if p == "workflows" {
			return OriginWorkflow
		}
	}
	return OriginSubagent
}

func readFile(path string, each func(*line)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		b := sc.Bytes()
		// Cheap filter before decoding, as the script does: most lines are
		// user turns and tool results without a usage.
		if !strings.Contains(string(b), `"usage"`) {
			continue
		}
		var l line
		if json.Unmarshal(b, &l) != nil {
			continue
		}
		if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
			continue
		}
		each(&l)
	}
	return sc.Err()
}

// Totals sums rows per origin and model, for --dry-run and the tests.
type Totals struct {
	Rows         int   `json:"rows"`
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

func (t *Totals) add(r Row) {
	t.Rows++
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite5m += r.CacheWrite5m
	t.CacheWrite1h += r.CacheWrite1h
}

// Summary is what --dry-run prints.
type Summary struct {
	First    time.Time          `json:"first"`
	Last     time.Time          `json:"last"`
	All      Totals             `json:"all"`
	ByOrigin map[string]*Totals `json:"by_origin"`
	ByModel  map[string]*Totals `json:"by_model"`
}

// Summarize totals the rows.
func Summarize(rows []Row) Summary {
	s := Summary{ByOrigin: map[string]*Totals{}, ByModel: map[string]*Totals{}}
	for i, r := range rows {
		if i == 0 || r.TS.Before(s.First) {
			s.First = r.TS
		}
		if r.TS.After(s.Last) {
			s.Last = r.TS
		}
		s.All.add(r)
		for _, m := range []struct {
			to  map[string]*Totals
			key string
		}{{s.ByOrigin, r.Origin}, {s.ByModel, r.Model}} {
			t := m.to[m.key]
			if t == nil {
				t = &Totals{}
				m.to[m.key] = t
			}
			t.add(r)
		}
	}
	return s
}
