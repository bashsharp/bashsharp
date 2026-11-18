// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import "testing"

func TestGroupSignalReceiptBroadcastDoesNotConsumeWaitWake(t *testing.T) {
	r := &Runner{sigWake: make(chan struct{}, 1)}
	first := r.groupSignalReceiptChan("TERM")
	second := r.groupSignalReceiptChan("TERM")
	if first == nil || second == nil {
		t.Fatal("missing receipt subscription")
	}
	r.queuePendingSignal("TERM", "")
	// An ordinary wait may consume the single buffered wake. A group kill
	// waiter must still observe its separate broadcast receipt.
	<-r.sigWake
	for _, receipt := range []<-chan struct{}{first, second} {
		select {
		case <-receipt:
		default:
			t.Fatal("group signal receipt was lost to wait")
		}
	}
	// A pending standard signal can coalesce with another delivery; the trap
	// already queued is enough to run before the next statement.
	if receipt := r.groupSignalReceiptChan("TERM"); receipt != nil {
		t.Fatal("joined a second receipt while TERM was already pending")
	}
}
