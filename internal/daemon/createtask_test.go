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

// createTaskPlane stands in for the control plane's half of
// covey/create_task: it approves the guard-rail question, answers the create
// request with a fixed answer and keeps the request and the audit events.
type createTaskPlane struct {
	mu       sync.Mutex
	requests []RequestCreateTask
	events   []map[string]any
	answer   InjectCreateTask
}

func (f *createTaskPlane) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		write := func(typ string, payload any) {
			out, _ := Encode(typ, payload)
			b, _ := json.Marshal(out)
			_ = conn.Write(r.Context(), websocket.MessageText, b)
		}
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
			case TypeRequestApproval:
				req, _ := DecodePayload[RequestApproval](msg)
				write(TypeApprovalDecision, ApprovalDecision{RequestID: req.RequestID, Status: "approved"})
			case TypeRequestCreateTask:
				req, _ := DecodePayload[RequestCreateTask](msg)
				f.mu.Lock()
				f.requests = append(f.requests, req)
				resp := f.answer
				f.mu.Unlock()
				resp.RequestID = req.RequestID
				write(TypeInjectCreateTask, resp)
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

func (f *createTaskPlane) proxy(t *testing.T) *actionProxy {
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
	return &actionProxy{client: c, taskID: "task-1"}
}

func (f *createTaskPlane) audit(t *testing.T) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, e := range f.events {
			if e["action"] == "covey:create_task:foreign" {
				f.mu.Unlock()
				return e
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no audit event for create_task")
	return nil
}

// A create_task routed onto an existing task (#45) answers that task's id
// with routed, its state, whether it woke and what it waits on; the topic
// reaches the control plane, and the audit event names the task it went to.
func TestCreateTaskRoutedAnswer(t *testing.T) {
	f := &createTaskPlane{answer: InjectCreateTask{OK: true, TaskID: "0b5c2f1e-0000-4000-8000-000000000045",
		Agent: "kollegin", Routed: true, State: "blocked", Parked: true, WaitingOn: "parked:feat/cost-insights"}}
	p := f.proxy(t)

	res, _ := p.run(context.Background(), "covey", "create_task", json.RawMessage(
		`{"title":"formula: Cost page headline.","body":"use the weekly figure","agent":"kollegin","topic":"cost page headline"}`)).(map[string]any)
	if res["status"] != "ok" {
		t.Fatalf("create_task: %v", res)
	}
	data, _ := res["data"].(map[string]any)
	want := map[string]any{"task_id": "0b5c2f1e-0000-4000-8000-000000000045", "agent": "kollegin",
		"routed": true, "state": "blocked", "woken": false, "parked": true, "waiting_on": "parked:feat/cost-insights"}
	if len(data) != len(want) {
		t.Fatalf("routed answer: got %v, want %v", data, want)
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("routed answer %s = %v, want %v", k, data[k], v)
		}
	}
	f.mu.Lock()
	got := f.requests[0]
	f.mu.Unlock()
	if got.Topic != "cost page headline" {
		t.Errorf("the topic has to reach the control plane: %+v", got)
	}

	ev := f.audit(t)
	if ev["routed"] != true || ev["routed_task"] != "0b5c2f1e-0000-4000-8000-000000000045" ||
		ev["created_task"] != "0b5c2f1e-0000-4000-8000-000000000045" || ev["topic"] != "cost page headline" {
		t.Errorf("the audit event has to name the routed task: %v", ev)
	}
}

// A task that was created answers as before, and says routed:false.
func TestCreateTaskCreatedAnswer(t *testing.T) {
	f := &createTaskPlane{answer: InjectCreateTask{OK: true, TaskID: "new-task", Agent: "kollegin"}}
	p := f.proxy(t)

	res, _ := p.run(context.Background(), "covey", "create_task",
		json.RawMessage(`{"title":"formula cost page headline","agent":"kollegin"}`)).(map[string]any)
	data, _ := res["data"].(map[string]any)
	if res["status"] != "ok" || len(data) != 3 || data["task_id"] != "new-task" || data["routed"] != false {
		t.Fatalf("created answer: %v", res)
	}
	ev := f.audit(t)
	if _, ok := ev["routed_task"]; ok || ev["routed"] != false {
		t.Errorf("a created task names no routed task: %v", ev)
	}
}
