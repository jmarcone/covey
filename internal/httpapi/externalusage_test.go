package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"covey/internal/identity"
)

// An upload into a name that reads as covey's own meter poll is refused
// before the store is asked (the server here has none), whatever its case
// and surrounding space, with the 400 any other refused source gets.
func TestExternalUploadRefusesMeterPollSources(t *testing.T) {
	s := &Server{}
	for _, src := range []string{
		"meter poll", "meter poll: claude_code_oauth_token#0", "Meter Poll", "METER POLL: x#1",
		"  meter poll: x#0", "meter poll\t", "\nMeter Poll:x", "meter polling", "Meter Poll~abc",
	} {
		body, _ := json.Marshal(map[string]any{"source": src, "rows": []any{}})
		r := httptest.NewRequest(http.MethodPost, "/api/usage/external", strings.NewReader(string(body)))
		r = r.WithContext(context.WithValue(r.Context(), principalKey, identity.Principal{OrgID: uuid.New()}))
		w := httptest.NewRecorder()
		s.handlePostExternalUsage(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", src, w.Code)
			continue
		}
		var res map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !strings.Contains(res["error"], "meter poll") {
			t.Errorf("%q: body %s", src, w.Body.String())
		}
	}
	for _, src := range []string{"operator: home", "my meter poll", "meterpoll"} {
		if err := uploadSourceAllowed(src); err != nil {
			t.Errorf("%q refused: %v", src, err)
		}
	}
}
