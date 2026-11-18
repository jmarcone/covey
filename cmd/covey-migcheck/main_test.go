package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// repo builds a git repository whose main holds the given migrations and whose
// branch "feature" adds the given files on top.
func repo(t *testing.T, onMain, added []string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(files []string) {
		for _, f := range files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("SELECT 1;\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q", "-b", "main")
	write(append([]string{"README.md"}, onMain...))
	git("add", "-A")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "-b", "feature")
	write(added)
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "feature")
}

// fakeGitHub answers the three reads the check makes. prs maps a pull request
// number to its branch and the files it adds.
type fakePR struct {
	branch string
	added  []string
}

func fakeGitHub(t *testing.T, onMain []string, prs map[int]fakePR, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		p := strings.TrimPrefix(r.URL.Path, "/repos/o/r")
		switch {
		case p == "/contents/migrations":
			if r.URL.Query().Get("ref") != "main" {
				http.Error(w, "wrong ref", http.StatusBadRequest)
				return
			}
			var out []map[string]string
			for _, f := range onMain {
				out = append(out, map[string]string{"path": "migrations/" + f, "type": "file"})
			}
			json.NewEncoder(w).Encode(out)
		case p == "/pulls":
			var out []map[string]any
			for n, pr := range prs {
				out = append(out, map[string]any{"number": n, "head": map[string]string{"ref": pr.branch}})
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(p, "/pulls/") && strings.HasSuffix(p, "/files"):
			var n int
			for k := range prs {
				if strings.Contains(p, "/pulls/"+itoa(k)+"/") {
					n = k
				}
			}
			out := []map[string]string{{"filename": "web/src/x.ts", "status": "modified"}}
			for _, f := range prs[n].added {
				out = append(out, map[string]string{"filename": f, "status": "added"})
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func runCheck(t *testing.T, srv *httptest.Server, extra ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args := append([]string{"-repo", "o/r", "-base", "main", "-head", "feature", "-api", srv.URL}, extra...)
	code := run(context.Background(), args, func(string) string { return "" }, &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestRun(t *testing.T) {
	mainFiles := []string{"0141_org_sso_last_error.up.sql", "0141_org_sso_last_error.down.sql"}
	stage := fakePR{branch: "fix/stage-keys", added: []string{"migrations/0142_stage_keys.up.sql", "migrations/0142_stage_keys.down.sql"}}

	tests := []struct {
		name      string
		onMain    []string // files on main, under migrations/
		added     []string // files the branch adds
		prs       map[int]fakePR
		extra     []string
		wantCode  int
		wantInOut []string
		wantCalls bool
	}{
		{
			name:      "collision with another open PR (AC 2)",
			onMain:    mainFiles,
			added:     []string{"migrations/0142_probe.up.sql"},
			prs:       map[int]fakePR{2: stage},
			wantCode:  exitCollision,
			wantInOut: []string{"migrations/0142_probe.up.sql", "migrations/0142_stage_keys.up.sql", "PR #2", "fix/stage-keys"},
			wantCalls: true,
		},
		{
			name:      "collision with main (AC 1)",
			onMain:    append([]string{"0142_stage_keys.up.sql"}, mainFiles...),
			added:     []string{"migrations/0142_probe.up.sql"},
			wantCode:  exitCollision,
			wantInOut: []string{"migrations/0142_probe.up.sql", "migrations/0142_stage_keys.up.sql", " in main"},
			wantCalls: true,
		},
		{
			name:      "same version and name in another PR (AC 3)",
			onMain:    mainFiles,
			added:     []string{"migrations/0142_stage_keys.up.sql", "migrations/0142_stage_keys.down.sql"},
			prs:       map[int]fakePR{2: stage},
			wantCode:  exitOK,
			wantInOut: []string{"not a collision", "ok:"},
			wantCalls: true,
		},
		{
			name:      "no migration added (AC 4)",
			onMain:    mainFiles,
			added:     []string{"web/src/y.ts"},
			prs:       map[int]fakePR{2: stage},
			wantCode:  exitOK,
			wantInOut: []string{"nothing to check"},
			wantCalls: false,
		},
		{
			name:      "a down file alone is not an added migration",
			onMain:    mainFiles,
			added:     []string{"migrations/0142_x.down.sql"},
			prs:       map[int]fakePR{2: stage},
			wantCode:  exitOK,
			wantInOut: []string{"nothing to check"},
		},
		{
			name:      "the PR under check is not its own other side",
			onMain:    mainFiles,
			added:     []string{"migrations/0142_probe.up.sql"},
			prs:       map[int]fakePR{2: {branch: "ci/probe", added: []string{"migrations/0142_probe.up.sql"}}},
			extra:     []string{"-pr", "2"},
			wantCode:  exitOK,
			wantInOut: []string{"ok:"},
			wantCalls: true,
		},
		{
			name:      "a free number passes",
			onMain:    mainFiles,
			added:     []string{"migrations/0149_new.up.sql"},
			prs:       map[int]fakePR{2: stage},
			wantCode:  exitOK,
			wantInOut: []string{"ok: 1 added migration(s) checked against main and 1 other open PR(s)"},
			wantCalls: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var inRepo []string
			for _, f := range tc.onMain {
				inRepo = append(inRepo, "migrations/"+f)
			}
			repo(t, inRepo, tc.added)
			srv := fakeGitHub(t, tc.onMain, tc.prs, &calls)
			code, out := runCheck(t, srv, tc.extra...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s", code, tc.wantCode, out)
			}
			for _, s := range tc.wantInOut {
				if !strings.Contains(out, s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			if got := calls.Load() > 0; got != tc.wantCalls {
				t.Errorf("API called = %v, want %v", got, tc.wantCalls)
			}
		})
	}
}

// An API that fails must not read as "no collision".
func TestRunAPIError(t *testing.T) {
	repo(t, []string{"migrations/0141_a.up.sql"}, []string{"migrations/0142_probe.up.sql"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	code, out := runCheck(t, srv)
	if code != exitError {
		t.Fatalf("exit %d, want %d\n%s", code, exitError, out)
	}
	if !strings.Contains(out, "rate limit") {
		t.Errorf("output does not carry the API's reason:\n%s", out)
	}
}

func TestRunAnnotation(t *testing.T) {
	repo(t, []string{"migrations/0141_a.up.sql", "migrations/0142_x.up.sql"}, []string{"migrations/0142_probe.up.sql"})
	var calls atomic.Int32
	srv := fakeGitHub(t, []string{"0141_a.up.sql", "0142_x.up.sql"}, nil, &calls)
	var out, errOut bytes.Buffer
	env := map[string]string{"GITHUB_ACTIONS": "true"}
	code := run(context.Background(), []string{"-repo", "o/r", "-base", "main", "-head", "feature", "-api", srv.URL},
		func(k string) string { return env[k] }, &out, &errOut)
	if code != exitCollision {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "::error file=migrations/0142_probe.up.sql,title=Migration number collision::") {
		t.Errorf("no annotation:\n%s", out.String())
	}
}
