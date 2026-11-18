// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"os"
	"sync"
)

// signalBus is an in-process stand-in for kernel signal delivery, with
// os/signal semantics: a subscribed channel receives a non-blocking send for
// every raise of its signal; an ignored signal is dropped; a signal nobody
// subscribes to takes the process default action. It exists for Windows,
// where the Go runtime cannot deliver a syscall.Signal(n) at all — os/signal
// there is pure bookkeeping — yet a shell still needs `kill -USR1 $$` from a
// foreground subshell or a background goroutine job, and a signal from a
// sibling bashy process (signalserver_windows.go), to run the trap. The bus
// mirrors the shell's os/signal calls (Notify/Stop/Ignore/Reset; see the
// bus* seams in signalbus_windows.go), so a raise flows through the existing
// subscription workers into the pending-signal queue unchanged.
//
// The type is platform-neutral so its semantics are proven on any host; only
// the wiring into signal.go is Windows-specific.
type signalBus struct {
	mu sync.Mutex
	// subs maps a signal number to its subscribed channels and the signal
	// value each was registered with (the value os/signal would send).
	subs    map[int][]busSubscriber
	ignored map[int]bool
	// defaultAction is the process default for an unhandled raise, set by a
	// standalone host to exit with the signal marker. nil (an embedding
	// host) makes an unhandled raise a no-op: a script must not be able to
	// kill the process hosting it.
	defaultAction func(num int)
}

type busSubscriber struct {
	ch  chan<- os.Signal
	sig os.Signal
}

func newSignalBus() *signalBus {
	return &signalBus{subs: make(map[int][]busSubscriber), ignored: make(map[int]bool)}
}

// subscribe registers ch for signal num; sig is the value delivered on it.
// Like signal.Notify, a channel may subscribe to several signals and
// subscribing twice to the same signal is idempotent.
func (b *signalBus) subscribe(ch chan<- os.Signal, num int, sig os.Signal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs[num] {
		if s.ch == ch {
			return
		}
	}
	b.subs[num] = append(b.subs[num], busSubscriber{ch: ch, sig: sig})
}

// unsubscribe removes ch from every signal, like signal.Stop.
func (b *signalBus) unsubscribe(ch chan<- os.Signal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for num, subs := range b.subs {
		b.subs[num] = dropSubscriber(subs, ch)
	}
}

// ignore marks num ignored and, like signal.Ignore, drops every subscription
// for it.
func (b *signalBus) ignore(num int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ignored[num] = true
	delete(b.subs, num)
}

// reset restores num's default: clears the ignore and drops subscriptions,
// like signal.Reset.
func (b *signalBus) reset(num int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.ignored, num)
	delete(b.subs, num)
}

// isIgnored reports whether num is currently ignored on the bus.
func (b *signalBus) isIgnored(num int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ignored[num]
}

// setDefault installs the process default action for unhandled raises.
func (b *signalBus) setDefault(fn func(num int)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.defaultAction = fn
}

// raise delivers signal num. It reports how the signal was disposed of:
// ignored, delivered to at least one subscriber, or handed to the default
// action (whether or not one is installed). A subscriber whose channel is
// full misses the delivery, exactly as with os/signal.
func (b *signalBus) raise(num int) busDisposition {
	b.mu.Lock()
	if b.ignored[num] {
		b.mu.Unlock()
		return busIgnored
	}
	subs := append([]busSubscriber(nil), b.subs[num]...)
	def := b.defaultAction
	b.mu.Unlock()
	if len(subs) == 0 {
		if def != nil {
			def(num)
		}
		return busDefaulted
	}
	for _, s := range subs {
		select {
		case s.ch <- s.sig:
		default:
		}
	}
	return busDelivered
}

// runDefault applies the process default action for num, bypassing
// subscriptions and ignores. It reports whether an action was installed.
func (b *signalBus) runDefault(num int) bool {
	b.mu.Lock()
	def := b.defaultAction
	b.mu.Unlock()
	if def == nil {
		return false
	}
	def(num)
	return true
}

type busDisposition int

const (
	busIgnored busDisposition = iota
	busDelivered
	busDefaulted
)

func dropSubscriber(subs []busSubscriber, ch chan<- os.Signal) []busSubscriber {
	out := subs[:0]
	for _, s := range subs {
		if s.ch != ch {
			out = append(out, s)
		}
	}
	return out
}

// processSignalBus is the process-wide bus. It is only raised on Windows;
// elsewhere the kernel delivers signals and the bus stays idle.
var processSignalBus = newSignalBus()

// SetProcessSignalDefault installs the action a standalone shell process takes
// when a signal raised through the in-process bus — a self-directed `kill`,
// or one from a sibling bashy over the signal pipe — has neither a trap nor
// an ignore. The bashy CLI on Windows sets it to exit with
// [SignalMarkerExitCode] so a parent bashy reports 128+num; an embedding host
// leaves it unset and an unhandled raise is a no-op. On other platforms the
// bus never fires and this is inert.
func SetProcessSignalDefault(fn func(num int)) {
	processSignalBus.setDefault(fn)
}

// RaiseProcessSignal delivers signal num to this process through the
// in-process bus, as a sibling bashy's `kill` would. It is what the Windows
// signal server calls for each pipe message; a host may also use it to
// inject a signal it received by some other channel (a console control
// event, for example). No-op off Windows.
func RaiseProcessSignal(num int) {
	if !signalBusActive {
		return
	}
	processSignalBus.raise(num)
}
