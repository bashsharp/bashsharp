package interp

import (
	"bytes"
	"errors"
	"net"
	"sync"
)

// Sprint: #376; Story: #1550; Story-ID: cae490ea17e7
//
// Group commit for the control connection.
//
// Every message used to be its own write system call, made while holding the
// session's write lock. Tasks calling into the dependency in parallel — a
// hundred goroutines each calling a reflect.MakeFunc function — then queued
// on that lock for one system call each, and the reader on the other side
// was woken once per message.
//
// A sender now encodes its message into the open batch. If no write is in
// progress it writes that batch itself, so a lone sender still makes exactly
// one write per message and hands nothing to another goroutine. A sender that
// arrives during a write leaves its message in the next batch and waits for
// the writer, which takes whatever accumulated while it was in the system
// call and writes it at once. Messages reach the connection in the order
// they were encoded, every sender still learns whether its own bytes were
// written, and the write lock is still held across every write, so closing
// the connection excludes writers as before.
//
// The writer does not wait for a batch to fill. Yielding before the write so
// that parallel senders could join it was measured and made the scaled
// fan-out slower at every processor count tried.

type bashPPBridgeBatch struct {
	buf *bytes.Buffer
	// done is closed once err holds the result of writing buf.
	done chan struct{}
	err  error
}

type bashPPBridgeOutbox struct {
	mu       sync.Mutex
	batch    *bashPPBridgeBatch
	flushing bool
	// spare is the last written batch's buffer, kept for the next batch.
	spare *bytes.Buffer
}

// bashPPBridgeFlushRounds bounds how many batches a sender writes on behalf of
// the others before it hands the role to a goroutine of its own and returns
// to its request.
const bashPPBridgeFlushRounds = 8

// sendMessage queues the message prepare returns and reports the result of
// the write that carried it. prepare runs under the queue lock: whatever it
// drains for this message (handle and origin releases) is encoded in the
// order the worker will read it.
func (s *bashPPNativeSession) sendMessage(prepare func() any) error {
	o := &s.outbox
	o.mu.Lock()
	batch := o.batch
	if batch == nil {
		batch = &bashPPBridgeBatch{buf: o.spare, done: make(chan struct{})}
		if o.spare = nil; batch.buf == nil {
			batch.buf = new(bytes.Buffer)
		}
		o.batch = batch
	}
	mark := batch.buf.Len()
	if err := bashPPBridgeEncoder(batch.buf).Encode(prepare()); err != nil {
		batch.buf.Truncate(mark)
		o.mu.Unlock()
		return err
	}
	if o.flushing {
		o.mu.Unlock()
		<-batch.done
		return batch.err
	}
	o.flushing = true
	o.mu.Unlock()
	s.flushOutbox()
	<-batch.done
	return batch.err
}

// flushOutbox writes batches until none is open. Its caller owns the
// flushing role, which it gives up only under the queue lock with no batch
// left, so a message is never queued behind a writer that has stopped.
func (s *bashPPNativeSession) flushOutbox() {
	o := &s.outbox
	for round := 0; ; round++ {
		o.mu.Lock()
		batch := o.batch
		if batch == nil {
			o.flushing = false
			o.mu.Unlock()
			return
		}
		if round == bashPPBridgeFlushRounds {
			o.mu.Unlock()
			go s.flushOutbox()
			return
		}
		o.batch = nil
		o.mu.Unlock()
		s.write.Lock()
		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			batch.err = errors.New("gosource: native bridge connection is unavailable")
		} else {
			_, batch.err = conn.Write(batch.buf.Bytes())
		}
		s.write.Unlock()
		if batch.buf.Cap() <= bashPPBridgeSocketBuffer {
			batch.buf.Reset()
			o.mu.Lock()
			o.spare = batch.buf
			o.mu.Unlock()
		}
		batch.buf = nil
		close(batch.done)
	}
}

// bashPPBridgeSocketBuffer is the kernel buffer requested for each direction
// of the control connection. A batch must fit in it: some systems default a
// local stream socket to a few kilobytes, and a writer that fills the buffer
// blocks until the reader has been scheduled, once per batch.
const bashPPBridgeSocketBuffer = 1 << 20

func bashPPBridgeSocketBuffers(conn net.Conn) {
	if c, ok := conn.(interface {
		SetReadBuffer(int) error
		SetWriteBuffer(int) error
	}); ok {
		_ = c.SetReadBuffer(bashPPBridgeSocketBuffer)
		_ = c.SetWriteBuffer(bashPPBridgeSocketBuffer)
	}
}
