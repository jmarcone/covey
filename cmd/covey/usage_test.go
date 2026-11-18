package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUsagePushDryRunSendsNothing(t *testing.T) {
	var out bytes.Buffer
	// No URL and no key: a dry run must not need either, and must not try to send.
	err := runUsage([]string{"push", "../../internal/usagepush/testdata/transcripts", "--source", "operator: home", "--dry-run"},
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
		{"push", "--source", "x"}, // no directory
		{"push", "dir"},           // no source
		{"push", "dir", "--source", strings.Repeat("x", 65)},
		{"push", "dir", "--source", "x", "--since", "yesterday"},
		{"push", "dir", "--source", "x", "--meter-secret", "k:one"},
		{"frobnicate"},
	} {
		if err := runUsage(args, &bytes.Buffer{}, env); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
	// A real push without a URL or a key is refused before anything is sent.
	dir := t.TempDir()
	if err := runUsage([]string{"push", dir, "--source", "x"}, &bytes.Buffer{}, env); err == nil || !strings.Contains(err.Error(), "COVEY_URL") {
		t.Errorf("no URL: %v", err)
	}
	if err := runUsage([]string{"push", dir, "--source", "x", "--url", "http://127.0.0.1:1"}, &bytes.Buffer{}, env); err == nil || !strings.Contains(err.Error(), "COVEY_API_KEY") {
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
