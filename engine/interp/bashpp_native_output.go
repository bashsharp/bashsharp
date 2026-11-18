package interp

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Ordered output across the dependency boundary. The helper process writes the
// program's stdout/stderr itself; the interpreter writes its own output
// (println, panic reports) directly to the caller's writers. When a caller
// writer is not a real file the child's bytes travel through a pipe, so a
// request could be answered on the control channel before its output had
// drained, and the interpreter's next direct write would overtake it. Each
// drain owns that pipe and supports a barrier: the host writes a random
// sentinel into the same pipe, and once the copier has consumed the sentinel,
// every child byte written before the request's reply has already reached the
// caller's writer. Real files need no drain — child and interpreter writes
// share one kernel object and order themselves.

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

type bashPPNativeOutputDrain struct {
	read     *os.File
	write    *os.File
	sink     io.Writer
	sentinel []byte
	// workerSentinel is written by the dependency before its control reply.
	// Its sequence is independent from sentinel, which the host still uses to
	// order callback output while the dependency call is in flight.
	workerSentinel []byte
	workerMask     uint8
	workerReached  atomic.Uint64
	// mu serializes barriers so at most one sentinel is in flight.
	mu        sync.Mutex
	reached   chan struct{} // one token per consumed sentinel
	finished  chan struct{} // closed when the copier stops
	closeOnce sync.Once
}

func newBashPPNativeOutputDrain(sink io.Writer) (*bashPPNativeOutputDrain, error) {
	return newBashPPNativeOutputDrainWithWorker(sink, nil)
}

func newBashPPNativeOutputDrainWithWorker(sink io.Writer, workerSentinel []byte, workerMask ...uint8) (*bashPPNativeOutputDrain, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	sentinel := make([]byte, 16)
	if _, err := rand.Read(sentinel); err != nil {
		r.Close()
		w.Close()
		return nil, err
	}
	mask := uint8(1)
	if len(workerMask) > 0 {
		mask = workerMask[0]
	}
	d := &bashPPNativeOutputDrain{
		read: r, write: w, sink: sink, sentinel: sentinel,
		workerSentinel: append([]byte(nil), workerSentinel...),
		workerMask:     mask,
		reached:        make(chan struct{}, 1), finished: make(chan struct{}),
	}
	go d.copy()
	return d, nil
}

// copy forwards child output to the caller's writer, stripping each sentinel
// occurrence and acknowledging it. Only a strict sentinel prefix at the end of
// the buffer is withheld, and never past the barrier that resolves it.
func (d *bashPPNativeOutputDrain) copy() {
	defer close(d.finished)
	defer d.read.Close()
	var held []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := d.read.Read(buf)
		if n > 0 {
			held = append(held, buf[:n]...)
			for {
				i, worker := d.nextSentinel(held)
				if i < 0 {
					break
				}
				if !d.flush(held[:i]) {
					return
				}
				marker := d.sentinel
				if worker {
					marker = d.workerSentinel
				}
				held = append(held[:0], held[i+len(marker):]...)
				if worker {
					d.workerReached.Add(1)
				} else {
					select {
					case d.reached <- struct{}{}:
					default:
					}
				}
			}
			keep := 0
			maxMarker := max(len(d.sentinel), len(d.workerSentinel))
			for k := min(maxMarker-1, len(held)); k > 0; k-- {
				tail := held[len(held)-k:]
				if k <= len(d.sentinel) && bytes.Equal(tail, d.sentinel[:k]) ||
					k <= len(d.workerSentinel) && bytes.Equal(tail, d.workerSentinel[:k]) {
					keep = k
					break
				}
			}
			if !d.flush(held[:len(held)-keep]) {
				return
			}
			held = append(held[:0], held[len(held)-keep:]...)
		}
		if err != nil {
			d.flush(held)
			return
		}
	}
}

func (d *bashPPNativeOutputDrain) nextSentinel(data []byte) (int, bool) {
	host := bytes.Index(data, d.sentinel)
	worker := -1
	if len(d.workerSentinel) > 0 {
		worker = bytes.Index(data, d.workerSentinel)
	}
	if worker >= 0 && (host < 0 || worker < host) {
		return worker, true
	}
	return host, false
}

func (d *bashPPNativeOutputDrain) flush(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	_, err := d.sink.Write(data)
	return err == nil
}

// barrier returns once every byte the child wrote to this stream before the
// call has reached the caller's writer. It never blocks past copier
// termination, so a failed sink or a dead child cannot hang a request.
func (d *bashPPNativeOutputDrain) barrier() {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.reached:
	default:
	}
	if _, err := d.write.Write(d.sentinel); err != nil {
		return
	}
	select {
	case <-d.reached:
	case <-d.finished:
	}
}

func (d *bashPPNativeOutputDrain) awaitWorker(sequence uint64) {
	if d.workerReached.Load() < sequence {
		// A host-written marker follows this reply's writes in the same pipe.
		// It preserves ordering without relying on a shared notification that
		// can leave concurrent request waiters asleep after their markers arrive.
		d.barrier()
	}
}

// closeWrite retires the host's write end. Once the child is gone too, the
// copier sees EOF, flushes what remains and finishes.
func (d *bashPPNativeOutputDrain) closeWrite() {
	d.closeOnce.Do(func() { d.write.Close() })
}

func (s *bashPPNativeSession) drainOutputs() {
	for _, drain := range s.drains {
		drain.barrier()
	}
}

func (s *bashPPNativeSession) awaitOutputBarriers(reply bashPPBridgeResponse) {
	for _, drain := range s.drains {
		var sequence uint64
		switch drain.workerMask {
		case 1:
			sequence = reply.OutputBarrierStdout
		case 2:
			sequence = reply.OutputBarrierStderr
		}
		if sequence > 0 {
			drain.awaitWorker(sequence)
		} else {
			// A dependency can close or replace its own descriptor. Its marker
			// then cannot be written, so retain the host-written fallback.
			drain.barrier()
		}
	}
}

// callbackOutputWriter orders the first direct interpreter write in a callback
// after any output the dependency emitted before the callback. The dependency
// and its callbacks otherwise run on separate output paths; when a callback
// produces no direct output, its earlier per-callback pipe barriers were
// unnecessary. A shared once orders stdout and stderr as one boundary.
type callbackOutputWriter struct {
	writer io.Writer
	once   *sync.Once
	bridge *bashPPNativeSession
}

func (w *callbackOutputWriter) Write(p []byte) (int, error) {
	w.once.Do(w.bridge.drainOutputs)
	return w.writer.Write(p)
}

func (s *bashPPNativeSession) lazyCallbackOutputBarrier(r *Runner) func() {
	if len(s.drains) == 0 {
		return func() {}
	}
	stdout, stderr := r.stdout, r.stderr
	origStdout, origStderr := r.origStdout, r.origStderr
	once := new(sync.Once)
	wrap := func(w io.Writer) io.Writer {
		if w == nil {
			return nil
		}
		// A real descriptor already shares kernel ordering with the helper;
		// retaining its concrete type also preserves exec/TTY/fd behavior.
		if _, ok := w.(interface{ Fd() uintptr }); ok {
			return w
		}
		return &callbackOutputWriter{writer: w, once: once, bridge: s}
	}
	r.stdout, r.stderr = wrap(stdout), wrap(stderr)
	r.origStdout, r.origStderr = wrap(origStdout), wrap(origStderr)
	return func() {
		r.stdout, r.stderr = stdout, stderr
		r.origStdout, r.origStderr = origStdout, origStderr
	}
}

func (s *bashPPNativeSession) closeDrains() {
	for _, drain := range s.drains {
		drain.closeWrite()
	}
}

// waitDrains blocks until every copier has flushed its remaining output, so a
// terminated program's final bytes are visible before the interpreter reports
// its exit.
func (s *bashPPNativeSession) waitDrains() {
	for _, drain := range s.drains {
		<-drain.finished
	}
}

// bashPPSameWriter mirrors os/exec's interfaceEqual: one writer given for both
// standard streams gets one pipe, preserving the child's own interleaving.
func bashPPSameWriter(a, b io.Writer) (same bool) {
	if a == nil || b == nil {
		return false
	}
	defer func() { _ = recover() }()
	return a == b
}
