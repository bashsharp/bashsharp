package interp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Sprint: #376; Story: #1550; Story-ID: cae490ea17e7

// gatedConn records every write and can hold one in progress.
type gatedConn struct {
	net.Conn
	mu     sync.Mutex
	writes [][]byte
	gate   chan struct{}
	inside chan struct{}
	err    error
}

func (c *gatedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, bytes.Clone(p))
	gate, inside, err := c.gate, c.inside, c.err
	c.gate, c.inside = nil, nil
	c.mu.Unlock()
	if inside != nil {
		close(inside)
	}
	if gate != nil {
		<-gate
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func groupWriteMessages(t *testing.T, writes [][]byte) [][]uint64 {
	t.Helper()
	var batches [][]uint64
	for _, write := range writes {
		var ids []uint64
		decoder := json.NewDecoder(bytes.NewReader(write))
		for decoder.More() {
			var q bashPPBridgeRequest
			if err := decoder.Decode(&q); err != nil {
				t.Fatalf("write is not whole messages: %v in %q", err, write)
			}
			ids = append(ids, q.ID)
		}
		batches = append(batches, ids)
	}
	return batches
}

// Senders that arrive while a write is in progress share the next write, in
// the order their messages were prepared, and each learns that write's result.
func TestS376GroupWriteBatchesBehindAWrite(t *testing.T) {
	conn := &gatedConn{gate: make(chan struct{}), inside: make(chan struct{})}
	gate, inside := conn.gate, conn.inside
	s := &bashPPNativeSession{conn: conn}
	const followers = 32
	errs := make(chan error, followers+1)
	go func() { errs <- s.sendMessage(func() any { return bashPPBridgeRequest{ID: 1, Op: "call"} }) }()
	<-inside
	var next uint64 = 1
	for range followers {
		go func() {
			errs <- s.sendMessage(func() any {
				// prepare runs under the queue lock, so this is wire order.
				next++
				return bashPPBridgeRequest{ID: next, Op: "call"}
			})
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.outbox.mu.Lock()
		queued := 0
		if s.outbox.batch != nil {
			queued = bytes.Count(s.outbox.batch.buf.Bytes(), []byte("\n"))
		}
		s.outbox.mu.Unlock()
		if queued == followers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d senders queued behind the write", queued, followers)
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	for range followers + 1 {
		if err := <-errs; err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	batches := groupWriteMessages(t, conn.writes)
	if len(batches) != 2 || len(batches[0]) != 1 || len(batches[1]) != followers {
		t.Fatalf("writes carried %v, want one message and then %d together", batches, followers)
	}
	for i, id := range batches[1] {
		if id != uint64(i+2) {
			t.Fatalf("second write order %v", batches[1])
		}
	}
	s.outbox.mu.Lock()
	idle := !s.outbox.flushing && s.outbox.batch == nil
	s.outbox.mu.Unlock()
	if !idle {
		t.Fatal("outbox kept a writer or a batch after every send returned")
	}
}

// A sender alone makes one write per message, and a failed write is reported
// to every sender whose message it carried and to no later one.
func TestS376GroupWriteReportsEachWrite(t *testing.T) {
	conn := &gatedConn{}
	s := &bashPPNativeSession{conn: conn}
	for id := uint64(1); id <= 3; id++ {
		if err := s.sendMessage(func() any { return bashPPBridgeRequest{ID: id, Op: "call"} }); err != nil {
			t.Fatal(err)
		}
	}
	if batches := groupWriteMessages(t, conn.writes); len(batches) != 3 {
		t.Fatalf("a lone sender's three messages took writes %v", batches)
	}

	failure := errors.New("write failed")
	conn.mu.Lock()
	conn.gate, conn.inside = make(chan struct{}), make(chan struct{})
	gate, inside := conn.gate, conn.inside
	conn.mu.Unlock()
	first := make(chan error, 1)
	go func() { first <- s.sendMessage(func() any { return bashPPBridgeRequest{ID: 4, Op: "call"} }) }()
	<-inside
	conn.mu.Lock()
	conn.err = failure
	conn.mu.Unlock()
	second := make(chan error, 2)
	for id := uint64(5); id <= 6; id++ {
		go func() { second <- s.sendMessage(func() any { return bashPPBridgeRequest{ID: id, Op: "call"} }) }()
	}
	for {
		s.outbox.mu.Lock()
		queued := s.outbox.batch != nil && bytes.Count(s.outbox.batch.buf.Bytes(), []byte("\n")) == 2
		s.outbox.mu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Fatalf("the write that succeeded reported %v", err)
	}
	for range 2 {
		if err := <-second; !errors.Is(err, failure) {
			t.Fatalf("a sender in the failed write got %v", err)
		}
	}
	conn.mu.Lock()
	conn.err = nil
	conn.mu.Unlock()
	if err := s.sendMessage(func() any { return bashPPBridgeRequest{ID: 7, Op: "call"} }); err != nil {
		t.Fatalf("a later write inherited the failure: %v", err)
	}
	if err := (&bashPPNativeSession{}).sendMessage(func() any { return bashPPBridgeRequest{ID: 1} }); err == nil {
		t.Fatal("a send with no connection succeeded")
	}
}

// Many senders at once: every message is written exactly once as a whole
// line, each sender's own messages keep their order, and the writer role is
// handed on rather than kept or lost when one sender has written its share.
func TestS376GroupWriteConcurrentSenders(t *testing.T) {
	conn := &gatedConn{}
	s := &bashPPNativeSession{conn: conn}
	const senders, each = 48, 200
	var wg sync.WaitGroup
	for sender := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range each {
				id := uint64(sender*each + n + 1)
				if err := s.sendMessage(func() any { return bashPPBridgeRequest{ID: id, Op: "call"} }); err != nil {
					t.Errorf("send %d: %v", id, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	last := make(map[uint64]uint64)
	seen := 0
	for _, batch := range groupWriteMessages(t, conn.writes) {
		for _, id := range batch {
			sender := (id - 1) / each
			if id <= last[sender] && last[sender] != 0 {
				t.Fatalf("sender %d: message %d written after %d", sender, id, last[sender])
			}
			last[sender] = id
			seen++
		}
	}
	if seen != senders*each {
		t.Fatalf("%d messages written, want %d", seen, senders*each)
	}
	s.outbox.mu.Lock()
	idle := !s.outbox.flushing && s.outbox.batch == nil
	s.outbox.mu.Unlock()
	if !idle {
		t.Fatal("outbox kept a writer or a batch after every send returned")
	}
}

// A connection closed under a write in progress fails that write and every
// message queued behind it, with the close error the request path classifies,
// and leaves no sender waiting.
func TestS376GroupWriteClosedConnection(t *testing.T) {
	conn := &gatedConn{gate: make(chan struct{}), inside: make(chan struct{}), err: net.ErrClosed}
	gate, inside := conn.gate, conn.inside
	s := &bashPPNativeSession{conn: conn}
	errs := make(chan error, 9)
	go func() { errs <- s.sendMessage(func() any { return bashPPBridgeRequest{ID: 1, Op: "call"} }) }()
	<-inside
	for id := uint64(2); id <= 9; id++ {
		go func() { errs <- s.sendMessage(func() any { return bashPPBridgeRequest{ID: id, Op: "call"} }) }()
	}
	for {
		s.outbox.mu.Lock()
		queued := s.outbox.batch != nil && bytes.Count(s.outbox.batch.buf.Bytes(), []byte("\n")) == 8
		s.outbox.mu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	for range 9 {
		select {
		case err := <-errs:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("a sender on the closed connection got %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a sender was left waiting on a closed connection")
		}
	}
}

// Every route slot function is its own, never inlined, so each has a code
// address no other has.
func TestS376RouteSlotSource(t *testing.T) {
	source := bashPPRouteSlotWorkerSource()
	for i := range bashPPRouteSlots {
		decl := fmt.Sprintf("//go:noinline\nfunc bppRouteSlot%d(f func()){f()}\n", i)
		if strings.Count(source, decl) != 1 {
			t.Fatalf("slot %d is not declared exactly once as a noinline function", i)
		}
		if strings.Count(source, fmt.Sprintf("bppRouteSlot%d,", i)) != 1 {
			t.Fatalf("slot %d is not listed exactly once", i)
		}
	}
}
