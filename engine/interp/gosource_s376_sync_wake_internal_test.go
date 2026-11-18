//go:build full

package interp

// Sprint: #376; Story: #1549; Story-ID: 91561dbe8537
//
// Narrow internal tests for resident-sync wakeups: a release wakes one
// exclusive waiter rather than all of them, an abandoned wakeup is handed on,
// and a writer's Unlock admits the readers blocked behind it before the next
// writer.

import (
	"context"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s376SyncCall(t *testing.T, ctx context.Context, s *goSourceResidentSync, typ, selector string) error {
	t.Helper()
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()))
	if err != nil {
		return err
	}
	_, _, err = r.goSourceResidentSyncRequest(ctx, bashPPBridgeRequest{Op: "call", Selector: selector, Receiver: &bashPPBridgeValue{Kind: "handle", Type: typ, residentSync: s}})
	return err
}

func s376SyncWait(t *testing.T, s *goSourceResidentSync, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.gate.Lock()
		ok := ready()
		s.gate.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestS376ResidentMutexWakesOneWaiter(t *testing.T) {
	const waiters = 8
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s := new(goSourceResidentSync)
	if err := s376SyncCall(t, ctx, s, "sync.Mutex", "Lock"); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() { acquired <- s376SyncCall(t, ctx, s, "sync.Mutex", "Lock") }()
	}
	s376SyncWait(t, s, "queued lockers", func() bool { return len(s.queue) == waiters })
	for left := waiters; left > 0; left-- {
		if err := s376SyncCall(t, ctx, s, "sync.Mutex", "Unlock"); err != nil {
			t.Fatal(err)
		}
		if err := <-acquired; err != nil {
			t.Fatal(err)
		}
		// One release woke one locker; the rest never left the queue.
		s.gate.Lock()
		queued, locked := len(s.queue), s.locked
		s.gate.Unlock()
		if queued != left-1 || !locked {
			t.Fatalf("after release: %d queued, locked=%v; want %d queued, locked", queued, locked, left-1)
		}
		select {
		case err := <-acquired:
			t.Fatalf("second locker acquired a held mutex: %v", err)
		default:
		}
	}
}

func TestS376ResidentSyncAbandonHandsWakeOn(t *testing.T) {
	s := new(goSourceResidentSync)
	first, second, third := s.enqueue(nil), s.enqueue(nil), s.enqueue(nil)
	if again := s.enqueue(first); again != first || len(s.queue) != 3 {
		t.Fatalf("a queued waiter was registered twice: %d queued", len(s.queue))
	}
	// A waiter that leaves while still queued takes no wakeup with it.
	s.abandon(second)
	if len(s.queue) != 2 {
		t.Fatalf("%d queued after withdrawal, want 2", len(s.queue))
	}
	s.wakeOne()
	select {
	case <-first:
	default:
		t.Fatal("the longest-queued waiter was not woken")
	}
	select {
	case <-third:
		t.Fatal("a release woke two waiters")
	default:
	}
	// The woken waiter gives up instead of retrying: the next one must run.
	s.abandon(first)
	select {
	case <-third:
	default:
		t.Fatal("an abandoned wakeup was lost")
	}
	if len(s.queue) != 0 {
		t.Fatalf("%d queued, want 0", len(s.queue))
	}
}

func TestS376ResidentMutexCancelledWaiterLeavesQueue(t *testing.T) {
	s := new(goSourceResidentSync)
	if err := s376SyncCall(t, context.Background(), s, "sync.Mutex", "Lock"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s376SyncCall(t, ctx, s, "sync.Mutex", "Lock"); err != context.DeadlineExceeded {
		t.Fatalf("error %v, want deadline", err)
	}
	if len(s.queue) != 0 {
		t.Fatalf("cancelled locker left %d queued", len(s.queue))
	}
}

func TestS376ResidentRWMutexUnlockAdmitsReadersThenWriter(t *testing.T) {
	const readers = 3
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s := new(goSourceResidentSync)
	if err := s376SyncCall(t, ctx, s, "sync.RWMutex", "Lock"); err != nil {
		t.Fatal(err)
	}
	read, wrote := make(chan error, readers), make(chan error, 2)
	for i := 0; i < readers; i++ {
		go func() { read <- s376SyncCall(t, ctx, s, "sync.RWMutex", "RLock") }()
	}
	for i := 0; i < 2; i++ {
		go func() { wrote <- s376SyncCall(t, ctx, s, "sync.RWMutex", "Lock") }()
	}
	s376SyncWait(t, s, "blocked readers and writers", func() bool {
		return s.rwaiting == readers && s.wpending == 2 && len(s.queue) == 2
	})
	if err := s376SyncCall(t, ctx, s, "sync.RWMutex", "Unlock"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < readers; i++ {
		if err := <-read; err != nil {
			t.Fatal(err)
		}
	}
	// The admitted readers hold the lock: no writer was woken to spin on it.
	s.gate.Lock()
	held, queued, wlocked := s.readers, len(s.queue), s.wlocked
	s.gate.Unlock()
	if held != readers || queued != 2 || wlocked {
		t.Fatalf("readers=%d queued=%d wlocked=%v; want %d readers, 2 queued writers", held, queued, wlocked, readers)
	}
	for i := 0; i < readers; i++ {
		if err := s376SyncCall(t, ctx, s, "sync.RWMutex", "RUnlock"); err != nil {
			t.Fatal(err)
		}
	}
	// The last RUnlock wakes exactly one writer; its Unlock wakes the other.
	for left := 2; left > 0; left-- {
		if err := <-wrote; err != nil {
			t.Fatal(err)
		}
		s.gate.Lock()
		queued, wlocked = len(s.queue), s.wlocked
		s.gate.Unlock()
		if queued != left-1 || !wlocked {
			t.Fatalf("queued=%d wlocked=%v; want %d queued, write-locked", queued, wlocked, left-1)
		}
		if err := s376SyncCall(t, ctx, s, "sync.RWMutex", "Unlock"); err != nil {
			t.Fatal(err)
		}
	}
}
