// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"os"
	"testing"
)

// busSig is a stand-in os.Signal so the bus semantics are testable on hosts
// where syscall.Signal(30) would be a real kernel signal.
type busSig int

func (s busSig) Signal()        {}
func (s busSig) String() string { return "bus signal" }

func recvNow(ch <-chan os.Signal) (os.Signal, bool) {
	select {
	case s := <-ch:
		return s, true
	default:
		return nil, false
	}
}

func TestSignalBusDeliversToSubscribers(t *testing.T) {
	t.Parallel()
	b := newSignalBus()
	a := make(chan os.Signal, 1)
	c := make(chan os.Signal, 1)
	b.subscribe(a, 30, busSig(30))
	b.subscribe(a, 30, busSig(30)) // idempotent
	b.subscribe(c, 30, busSig(30))
	b.subscribe(c, 31, busSig(31))
	if d := b.raise(30); d != busDelivered {
		t.Fatalf("raise(30) = %v, want delivered", d)
	}
	for name, ch := range map[string]<-chan os.Signal{"a": a, "c": c} {
		if s, ok := recvNow(ch); !ok || s != busSig(30) {
			t.Errorf("%s got %v, %v; want busSig(30)", name, s, ok)
		}
		if _, ok := recvNow(ch); ok {
			t.Errorf("%s received a second delivery", name)
		}
	}
	if d := b.raise(31); d != busDelivered {
		t.Fatalf("raise(31) = %v", d)
	}
	if s, ok := recvNow(c); !ok || s != busSig(31) {
		t.Errorf("c got %v, %v; want busSig(31)", s, ok)
	}
	if _, ok := recvNow(a); ok {
		t.Error("a received a signal it did not subscribe to")
	}
}

func TestSignalBusNonBlockingSend(t *testing.T) {
	t.Parallel()
	b := newSignalBus()
	full := make(chan os.Signal, 1)
	full <- busSig(1)
	b.subscribe(full, 15, busSig(15))
	done := make(chan busDisposition, 1)
	go func() { done <- b.raise(15) }()
	if d := <-done; d != busDelivered {
		t.Fatalf("raise on a full channel = %v", d)
	}
	if s, _ := recvNow(full); s != busSig(1) {
		t.Errorf("full channel was overwritten: %v", s)
	}
}

func TestSignalBusIgnoreResetUnsubscribe(t *testing.T) {
	t.Parallel()
	b := newSignalBus()
	defaults := 0
	b.setDefault(func(int) { defaults++ })
	ch := make(chan os.Signal, 1)
	b.subscribe(ch, 31, busSig(31))

	// ignore drops the subscription and swallows raises without the default.
	b.ignore(31)
	if !b.isIgnored(31) {
		t.Fatal("31 not ignored")
	}
	if d := b.raise(31); d != busIgnored {
		t.Errorf("raise while ignored = %v", d)
	}
	if _, ok := recvNow(ch); ok || defaults != 0 {
		t.Errorf("ignored raise leaked: delivered=%v defaults=%d", ok, defaults)
	}

	// reset clears the ignore; with nobody subscribed the default runs.
	b.reset(31)
	if b.isIgnored(31) {
		t.Fatal("31 still ignored after reset")
	}
	if d := b.raise(31); d != busDefaulted || defaults != 1 {
		t.Errorf("raise after reset = %v, defaults=%d", d, defaults)
	}

	// re-subscribe, then unsubscribe (signal.Stop): default again.
	b.subscribe(ch, 31, busSig(31))
	b.subscribe(ch, 15, busSig(15))
	b.unsubscribe(ch)
	if d := b.raise(31); d != busDefaulted || defaults != 2 {
		t.Errorf("raise after unsubscribe = %v, defaults=%d", d, defaults)
	}
	if d := b.raise(15); d != busDefaulted || defaults != 3 {
		t.Errorf("raise(15) after unsubscribe = %v, defaults=%d", d, defaults)
	}
	if _, ok := recvNow(ch); ok {
		t.Error("unsubscribed channel received a signal")
	}
}

func TestSignalBusDefaultAction(t *testing.T) {
	t.Parallel()
	b := newSignalBus()
	// An embedding host installs nothing: an unhandled raise is a no-op.
	if d := b.raise(15); d != busDefaulted {
		t.Errorf("raise with no default = %v", d)
	}
	if b.runDefault(15) {
		t.Error("runDefault reported an action with none installed")
	}
	var got []int
	b.setDefault(func(num int) { got = append(got, num) })
	b.raise(9)
	if !b.runDefault(15) {
		t.Error("runDefault reported no action")
	}
	// runDefault bypasses subscriptions and ignores: it is the relay for a
	// native-default delivery that already decided on the default action.
	ch := make(chan os.Signal, 1)
	b.subscribe(ch, 30, busSig(30))
	b.ignore(31)
	b.runDefault(30)
	b.runDefault(31)
	if want := []int{9, 15, 30, 31}; len(got) != len(want) || got[0] != 9 || got[1] != 15 || got[2] != 30 || got[3] != 31 {
		t.Errorf("default actions = %v, want %v", got, want)
	}
	if _, ok := recvNow(ch); ok {
		t.Error("runDefault delivered to a subscriber")
	}
}

// RaiseProcessSignal and the bus* seams are inert off Windows; on Windows the
// seams mirror os/signal. Both are exercised through the runner on a Windows
// worker (signal_windows_test.go); here only the guard is checked.
func TestRaiseProcessSignalGuard(t *testing.T) {
	t.Parallel()
	ch := make(chan os.Signal, 1)
	processSignalBus.subscribe(ch, 63, busSig(63))
	defer processSignalBus.unsubscribe(ch)
	RaiseProcessSignal(63)
	if _, ok := recvNow(ch); ok != signalBusActive {
		t.Errorf("RaiseProcessSignal delivered=%v, want %v on this platform", ok, signalBusActive)
	}
}
