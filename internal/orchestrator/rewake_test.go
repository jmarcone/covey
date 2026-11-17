package orchestrator

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// A wake that finds a session running is remembered on it, not dropped — and
// does not start a second one beside it. The whole round trip, a task handed
// over while the sandbox goes down, is in internal/integration
// (wakesecuring_test.go); this is the part that needs no database.
func TestAWakeDuringASessionIsRemembered(t *testing.T) {
	o := New(Options{})
	id := uuid.New()
	s := &session{cancel: func() {}}
	o.sessions[id] = s

	o.EnsureRunning(id)

	if !s.rewake {
		t.Fatal("the wake was dropped: the session does not know it was asked for")
	}
	if len(o.sessions) != 1 || o.sessions[id] != s {
		t.Fatal("a second session was started beside the running one")
	}
	if !o.endSession(id, s) {
		t.Error("the end of the session does not answer the wake")
	}
	if _, still := o.sessions[id]; still {
		t.Error("the ended session is still registered")
	}
}

// Nothing asked, nothing started: the end of an ordinary session stays quiet.
func TestASessionNobodyAskedForEndsQuietly(t *testing.T) {
	o := New(Options{})
	id := uuid.New()
	s := &session{cancel: func() {}}
	o.sessions[id] = s

	if o.endSession(id, s) {
		t.Error("a session nobody asked for during its run wants a successor")
	}
}

// During a shutdown the wish stands, but nothing answers it: the process is
// waiting for its own work to stop, not for more of it.
func TestNoSuccessorDuringShutdown(t *testing.T) {
	o := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	o.baseCtx = ctx
	id := uuid.New()
	s := &session{cancel: func() {}}
	o.sessions[id] = s

	o.EnsureRunning(id)
	cancel()

	if o.endSession(id, s) {
		t.Error("a session asks for a successor while the control plane shuts down")
	}
}
