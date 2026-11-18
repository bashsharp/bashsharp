//go:build full

package interp

// Sprint: #376; Story: #1549; Story-ID: 91561dbe8537
//
// Narrow regressions for resident RWMutex writer preference: a blocked Lock
// excludes new RLock/TryRLock callers, a cancelled writer withdraws and wakes
// the readers it excluded, queued writers keep excluding readers until the
// last one is done, and a failed TryLock claims nothing. Every step waits on
// the registration it depends on, so no outcome depends on scheduling.

import (
	"context"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type s376RWMutex struct {
	t *testing.T
	s *goSourceResidentSync
}

func (m s376RWMutex) call(ctx context.Context, selector string) ([]bashPPBridgeValue, error) {
	r, err := New(Lang(syntax.LangBashPP), Dir(m.t.TempDir()))
	if err != nil {
		return nil, err
	}
	values, _, err := r.goSourceResidentSyncRequest(ctx, bashPPBridgeRequest{Op: "call", Selector: selector, Receiver: &bashPPBridgeValue{Kind: "handle", Type: "sync.RWMutex", residentSync: m.s}})
	return values, err
}

func (m s376RWMutex) must(selector string) {
	m.t.Helper()
	if _, err := m.call(context.Background(), selector); err != nil {
		m.t.Fatalf("%s: %v", selector, err)
	}
}

func (m s376RWMutex) try(selector string) bool {
	m.t.Helper()
	values, err := m.call(context.Background(), selector)
	if err != nil || len(values) != 1 {
		m.t.Fatalf("%s: %v %v", selector, values, err)
	}
	return values[0].Text == "true"
}

// start runs a blocking operation and returns the channel carrying its result.
func (m s376RWMutex) start(ctx context.Context, selector string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := m.call(ctx, selector)
		done <- err
	}()
	return done
}

// settle waits until the bookkeeping reaches the wanted state.
func (m s376RWMutex) settle(what string, ok func(*goSourceResidentSync) bool) {
	m.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.s.gate.Lock()
		reached := ok(m.s)
		m.s.gate.Unlock()
		if reached {
			return
		}
		if time.Now().After(deadline) {
			m.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (m s376RWMutex) idle() {
	m.t.Helper()
	m.s.gate.Lock()
	defer m.s.gate.Unlock()
	if s := m.s; s.wlocked || s.readers != 0 || s.wpending != 0 || s.rwaiting != 0 || s.rgranted != 0 {
		m.t.Fatalf("residual state wlocked=%v readers=%d wpending=%d rwaiting=%d rgranted=%d", s.wlocked, s.readers, s.wpending, s.rwaiting, s.rgranted)
	}
}

func s376Wait(t *testing.T, what string, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if err != want {
			t.Fatalf("%s: error %v, want %v", what, err, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

func s376Blocked(t *testing.T, what string, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s returned early: %v", what, err)
	default:
	}
}

func TestS376ResidentRWMutexPendingWriterExcludesReaders(t *testing.T) {
	m := s376RWMutex{t, new(goSourceResidentSync)}
	m.must("RLock")
	writer := m.start(context.Background(), "Lock")
	m.settle("pending writer", func(s *goSourceResidentSync) bool { return s.wpending == 1 })

	// The original defect: both of these entered past the blocked writer.
	if m.try("TryRLock") {
		t.Fatal("TryRLock acquired past a pending writer")
	}
	reader := m.start(context.Background(), "RLock")
	m.settle("waiting reader", func(s *goSourceResidentSync) bool { return s.rwaiting == 1 })
	s376Blocked(t, "RLock behind a pending writer", reader)
	s376Blocked(t, "Lock behind a reader", writer)

	m.must("RUnlock")
	s376Wait(t, "pending Lock", writer, nil)
	s376Blocked(t, "RLock behind the writer", reader)
	m.must("Unlock")
	s376Wait(t, "RLock after Unlock", reader, nil)
	m.must("RUnlock")
	m.idle()
}

func TestS376ResidentRWMutexCancelledWriterWakesReaders(t *testing.T) {
	m := s376RWMutex{t, new(goSourceResidentSync)}
	m.must("RLock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := m.start(ctx, "Lock")
	m.settle("pending writer", func(s *goSourceResidentSync) bool { return s.wpending == 1 })
	reader := m.start(context.Background(), "RLock")
	m.settle("waiting reader", func(s *goSourceResidentSync) bool { return s.rwaiting == 1 })
	s376Blocked(t, "RLock behind a pending writer", reader)

	cancel()
	s376Wait(t, "cancelled Lock", writer, context.Canceled)
	// The first reader still holds the lock: only the writer's withdrawal
	// can have woken this one.
	s376Wait(t, "RLock after the writer withdrew", reader, nil)
	if !m.try("TryRLock") {
		t.Fatal("TryRLock refused after the pending writer was cancelled")
	}
	m.must("RUnlock")
	m.must("RUnlock")
	m.must("RUnlock")
	if !m.try("TryLock") {
		t.Fatal("TryLock refused on an idle RWMutex")
	}
	m.must("Unlock")
	m.idle()
}

func TestS376ResidentRWMutexCancelledReaderLeavesNoClaim(t *testing.T) {
	m := s376RWMutex{t, new(goSourceResidentSync)}
	m.must("RLock")
	writer := m.start(context.Background(), "Lock")
	m.settle("pending writer", func(s *goSourceResidentSync) bool { return s.wpending == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := m.start(ctx, "RLock")
	m.settle("waiting reader", func(s *goSourceResidentSync) bool { return s.rwaiting == 1 })
	cancel()
	s376Wait(t, "cancelled RLock", reader, context.Canceled)
	m.settle("reader withdrawal", func(s *goSourceResidentSync) bool { return s.rwaiting == 0 })

	m.must("RUnlock")
	s376Wait(t, "pending Lock", writer, nil)
	m.must("Unlock")
	// A grant left behind for the cancelled reader would refuse this.
	if !m.try("TryLock") {
		t.Fatal("TryLock refused after a cancelled reader")
	}
	m.must("Unlock")
	m.idle()
}

func TestS376ResidentRWMutexQueuedWriters(t *testing.T) {
	m := s376RWMutex{t, new(goSourceResidentSync)}
	m.must("RLock")
	first := m.start(context.Background(), "Lock")
	second := m.start(context.Background(), "Lock")
	m.settle("two pending writers", func(s *goSourceResidentSync) bool { return s.wpending == 2 })
	if m.try("TryRLock") {
		t.Fatal("TryRLock acquired past two pending writers")
	}

	m.must("RUnlock")
	m.settle("first writer in", func(s *goSourceResidentSync) bool { return s.wlocked && s.wpending == 1 })
	reader := m.start(context.Background(), "RLock")
	m.settle("waiting reader", func(s *goSourceResidentSync) bool { return s.rwaiting == 1 })

	// As in Go, the reader blocked during the first write section is admitted
	// at Unlock ahead of the remaining writer, which still excludes new ones.
	m.must("Unlock")
	s376Wait(t, "RLock handed off at Unlock", reader, nil)
	if m.try("TryRLock") {
		t.Fatal("TryRLock acquired past the remaining pending writer")
	}
	m.settle("second writer still queued", func(s *goSourceResidentSync) bool { return !s.wlocked && s.wpending == 1 })
	m.must("RUnlock")
	m.settle("second writer in", func(s *goSourceResidentSync) bool { return s.wlocked && s.wpending == 0 })
	m.must("Unlock")

	// The two writers finish in either order; both must have acquired.
	s376Wait(t, "first Lock", first, nil)
	s376Wait(t, "second Lock", second, nil)
	if !m.try("TryRLock") {
		t.Fatal("TryRLock refused after every writer finished")
	}
	m.must("RUnlock")
	m.idle()
}

func TestS376ResidentRWMutexFailedTryLockClaimsNothing(t *testing.T) {
	m := s376RWMutex{t, new(goSourceResidentSync)}
	m.must("RLock")
	if m.try("TryLock") {
		t.Fatal("TryLock acquired a read-locked RWMutex")
	}
	if !m.try("TryRLock") {
		t.Fatal("failed TryLock excluded a reader")
	}
	reader := m.start(context.Background(), "RLock")
	s376Wait(t, "RLock after a failed TryLock", reader, nil)
	m.must("RUnlock")
	m.must("RUnlock")
	m.must("RUnlock")
	m.idle()
}
