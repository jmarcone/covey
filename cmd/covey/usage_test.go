package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUsagePushDryRunSendsNothing(t *testing.T) {
	var out bytes.Buffer
	// No URL and no key: a dry run must not need either, and must not try to send.
	err := runUsage([]string{"push", "../../internal/usagepush/testdata/transcripts", "--source", "operator: home", "--host", "home-wsl", "--dry-run"},
		&out, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dry run, nothing sent: 806 calls") {
		t.Fatalf("dry run said:\n%s", out.String())
	}
}

func TestUsagePushRefusesBadInput(t *testing.T) {
	env := func(string) string { return "" }
	for _, args := range [][]string{
		{"push", "--source", "x", "--host", "h"}, // no directory
		{"push", "dir", "--source", "x"},         // no host: the machine's name is no default
		{"push", "dir"},                          // no source
		{"push", "dir", "--source", strings.Repeat("x", 65)},
		{"push", "dir", "--source", "x", "--since", "yesterday"},
		{"push", "dir", "--source", "x", "--meter-secret", "k:one"},
		{"frobnicate"},
		{"push", "dir", "--source", "x", "--label", "a=b"},           // --label needs --source-per-project
		{"push", "dir", "--source", "x", "--home-prefix", "/home/u"}, // so does --home-prefix
		{"push", "dir", "--source-per-project", "--label", "nolabel"},
		{"push", "dir", "--source-per-project", "--label", "a=b", "--label", "a=c"},
		{"push", "dir", "--source-per-project", "--no-meter", "--source", strings.Repeat("x", 65)},
		{"push", "dir", "--source", "x", "--no-meter"},                               // --no-meter needs --source-per-project
		{"push", "dir", "--source-per-project", "--no-meter", "--meter-secret", "k"}, // one or the other
	} {
		if err := runUsage(args, &bytes.Buffer{}, env); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
	// A real push without a URL or a key is refused before anything is sent.
	dir := t.TempDir()
	if err := runUsage([]string{"push", dir, "--source", "x", "--host", "h"}, &bytes.Buffer{}, env); err == nil || !strings.Contains(err.Error(), "COVEY_URL") {
		t.Errorf("no URL: %v", err)
	}
	if err := runUsage([]string{"push", dir, "--source", "x", "--host", "h", "--url", "http://127.0.0.1:1"}, &bytes.Buffer{}, env); err == nil || !strings.Contains(err.Error(), "COVEY_API_KEY") {
		t.Errorf("no key: %v", err)
	}
}

func TestUsageKeyFileMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permissions")
	}
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte("covey_secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := apiKey(p, nil); err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Fatalf("0644 key file: %v", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := apiKey(p, nil)
	if err != nil || k != "covey_secret" {
		t.Fatalf("0600 key file: %q %v", k, err)
	}
	if k, err := apiKey("", func(string) string { return " covey_env " }); err != nil || k != "covey_env" {
		t.Fatalf("env key: %q %v", k, err)
	}
}

// pushTo runs a real push against a stub intake and returns the bodies it got.
func pushTo(t *testing.T, args ...string) (bodies []map[string]any, out, errOut string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Write([]byte(`{"source":"s","received":1,"inserted":1,"updated":0,"unchanged":0}`))
	}))
	defer srv.Close()
	var o, e bytes.Buffer
	old := usageStderr
	usageStderr = &e
	defer func() { usageStderr = old }()
	env := func(k string) string {
		return map[string]string{"COVEY_URL": srv.URL, "COVEY_API_KEY": "covey_k"}[k]
	}
	err = runUsage(append([]string{"push"}, args...), &o, env)
	return bodies, o.String(), e.String(), err
}

func transcriptDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"type":"assistant","timestamp":"2026-11-18T10:00:00Z","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The watermark is a claim that every call of the host is in the push. It is
// sent only with --complete, and never when --since or an unreadable line
// means calls may be missing.
func TestUsagePushSendsTheWatermarkOnlyWhenItCanClaim(t *testing.T) {
	dir := transcriptDir(t)
	has := func(args ...string) (bool, string) {
		bodies, out, _, err := pushTo(t, append([]string{dir, "--source", "x", "--host", "h1"}, args...)...)
		if err != nil || len(bodies) != 1 {
			t.Fatalf("%v: %v, %d bodies", args, err, len(bodies))
		}
		_, ok := bodies[0]["reported_through"]
		return ok, out
	}
	if ok, out := has(); ok || !strings.Contains(out, "waits until a --complete push") {
		t.Errorf("without --complete: watermark %v, output %q", ok, out)
	}
	if ok, _ := has("--complete"); !ok {
		t.Error("--complete: no watermark")
	}
	if ok, out := has("--complete", "--since", "2026-11-01T00:00:00Z"); ok || !strings.Contains(out, "--since") {
		t.Errorf("--since: watermark %v, output %q", ok, out)
	}
	// a symlinked directory below the root: warned about, no watermark
	other := transcriptDir(t)
	if err := os.Symlink(other, filepath.Join(dir, "linked")); err == nil {
		bodies, _, errOut, err := pushTo(t, dir, "--source", "x", "--host", "h1", "--complete")
		if err != nil || len(bodies) != 1 || bodies[0]["reported_through"] != nil || !strings.Contains(errOut, "symlinked") {
			t.Errorf("symlink: %v %v %q", err, bodies, errOut)
		}
		os.Remove(filepath.Join(dir, "linked"))
	}
	// a dangling link: what it pointed to may hold calls, no watermark
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(dir, "dangling")); err == nil {
		bodies, out, _, err := pushTo(t, dir, "--source", "x", "--host", "h1", "--complete")
		if err != nil || len(bodies) != 1 || bodies[0]["reported_through"] != nil || !strings.Contains(out, "neither a file nor a directory") {
			t.Errorf("dangling link: %v %v %q", err, bodies, out)
		}
		os.Remove(filepath.Join(dir, "dangling"))
	}
	// an unreadable line in a settled file
	bad := filepath.Join(dir, "bad.jsonl")
	os.WriteFile(bad, []byte("{\"type\":\"assistant\",\"message\":{\"usage\":{\n{}\n"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(bad, old, old)
	bodies, out, _, err := pushTo(t, dir, "--source", "x", "--host", "h1", "--complete")
	if err != nil || len(bodies) != 1 || bodies[0]["reported_through"] != nil || !strings.Contains(out, "1 lines skipped") {
		t.Errorf("unreadable line: %v %v %q", err, bodies, out)
	}
}

func TestUsagePushWatermarkMargin(t *testing.T) {
	bodies, _, _, err := pushTo(t, transcriptDir(t), "--source", "x", "--host", "h1", "--complete")
	if err != nil {
		t.Fatal(err)
	}
	rt := bodies[0]["reported_through"].(map[string]any)
	at, _ := time.Parse(time.RFC3339, rt["h1"].(string))
	if d := time.Since(at); d < 15*time.Minute || d > 16*time.Minute {
		t.Errorf("watermark %v behind now, want 15 minutes", d)
	}
}

func TestUsagePushRefusesAnEmptyRootAmongOthers(t *testing.T) {
	err := runUsage([]string{"push", transcriptDir(t), t.TempDir(), "--source", "x", "--host", "h", "--dry-run"}, &bytes.Buffer{}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "no *.jsonl") {
		t.Fatalf("%v", err)
	}
}

func TestUsagePushHostMustBeValidText(t *testing.T) {
	for _, h := range []string{"a\x00b", "\xff\xfe", strings.Repeat("h", 257), "", " "} {
		err := runUsage([]string{"push", transcriptDir(t), "--source", "x", "--host", h, "--dry-run"}, &bytes.Buffer{}, func(string) string { return "" })
		if err == nil {
			t.Errorf("host %q accepted", h)
		}
	}
}

func TestWarnPlainHTTP(t *testing.T) {
	for u, want := range map[string]bool{
		"http://covey.example.com": true, "http://10.0.0.5:8494": true,
		"http://localhost:8494": false, "http://127.0.0.1:8494": false, "http://[::1]:8494": false,
		"https://covey.example.com": false, "http://covey.localhost": false,
	} {
		var b bytes.Buffer
		warnPlainHTTP(&b, u)
		if (b.Len() > 0) != want {
			t.Errorf("%s: warned %v, want %v", u, b.Len() > 0, want)
		}
	}
}

// What the user needs to fix a withheld claim: file:line, never the content,
// twenty at most.
func TestUsagePushListsTheLinesThatBlockTheClaim(t *testing.T) {
	dir := transcriptDir(t)
	bad := filepath.Join(dir, "bad.jsonl")
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, `{"type":"assistant","message":{"usage":{"SECRETTEXT`)
	}
	lines = append(lines, "{}")
	if err := os.WriteFile(bad, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bodies, out, _, err := pushTo(t, dir, "--source", "x", "--host", "h1", "--complete")
	if err != nil || len(bodies) != 1 || bodies[0]["reported_through"] != nil {
		t.Fatalf("%v %v", err, bodies)
	}
	if !strings.Contains(out, "bad.jsonl:1\n") || !strings.Contains(out, "bad.jsonl:20\n") || strings.Contains(out, "bad.jsonl:21\n") ||
		!strings.Contains(out, "and 5 more") || strings.Contains(out, "SECRETTEXT") {
		t.Errorf("output:\n%s", out)
	}
}

// A broken last line is taken for one still being written only in a file
// changed after the claim time; then the claim stays below that change.
// One older (20 minutes, inside the old 30-minute window) withholds it, as
// does a line cut short before its "usage", wherever it is.
func TestUsagePushBrokenLinesAndTheClaim(t *testing.T) {
	claim := func(name string, age time.Duration, lines ...string) (time.Time, bool, string) {
		t.Helper()
		dir := transcriptDir(t)
		f := filepath.Join(dir, name)
		os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		at := time.Now().Add(-age)
		os.Chtimes(f, at, at)
		bodies, out, _, err := pushTo(t, dir, "--source", "x", "--host", "h1", "--complete")
		if err != nil || len(bodies) != 1 {
			t.Fatalf("%s: %v %d", name, err, len(bodies))
		}
		rt, ok := bodies[0]["reported_through"].(map[string]any)
		if !ok {
			return time.Time{}, false, out
		}
		ts, _ := time.Parse(time.RFC3339, rt["h1"].(string))
		return ts, true, out + " mtime " + at.UTC().Format(time.RFC3339)
	}
	call := `{"type":"assistant","timestamp":"2026-11-18T10:00:00Z","message":{"id":"m9","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":2}}}`
	cutCall := `{"type":"assistant","message":{"usage":{"input_tok`
	cutUser := `{"type":"user","message":{"content":"a long answer, cut off by a killed pro`
	if _, ok, out := claim("old.jsonl", 20*time.Minute, call, cutCall); ok {
		t.Errorf("a broken last line 20 minutes old: claim sent\n%s", out)
	}
	if _, ok, out := claim("cut.jsonl", 2*time.Hour, cutUser, call); ok || !strings.Contains(out, "cut.jsonl:1") {
		t.Errorf("a line cut before its usage: claim sent\n%s", out)
	}
	if _, ok, out := claim("cutlast.jsonl", 20*time.Minute, call, cutUser); ok {
		t.Errorf("a cut last line 20 minutes old: claim sent\n%s", out)
	}
	at, ok, out := claim("fresh.jsonl", time.Minute, call, cutUser)
	if !ok || !at.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("a line being written a minute ago: claim %v %v\n%s", at, ok, out)
	}
}
