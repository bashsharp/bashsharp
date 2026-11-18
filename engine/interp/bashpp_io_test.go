// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type overlapWriter struct {
	active  atomic.Int32
	overlap atomic.Bool
	mu      sync.Mutex
	buf     bytes.Buffer // bashpp-racegate:safe-synchronized
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	defer w.active.Add(-1)
	time.Sleep(100 * time.Microsecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func runBashPPIO(t *testing.T, out *overlapWriter, src string, opts ...RunnerOption) error {
	t.Helper()
	opts = append(opts, Lang(syntax.LangBashPP), StdIO(nil, out, out))
	r, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "io.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return r.Run(context.Background(), f)
}

func TestBashPPConcurrentBuiltinStdoutStderrSerialized(t *testing.T) {
	w := new(overlapWriter)
	err := runBashPPIO(t, w, `
func spam(start, ack, value) {
 <-start
 echo "$value-1"
 echo "$value-2" >&2
 echo "$value-3"
 echo "$value-4" >&2
 ack <- ok
}
func main() {
 a := make(chan string); b := make(chan string); ack := make(chan string)
 go spam(a, ack, a); go spam(b, ack, b)
 a <- go; b <- go
 <-ack; <-ack
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if w.overlap.Load() {
		t.Fatal("builtin stdout/stderr writes overlapped")
	}
}

func TestBashPPConcurrentCustomHandlerOutputSerialized(t *testing.T) {
	w := new(overlapWriter)
	handler := func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] != "emit" {
				return next(ctx, args)
			}
			hc := HandlerCtx(ctx)
			for i := range 20 {
				fmt.Fprintf(hc.Stdout, "%s-%d\n", args[1], i)
				fmt.Fprintf(hc.Stderr, "%s-e%d\n", args[1], i)
			}
			return nil
		}
	}
	err := runBashPPIO(t, w, `
func worker(ack, value) { emit "$value"; ack <- ok; }
func main() {
 ack := make(chan string)
 go worker(ack, one); go worker(ack, two)
 <-ack; <-ack
}
main()
`, ExecHandlers(handler))
	if err != nil {
		t.Fatal(err)
	}
	if w.overlap.Load() {
		t.Fatal("custom handler stdout/stderr writes overlapped")
	}
}

func TestBashPPConcurrentExternalOutputSerialized(t *testing.T) {
	w := new(overlapWriter)
	err := runBashPPIO(t, w, `
func worker(ack, value) {
 /bin/sh -c 'i=0; while [ $i -lt 20 ]; do echo "$1-$i"; echo "$1-e$i" >&2; i=$((i+1)); done' sh "$value"
 ack <- ok
}

func main() {
 ack := make(chan string)
 go worker(ack, one); go worker(ack, two)
 <-ack; <-ack
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if w.overlap.Load() {
		t.Fatal("external process stdout/stderr writes overlapped")
	}
}

func TestBashPPLockedWriterIsIdempotentAndPreservesPipelineOuter(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	base := new(overlapWriter)
	wrapped := c.writer(base)
	if again := c.writer(wrapped); again != wrapped {
		t.Fatal("already locked writer was wrapped twice")
	}
	pipeline := &pipelineWriter{w: base, runner: new(Runner)}
	wrappedPipeline, ok := c.writer(pipeline).(*pipelineWriter)
	if !ok {
		t.Fatalf("pipeline outer shape lost: %T", c.writer(pipeline))
	}
	if _, ok := wrappedPipeline.w.(*bashPPLockedWriter); !ok {
		t.Fatalf("pipeline sink not serialized: %T", wrappedPipeline.w)
	}
	if again := c.writer(wrappedPipeline); again != wrappedPipeline {
		t.Fatal("already wrapped pipeline was rebuilt")
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = wrapped.Write([]byte("plain\n")) }()
		go func() { defer wg.Done(); _, _ = wrappedPipeline.Write([]byte("pipe\n")) }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nested writer facade deadlocked")
	}
	if base.overlap.Load() {
		t.Fatal("plain and pipeline writes did not share serialization")
	}
}

func TestBashPPObserverCallbacksSerialized(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	r := &Runner{bashPPConcurrent: c}
	var active atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.bashPPObserve(func() {
				if active.Add(1) != 1 {
					overlap.Store(true)
				}
				time.Sleep(100 * time.Microsecond)
				active.Add(-1)
			})
		}()
	}
	wg.Wait()
	if overlap.Load() {
		t.Fatal("observer callbacks overlapped")
	}
}

func TestBashPPConcurrentEchoLogicalLinesAtomic(t *testing.T) {
	w := new(overlapWriter)
	err := runBashPPIO(t, w, `
func line(start, ack, value) {
 <-start
 echo "$value" "$value" "$value" "$value"
 ack <- ok
}
func main() {
 start := make(chan string, 16); ack := make(chan string, 16)
 go line(start, ack, a); go line(start, ack, b); go line(start, ack, c); go line(start, ack, d)
 start <- x; start <- x; start <- x; start <- x
 <-ack; <-ack; <-ack; <-ack
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	output := w.buf.String()
	w.mu.Unlock()
	lines := strings.Fields(strings.TrimSpace(output))
	if len(lines) != 16 {
		t.Fatalf("fragmented output %q", output)
	}
	for i := 0; i < len(lines); i += 4 {
		if lines[i] != lines[i+1] || lines[i] != lines[i+2] || lines[i] != lines[i+3] {
			t.Fatalf("logical echo line interleaved: %q", output)
		}
	}
}
