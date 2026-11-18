package interp

// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
// Sprint: #376; Story: #1507; Story-ID: 0cde6d446a94

import (
	"runtime"
	"testing"
	"time"
)

// A callback-capable request serves the shared-memory mailbox while native
// testing runs callback bodies in independent goroutines. On one processor an
// empty poll must give the processor up at once so those bodies can produce
// the next request or reply; a spinning poll otherwise competes with the work
// it is serving.
func TestS319CallbackMailboxPollYieldsToParallelCallback(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })

	var idle bashPPMailboxIdle
	if idle.spin() {
		t.Fatal("empty mailbox poll spins on one processor instead of parking")
	}
}

// A testing.M callback request stays live while all parallel test callbacks
// execute. An idle owner which keeps polling remains runnable and is scheduled
// once for every callback step. After a short responsive window, idle polling
// must stop and the owner park, so callback work, the control reader, and
// timers own the processors.
func TestS319CallbackMailboxPollParksAfterBoundedSpin(t *testing.T) {
	if runtime.GOMAXPROCS(0) == 1 {
		t.Skip("one processor never spins")
	}
	var idle bashPPMailboxIdle
	start := time.Now()
	for idle.spin() {
		if time.Since(start) > time.Second {
			t.Fatal("empty mailbox polls never park")
		}
	}
	if idle.polls < 2 {
		t.Fatalf("empty mailbox polls parked after %d polls, want a responsive spin first", idle.polls)
	}
}

// A parked owner is counted in the shared header before its last look at the
// slots, so a request published at any point is either seen by that look or
// its publisher sees the count and sends the wake.
func TestS376CallbackMailboxParkSeesPublishedRequest(t *testing.T) {
	mailbox := &bashPPCallbackMailbox{data: make([]byte, bashPPMailboxSize), wake: make(chan struct{}, 1)}
	parked := mailbox.word(0, bashPPMailboxHostParked)
	if !mailbox.park() || *parked != 1 {
		t.Fatalf("empty mailbox did not park: count %d", *parked)
	}
	mailbox.unpark()
	*mailbox.word(3, 0) = bashPPMailboxRequest
	if mailbox.park() || *parked != 0 {
		t.Fatalf("owner parked over a published request: count %d", *parked)
	}
	mailbox.notify()
	mailbox.notify()
	select {
	case <-mailbox.wake:
	default:
		t.Fatal("wake was not delivered")
	}
}

// A streaming producer sets the serve gap, and the next spin budget covers
// twice that gap so the producer is met spinning on hosts where the fixed
// spin would already have expired. Without history the fixed spin applies,
// and a long-ended stream still parks after a bounded burn.
func TestS376CallbackMailboxIdleAdaptsToServeGap(t *testing.T) {
	now := time.Now()
	var idle bashPPMailboxIdle
	idle.reset(now, time.Time{})
	if idle.budget != bashPPMailboxHostSpin {
		t.Fatalf("no history budget %s, want fixed %s", idle.budget, bashPPMailboxHostSpin)
	}
	idle.reset(now, now.Add(-100*time.Microsecond))
	if idle.budget != 200*time.Microsecond {
		t.Fatalf("streaming budget %s, want twice the 100µs gap", idle.budget)
	}
	idle.reset(now, now.Add(-10*time.Millisecond))
	if idle.budget != bashPPMailboxIdleMaxBudget {
		t.Fatalf("idle budget %s, want cap %s", idle.budget, bashPPMailboxIdleMaxBudget)
	}
}
