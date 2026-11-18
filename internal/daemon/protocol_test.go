package daemon

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// responseTypes are the message types with which the control plane answers a
// REQUEST from the daemon. Each of them must find its waiting caller; if one is
// missing from the routing, the answer never arrives: the call runs into its
// timeout, and because some requests sit in the critical path before the run,
// the whole task stalls afterwards.
//
// That is exactly what happened when inject_skills was added — the route was
// missing, and every integration test ran into a 15-second timeout.
var responseTypes = []string{
	TypeInjectCredentials,
	TypeApprovalDecision,
	TypeInjectTarget,
	TypeInjectOrgChart,
	TypeInjectWiki,
	TypeInjectSkills,
	TypeInjectCreateTask,
	TypeInjectHiring,
	TypeInjectSecret,
	TypeInjectTool,
	TypeInjectPlatformAction,
	TypeInjectMessage,
	TypeInjectTasks,
}

// TestResponsesReachTheirCaller checks delivery by behavior instead of by
// comparing two lists: for every response type a real channel waits, and the
// message must arrive there. If the type is missing from the routing, the test
// fails — the same effect as in production, only in milliseconds.
func TestResponsesReachTheirCaller(t *testing.T) {
	c := &Client{pending: map[string]chan Message{}}

	for _, typ := range responseTypes {
		ch := make(chan Message, 1)
		c.pending["req-1"] = ch
		msg := Message{Type: typ, Payload: json.RawMessage(`{"request_id":"req-1"}`)}

		if !c.deliverIfResponse(msg) {
			t.Errorf("%s is not recognized as a response — the caller runs into its timeout", typ)
			continue
		}
		select {
		case got := <-ch:
			if got.Type != typ {
				t.Errorf("%s: wrong message delivered (%s)", typ, got.Type)
			}
		default:
			t.Errorf("%s: nothing arrived on the caller's channel", typ)
		}
	}
}

// A proactive push carries no request_id — it belongs in the read loop's switch
// and must not be swallowed as a response. inject_credentials occurs in both
// forms and is therefore the interesting case.
func TestPushIsNotTreatedAsResponse(t *testing.T) {
	c := &Client{pending: map[string]chan Message{}}
	push := Message{Type: TypeInjectCredentials, Payload: json.RawMessage(`{"system":"gitlab"}`)}
	if c.deliverIfResponse(push) {
		t.Fatal("a push without a request_id must not be routed as a response — otherwise it vanishes")
	}
}

// The list above is maintained by hand; this check catches the case that
// someone extends routedInjectTypes without pulling the behavior test along.
func TestRoutingTableMatchesTestedTypes(t *testing.T) {
	if len(routedInjectTypes) != len(responseTypes) {
		t.Fatalf("routedInjectTypes has %d entries, %d are checked — add the new response type to responseTypes",
			len(routedInjectTypes), len(responseTypes))
	}
	for _, typ := range responseTypes {
		if !routedInjectTypes[typ] {
			t.Errorf("%s is missing from routedInjectTypes", typ)
		}
	}
}

// TestEveryInjectTypeIsRouted closes the hole the two lists above leave open: a
// response type missing from BOTH of them used to stay undetected, and that is
// not a hypothetical — it happened to inject_skills, and then again to
// inject_hiring, where every hiring action of an agent ran into its timeout
// while the run looked healthy from the outside.
//
// So the expectation is not maintained by hand any more but read off the
// protocol itself: every constant whose VALUE begins with "inject_" is an
// answer from the control plane and has to find its caller. Whoever adds one
// and forgets the routing gets a failing unit test instead of a thirty-second
// silence inside a sandbox.
func TestEveryInjectTypeIsRouted(t *testing.T) {
	// Pushes: the control plane sends these unprompted, so there is no caller
	// waiting and they belong in the read loop's switch. Named individually and
	// with a reason — an exemption list one can extend thoughtlessly would give
	// back exactly the hole this test closes.
	pushOnly := map[string]string{
		"inject_config": "sent once at the start of a run, nobody requested it",
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "protocol.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value := strings.Trim(lit.Value, `"`)
		if !strings.HasPrefix(value, "inject_") {
			return true
		}
		found++
		if _, exempt := pushOnly[value]; exempt {
			return true
		}
		if !routedInjectTypes[value] {
			t.Errorf("%s (%s) is an answer from the control plane and is missing from routedInjectTypes — "+
				"its caller would run into its timeout", spec.Names[0].Name, value)
		}
		return true
	})
	if found == 0 {
		t.Fatal("no inject_ type found — the parse is not doing what it claims")
	}
}

// The cost message says what its figure covers. A new daemon sends the basis
// and the session; a daemon from before the fields sends neither, and its
// message must decode to an empty basis — that emptiness is how the control
// plane recognizes a session-cumulative claude-code figure.
func TestCostCarriesBasisAndSession(t *testing.T) {
	in := Cost{TaskID: "t", USD: 0.3, InputTokens: 2, OutputTokens: 300,
		CacheReadTokens: 52000, CacheCreationTokens: 900, Model: "claude-opus-5-5",
		Basis: CostBasisInvocation, SessionID: "sess-1", SessionTotalUSD: 1.3}
	msg, err := Encode(TypeCost, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"basis":"invocation"`, `"session_id":"sess-1"`, `"session_total_usd":1.3`} {
		if !strings.Contains(string(msg.Payload), want) {
			t.Fatalf("%s missing from %s", want, msg.Payload)
		}
	}
	out, err := DecodePayload[Cost](msg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip lost fields:\n in %+v\nout %+v", in, out)
	}

	old := Message{Type: TypeCost, Payload: json.RawMessage(
		`{"task_id":"t","usd":2.3,"input_tokens":5,"output_tokens":900,"cache_read_tokens":40000,"model":"claude-opus-5-5"}`)}
	got, err := DecodePayload[Cost](old)
	if err != nil {
		t.Fatal(err)
	}
	if got.Basis != "" || got.SessionID != "" || got.SessionTotalUSD != 0 || got.USD != 2.3 {
		t.Fatalf("an old daemon's message must decode with an empty basis: %+v", got)
	}
}
