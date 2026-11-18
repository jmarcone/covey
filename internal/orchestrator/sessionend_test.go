package orchestrator

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

/* What the end of a session answers, without a database. The round trips —
   a task handed over while the sandbox goes down, a press during a failing
   wake, a daemon loss, a kill, a shutdown — are in internal/integration
   (wakesecuring_test.go). */

func runningSession(o *Orchestrator) (uuid.UUID, *session) {
	id := uuid.New()
	s := &session{cancel: func() {}}
	o.sessions[id] = s
	return id, s
}

// A press that finds a session running is held on it, starts no second
// session beside it, and is what the end answers.
func TestAWakePressedDuringASessionIsRemembered(t *testing.T) {
	o := New(Options{})
	id, s := runningSession(o)

	o.WakeNow(id)

	if !s.wakeNow {
		t.Fatal("the press was dropped: the session does not know a person asked")
	}
	if len(o.sessions) != 1 || o.sessions[id] != s {
		t.Fatal("a second session was started beside the running one")
	}
	if got := o.endSession(id, s); got != endWake {
		t.Errorf("the end of the session answers %d, expected the press (%d)", got, endWake)
	}
	if _, still := o.sessions[id]; still {
		t.Error("the ended session is still registered")
	}
}

// Any other wake that finds a session running is not held: a busy session
// that stops in the middle of something leaves its work to the tick.
func TestABusySessionEndsQuietly(t *testing.T) {
	o := New(Options{})
	id, s := runningSession(o)

	o.EnsureRunning(id) // the tick, a NOTIFY, a delegation

	if s.wakeNow {
		t.Error("a wake nobody pressed is held like a press")
	}
	if got := o.endSession(id, s); got != endQuiet {
		t.Errorf("a session that stopped mid-run answers %d, expected nothing", got)
	}
}

// A session whose last look found nothing looks again at its end.
func TestAnIdleSessionLooksAgain(t *testing.T) {
	o := New(Options{})
	id, s := runningSession(o)
	s.idle = true

	if got := o.endSession(id, s); got != endLook {
		t.Errorf("an idle session answers %d at its end, expected a look (%d)", got, endLook)
	}
}

// During a shutdown nothing is answered, a press included: the process is
// waiting for its own work to stop, not for more of it.
func TestNoSuccessorDuringShutdown(t *testing.T) {
	o := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	o.baseCtx = ctx
	id, s := runningSession(o)
	s.idle = true

	o.WakeNow(id)
	cancel()

	if got := o.endSession(id, s); got != endQuiet {
		t.Errorf("a session asks for a successor (%d) while the control plane shuts down", got)
	}
}

// And no session starts at all once the lifecycle is done, whoever asks.
func TestNoSessionStartsDuringShutdown(t *testing.T) {
	o := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	o.baseCtx = ctx
	cancel()

	if _, _, ok := o.register(uuid.New()); ok {
		t.Error("a session was registered after the control plane's lifecycle ended")
	}
	if len(o.sessions) != 0 {
		t.Errorf("%d sessions registered during the shutdown", len(o.sessions))
	}
}
