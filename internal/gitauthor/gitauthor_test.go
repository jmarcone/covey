package gitauthor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testID = Identity{Name: "covey-dev (covey agent)", Email: "1+covey-bot@users.noreply.github.com"}

// post sends body to path through the transport and returns what the server
// received.
func post(t *testing.T, ctx context.Context, method, path, body string) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if int64(len(b)) != r.ContentLength {
			t.Errorf("content length %d, body %d bytes", r.ContentLength, len(b))
		}
		_ = json.Unmarshal(b, &got)
	}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	resp, err := (&http.Client{Transport: Transport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return got
}

func TestCommitRequestGetsAuthorAndCommitter(t *testing.T) {
	for _, path := range []string{"/repos/o/r/git/commits", "/api/v3/repos/o/r/git/commits", "/repositories/1/git/commits", "/api/v3/repositories/1/git/commits"} {
		got := post(t, WithIdentity(context.Background(), testID), "POST", path,
			`{"message":"m","tree":"t","parents":["p"]}`)
		for _, k := range []string{"author", "committer"} {
			who, _ := got[k].(map[string]any)
			if who["name"] != testID.Name || who["email"] != testID.Email {
				t.Errorf("%s: %s = %v, want %v", path, k, got[k], testID)
			}
		}
		if got["message"] != "m" || got["tree"] != "t" {
			t.Errorf("%s: the rest of the body was lost: %v", path, got)
		}
	}
}

func TestRequestsThatAreNotCommitsPassUntouched(t *testing.T) {
	ctx := WithIdentity(context.Background(), testID)
	for _, c := range []struct{ method, path string }{
		{"POST", "/repos/o/r/git/trees"},
		{"POST", "/repos/o/r/git/commits/abc/comments"},
		{"GET", "/repos/o/r/git/commits"},
		{"POST", "/repositories/1/git/commits/abc/comments"},
	} {
		got := post(t, ctx, c.method, c.path, `{"message":"m"}`)
		if _, ok := got["author"]; ok {
			t.Errorf("%s %s got an author", c.method, c.path)
		}
	}
}

func TestNoIdentityNoChange(t *testing.T) {
	got := post(t, context.Background(), "POST", "/repos/o/r/git/commits", `{"message":"m"}`)
	if _, ok := got["author"]; ok {
		t.Error("a request without an identity on its context got an author")
	}
}

func TestAnAuthorAlreadyThereIsKept(t *testing.T) {
	got := post(t, WithIdentity(context.Background(), testID), "POST", "/repos/o/r/git/commits",
		`{"message":"m","author":{"name":"x","email":"x@example.com"}}`)
	if who, _ := got["author"].(map[string]any); who["name"] != "x" {
		t.Errorf("author overwritten: %v", got["author"])
	}
	if _, ok := got["committer"]; ok {
		t.Error("committer added next to a caller's author")
	}

	got = post(t, WithIdentity(context.Background(), testID), "POST", "/repos/o/r/git/commits",
		`{"message":"m","committer":{"name":"y","email":"y@example.com"}}`)
	if who, _ := got["committer"].(map[string]any); who["name"] != "y" {
		t.Errorf("committer overwritten: %v", got["committer"])
	}
	if _, ok := got["author"]; ok {
		t.Error("author added next to a caller's committer")
	}
}

// A create-commit body above maxBody cannot be given an author, so it is
// refused: sent on, it would make GitHub record the token's own account.
func TestACommitBodyOverTheCapIsRefused(t *testing.T) {
	body := `{"message":"` + strings.Repeat("x", 2*maxBody) + `"}`
	for _, path := range []string{"/repos/o/r/git/commits", "/repositories/1/git/commits"} {
		hit := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
		req, _ := http.NewRequestWithContext(WithIdentity(context.Background(), testID), "POST",
			srv.URL+path, strings.NewReader(body))
		resp, err := (&http.Client{Transport: Transport(nil)}).Do(req)
		srv.Close()
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: an over-cap commit body was sent", path)
		}
		if !errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("%s: error = %v, want ErrBodyTooLarge", path, err)
		}
		if hit {
			t.Errorf("%s: the server got a request without an author", path)
		}
	}
}

// The cap only concerns the commit endpoint: another request with a large body
// (a blob) still arrives whole.
func TestALargeBodyToAnotherEndpointArrivesWhole(t *testing.T) {
	body := `{"content":"` + strings.Repeat("x", 2*maxBody) + `"}`
	var got []byte
	var length int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		length = r.ContentLength
	}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(WithIdentity(context.Background(), testID), "POST",
		srv.URL+"/repos/o/r/git/blobs", strings.NewReader(body))
	resp, err := (&http.Client{Transport: Transport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(got) != body || length != int64(len(body)) {
		t.Errorf("server got %d bytes (content length %d), want %d", len(got), length, len(body))
	}
}

// A repository that was renamed or moved answers with a redirect to
// /repositories/{id}/git/commits, and the client re-sends the caller's original
// body there. The second hop goes through the transport again and must get the
// identity too (307 and 308 keep method and body).
func TestARedirectedCommitRequestGetsTheIdentityToo(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var got map[string]any
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.URL.Path == "/repos/o/old/git/commits" {
				http.Redirect(w, r, "/repositories/1/git/commits", code)
				return
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
		}))
		req, _ := http.NewRequestWithContext(WithIdentity(context.Background(), testID), "POST",
			srv.URL+"/repos/o/old/git/commits", strings.NewReader(`{"message":"m"}`))
		resp, err := (&http.Client{Transport: Transport(nil)}).Do(req)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(paths) != 2 || paths[1] != "/repositories/1/git/commits" {
			t.Fatalf("%d: paths = %v, want the redirect followed", code, paths)
		}
		for _, k := range []string{"author", "committer"} {
			who, _ := got[k].(map[string]any)
			if who["name"] != testID.Name || who["email"] != testID.Email {
				t.Errorf("%d: after the redirect %s = %v, want %v", code, k, got[k], testID)
			}
		}
	}
}

func TestForAgent(t *testing.T) {
	if id := ForAgent("covey-dev", " a@b "); id.Name != "covey-dev (covey agent)" || id.Email != "a@b" {
		t.Errorf("ForAgent = %+v", id)
	}
	if ForAgent("covey-dev", "").Valid() || ForAgent("", "a@b").Valid() {
		t.Error("an identity without a slug or an address must not be valid")
	}
	if ForAgent("", "").Env() != nil {
		t.Error("an invalid identity must set no GIT_* variables")
	}
	env := strings.Join(testID.Env(), "\n")
	for _, k := range []string{"GIT_AUTHOR_NAME=" + testID.Name, "GIT_AUTHOR_EMAIL=" + testID.Email,
		"GIT_COMMITTER_NAME=" + testID.Name, "GIT_COMMITTER_EMAIL=" + testID.Email} {
		if !strings.Contains(env, k) {
			t.Errorf("env lacks %q", k)
		}
	}
}
