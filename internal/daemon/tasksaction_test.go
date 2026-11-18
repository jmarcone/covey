package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeControlPlane answers request_tasks over a real WebSocket and keeps what
// arrived: the requests and the events. It stands in for the control plane so
// the proxy's half of covey/list_tasks and covey/get_task is tested without a
// database.
type fakeControlPlane struct {
	mu       sync.Mutex
	requests []RequestTasks
	events   []map[string]any
	answer   func(RequestTasks) InjectTasks
}

func (f *fakeControlPlane) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			switch msg.Type {
			case TypeRequestTasks:
				req, _ := DecodePayload[RequestTasks](msg)
				f.mu.Lock()
				f.requests = append(f.requests, req)
				f.mu.Unlock()
				resp := f.answer(req)
				resp.RequestID = req.RequestID
				out, _ := Encode(TypeInjectTasks, resp)
				b, _ := json.Marshal(out)
				_ = conn.Write(r.Context(), websocket.MessageText, b)
			case TypeEvent:
				ev, _ := DecodePayload[Event](msg)
				var payload map[string]any
				_ = json.Unmarshal(ev.Payload, &payload)
				f.mu.Lock()
				f.events = append(f.events, payload)
				f.mu.Unlock()
			}
		}
	}))
}

// got is a copy of the requests so far. The answer is written after the
// request is kept, so a request whose answer arrived is in it.
func (f *fakeControlPlane) got() []RequestTasks {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RequestTasks(nil), f.requests...)
}

func (f *fakeControlPlane) audit(action string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, e := range f.events {
		if e["action"] == action {
			out = append(out, e)
		}
	}
	return out
}

// connectedProxy is an action proxy whose client talks to the fake.
func connectedProxy(t *testing.T, f *fakeControlPlane) *actionProxy {
	t.Helper()
	srv := f.serve(t)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	c := &Client{pending: map[string]chan Message{}, conn: conn}
	go func() {
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(raw, &msg) == nil {
				c.deliverIfResponse(msg)
			}
		}
	}()
	return &actionProxy{client: c, taskID: "task-1",
		tools: []ActionTool{{Name: "covey", Description: "The platform's own actions"}}}
}

// waitAudit waits for the audit event, which travels after the answer.
func waitAudit(t *testing.T, f *fakeControlPlane, action string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.audit(action); len(got) > 0 {
			return got[len(got)-1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no audit event %s", action)
	return nil
}

// covey/list_tasks and covey/get_task are carried to the control plane with
// their parameters, answered with its data, and recorded as action events —
// on the shell route and on the MCP route alike.
func TestTaskActionsReachTheControlPlane(t *testing.T) {
	f := &fakeControlPlane{answer: func(req RequestTasks) InjectTasks {
		if req.Op == "get" && req.ID != "abcdef01" {
			return InjectTasks{OK: false, Error: `no task "` + req.ID + `" among your own or created tasks`}
		}
		return InjectTasks{OK: true, Data: json.RawMessage(`[{"id":"abcdef01-0000-4000-8000-000000000001","title":"Plan"}]`)}
	}}
	p := connectedProxy(t, f)
	ctx := context.Background()

	res, _ := p.run(ctx, "covey", "list_tasks",
		json.RawMessage(`{"scope":"created","state":["open","done"],"archived":true,"limit":7}`)).(map[string]any)
	if res["status"] != "ok" {
		t.Fatalf("list_tasks: %v", res)
	}
	if data, _ := json.Marshal(res["data"]); !strings.Contains(string(data), `"title":"Plan"`) {
		t.Fatalf("list_tasks hands the control plane's data through: %s", data)
	}
	req := f.got()[0]
	if req.Op != "list" || req.Scope != "created" || !req.Archived || req.Limit != 7 ||
		len(req.States) != 2 || req.States[0] != "open" || req.States[1] != "done" {
		t.Fatalf("list_tasks request: %+v", req)
	}
	if ev := waitAudit(t, f, "covey:list_tasks"); ev["ok"] != true || ev["scope"] != "created" || ev["count"] != float64(1) {
		t.Fatalf("list_tasks audit event: %v", ev)
	}

	// A single state may be a string.
	p.run(ctx, "covey", "list_tasks", json.RawMessage(`{"state":"blocked"}`))
	if req := f.got()[1]; len(req.States) != 1 || req.States[0] != "blocked" {
		t.Fatalf("a single state as a string: %+v", req)
	}
	// Anything else for state is refused before it travels.
	if res, _ := p.run(ctx, "covey", "list_tasks", json.RawMessage(`{"state":3}`)).(map[string]string); res["status"] != "error" {
		t.Fatalf("state 3 has to be refused, got %v", res)
	}
	if n := len(f.got()); n != 2 {
		t.Fatalf("a refused parameter must not reach the control plane: %d requests", n)
	}

	res, _ = p.run(ctx, "covey", "get_task", json.RawMessage(`{"id":"abcdef01"}`)).(map[string]any)
	if r := f.got()[2]; res["status"] != "ok" || r.Op != "get" || r.ID != "abcdef01" {
		t.Fatalf("get_task: %v / %+v", res, r)
	}
	if ev := waitAudit(t, f, "covey:get_task"); ev["ok"] != true || ev["id"] != "abcdef01" {
		t.Fatalf("get_task audit event: %v", ev)
	}

	refused, _ := p.run(ctx, "covey", "get_task", json.RawMessage(`{"id":"12345678"}`)).(map[string]string)
	if refused["status"] != "error" || !strings.Contains(refused["error"], "12345678") {
		t.Fatalf("a refusal comes back as an error: %v", refused)
	}
	if missing, _ := p.run(ctx, "covey", "get_task", json.RawMessage(`{}`)).(map[string]string); missing["status"] != "error" {
		t.Fatalf("get_task without an id: %v", missing)
	}

	// The second door: the covey MCP tool.
	_, resp := rpc(t, p, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"covey","arguments":{"action":"list_tasks","params":{"scope":"own"}}}}`)
	raw, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(raw), `\"status\":\"ok\"`) || !strings.Contains(string(raw), "Plan") {
		t.Fatalf("list_tasks through MCP: %s", raw)
	}
	all := f.got()
	if last := all[len(all)-1]; last.Op != "list" || last.Scope != "own" {
		t.Fatalf("list_tasks through MCP request: %+v", last)
	}
}
