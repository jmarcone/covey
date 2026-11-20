// Package gitauthor gives the commits an agent pushes an author of the
// platform's choosing.
//
// The github plugin's commit action does not run `git commit`: it writes the
// commit over the Git Data API (blobs, tree, commit, ref) and sends neither an
// author nor a committer, so GitHub records the owner of the token — for a
// shared token, the account behind it, with that account's own address. A
// `git config` in the checkout, or GIT_AUTHOR_* in the environment, never
// reaches that request, which is why the rule in an agent's instructions did
// not hold (#106).
//
// So the platform sets the identity where the request is made: the daemon puts
// the agent's identity into the context of every action, and the transport
// below adds author and committer to a commit-creating request that has none.
// The same identity goes into the environment of the run's own process, for
// the agent that does run git in its sandbox. A `dev agent` sub-run and the
// shells of `dev exec`/`dev start` do not get it; nothing pushes their local
// history, and what a sub-run changes goes out through the commit action.
package gitauthor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Identity is who a commit is attributed to. Both fields are needed: a name
// without an address would fall back to the token's account again, and that is
// the leak this package exists to close.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Valid reports whether the identity can be sent.
func (i Identity) Valid() bool {
	return strings.TrimSpace(i.Name) != "" && strings.TrimSpace(i.Email) != ""
}

// ForAgent builds the identity of an agent: "<slug> (covey agent)" with the
// address the installation configured (COVEY_GIT_AUTHOR_EMAIL). Without an
// address there is no identity — the platform does not invent one.
func ForAgent(slug, email string) Identity {
	slug, email = strings.TrimSpace(slug), strings.TrimSpace(email)
	if slug == "" || email == "" {
		return Identity{}
	}
	return Identity{Name: slug + " (covey agent)", Email: email}
}

// Env is the identity as the variables git itself reads. Empty for an
// identity that is not valid.
func (i Identity) Env() []string {
	if !i.Valid() {
		return nil
	}
	return []string{
		"GIT_AUTHOR_NAME=" + i.Name, "GIT_AUTHOR_EMAIL=" + i.Email,
		"GIT_COMMITTER_NAME=" + i.Name, "GIT_COMMITTER_EMAIL=" + i.Email,
	}
}

type ctxKey struct{}

// WithIdentity attaches the identity to the context of an action.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	if !id.Valid() {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

func identityFrom(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok && id.Valid()
}

// createCommitPath matches GitHub's "create a commit" endpoint, on github.com
// and below an Enterprise Server's /api/v3 alike. Both shapes: a renamed or
// moved repository answers with a redirect to /repositories/{id}/git/commits,
// and the client re-sends the original body there.
var createCommitPath = regexp.MustCompile(`/(repos/[^/]+/[^/]+|repositories/[^/]+)/git/commits$`)

// ErrBodyTooLarge is what a commit-creating request over maxBody gets: it
// cannot be given an author, and sending it without one would record the
// token's own account.
var ErrBodyTooLarge = errors.New("gitauthor: commit body over the cap, refusing to send it without an author")

// maxBody caps what the transport buffers to rewrite. A commit request carries
// a message and SHAs, but the message is the agent's and the pack puts no cap
// on it, so a body above this is refused, not passed on: it would go out
// without author or committer.
const maxBody = 1 << 20

// Transport wraps base (nil = http.DefaultTransport). A request that creates a
// commit and carries no author or committer of its own gets the identity from
// its context; every other request passes through unchanged.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base}
}

type transport struct{ base http.RoundTripper }

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	id, ok := identityFrom(req.Context())
	if !ok || req.Method != http.MethodPost || req.Body == nil || !createCommitPath.MatchString(req.URL.Path) {
		return t.base.RoundTrip(req)
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil {
		req.Body.Close()
		return nil, err
	}
	if len(raw) > maxBody {
		// Fail closed: the body is too big to rewrite, and the request is
		// one that must not go out without an identity.
		req.Body.Close()
		return nil, ErrBodyTooLarge
	}
	// RoundTrip must not modify the request it is given.
	out := req.Clone(req.Context())
	req.Body.Close()
	body := withIdentity(raw, id)
	out.Body = io.NopCloser(bytes.NewReader(body))
	// The request writer sends ContentLength, not a Content-Length header.
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return t.base.RoundTrip(out)
}

// withIdentity adds author and committer to a commit body. A body that is not
// a JSON object, or that names an author or committer already, is returned as
// it came: whoever set one meant it.
func withIdentity(raw []byte, id Identity) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	if _, ok := m["author"]; ok {
		return raw
	}
	if _, ok := m["committer"]; ok {
		return raw
	}
	who, _ := json.Marshal(id)
	m["author"], m["committer"] = who, who
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}
