//go:build full

package interp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A callback ID from a completed run must not resolve to the new run's closure
// with the same numeric ID. Probe two real initialized dependency sessions.
func TestGoSourceFunctionCallbackStaleSession(t *testing.T) {
	var runner *Runner
	var old bashPPBridgeValue
	var probeErr error
	round := 0
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !bytes.Contains(p, []byte("capture")) {
			return len(p), nil
		}
		req, err := runner.bashPPEvalRequest()
		if err != nil {
			probeErr = err
			return len(p), nil
		}
		if round == 0 {
			req.Bridge.mu.Lock()
			for id := range req.Bridge.functions {
				old = bashPPBridgeValue{Kind: "callback", Callbacks: true, Session: req.Bridge.id, Handle: id}
				break
			}
			req.Bridge.mu.Unlock()
		} else {
			alias := ""
			for name, path := range req.Imports {
				if path == "strings" {
					alias = name
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, probeErr = runner.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "call", Selector: alias + ".Map", Args: []bashPPBridgeValue{old, {Kind: "string", Type: "string", Text: "a"}}})
		}
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	const source = `package main
import "strings"
func main(){strings.Map(func(r rune)rune{println("capture");return r},"a")}`
	for round = 0; round < 2; round++ {
		if round > 0 {
			runner.Reset()
		}
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(runner.Dir, fmt.Sprintf("round%d.go", round)), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = runner.Run(ctx, p.File)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if old.Session == "" || old.Handle == 0 {
		t.Fatal("first original callback was not registered")
	}
	if probeErr == nil || !strings.Contains(probeErr.Error(), "another dependency session") {
		t.Fatalf("stale callback accepted: %v", probeErr)
	}
}

func TestGoSourceLazyCallbackOutputBarrier(t *testing.T) {
	var output bytes.Buffer
	drain, err := newBashPPNativeOutputDrain(&output)
	if err != nil {
		t.Fatal(err)
	}
	defer drain.closeWrite()

	session := &bashPPNativeSession{drains: []*bashPPNativeOutputDrain{drain}}
	runner := &Runner{stdout: &output, stderr: &output, origStdout: &output, origStderr: &output}
	if _, err := drain.write.Write([]byte("native-before\n")); err != nil {
		t.Fatal(err)
	}

	restore := session.lazyCallbackOutputBarrier(runner)
	if _, err := runner.stdout.Write([]byte("callback\n")); err != nil {
		t.Fatal(err)
	}
	restore()
	drain.closeWrite()
	<-drain.finished

	if got := output.String(); got != "native-before\ncallback\n" {
		t.Fatalf("callback output overtook native output: %q", got)
	}
	if _, wrapped := runner.stdout.(*callbackOutputWriter); wrapped {
		t.Fatal("callback output wrapper was not restored")
	}
}

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Ordinary dependency replies carry a worker-written marker. Multiple replies
// may reach the copier before their request goroutines resume, so acknowledgments
// are sequence-based rather than a lossy one-token notification.
func TestGoSourceWorkerOutputBarrierSequence(t *testing.T) {
	var output bytes.Buffer
	marker := []byte("worker-output-marker")
	drain, err := newBashPPNativeOutputDrainWithWorker(&output, marker, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer drain.closeWrite()

	for i := 0; i < 3; i++ {
		if _, err := drain.write.Write(append([]byte("native\n"), marker...)); err != nil {
			t.Fatal(err)
		}
	}
	drain.awaitWorker(3)
	if _, err := output.WriteString("local\n"); err != nil {
		t.Fatal(err)
	}
	drain.closeWrite()
	<-drain.finished

	if got, want := output.String(), "native\nnative\nnative\nlocal\n"; got != want {
		t.Fatalf("worker barriers leaked or reordered output: got %q want %q", got, want)
	}
}

// Replies can reach their waiters out of sequence. An early marker must not
// wake only a later-sequence waiter and strand an earlier reply indefinitely.
func TestGoSourceWorkerOutputBarrierConcurrentWaiters(t *testing.T) {
	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)
	var output bytes.Buffer
	copying, release := make(chan struct{}), make(chan struct{})
	sink := callbackProbeWriter(func(p []byte) (int, error) {
		close(copying)
		<-release
		return output.Write(p)
	})
	marker := []byte("worker-output-marker")
	drain, err := newBashPPNativeOutputDrainWithWorker(sink, marker, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { drain.closeWrite(); <-drain.finished }()
	if _, err := drain.write.Write([]byte("native\n")); err != nil {
		t.Fatal(err)
	}
	<-copying
	const waiters = 8
	started, done := make(chan struct{}), make(chan struct{}, waiters)
	for i := 1; i <= waiters; i++ {
		sequence := (i+waiters-2)%waiters + 1
		go func(sequence uint64) {
			started <- struct{}{}
			drain.awaitWorker(sequence)
			done <- struct{}{}
		}(uint64(sequence))
		<-started
		// Let each waiter enter its wait while the copier is held at the sink.
		runtime.Gosched()
	}
	close(release)
	// Register sequence 8 first. Its first notification arrives before its
	// target, so it must wait again without stealing sequence 1's progress.
	for i := 0; i < waiters; i++ {
		if _, err := drain.write.Write(marker); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 32; j++ {
			runtime.Gosched()
		}
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for i := 0; i < waiters; i++ {
		select {
		case <-done:
		case <-deadline.C:
			t.Fatalf("only %d/%d concurrent output waiters returned before drain closure", i, waiters)
		}
	}
	if got := output.String(); got != "native\n" {
		t.Fatalf("output = %q", got)
	}
}

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// A failed worker marker on one reply must not consume a global sequence number
// that a later successful marker on the same stream can never reach. Per-stream
// sequences count only successful markers for that stream.
func TestGoSourceWorkerOutputBarrierFailedMarkerThenSuccess(t *testing.T) {
	var output bytes.Buffer
	marker := []byte("worker-output-marker")
	drain, err := newBashPPNativeOutputDrainWithWorker(&output, marker, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer drain.closeWrite()
	session := &bashPPNativeSession{drains: []*bashPPNativeOutputDrain{drain}}

	if _, err := drain.write.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	session.awaitOutputBarriers(bashPPBridgeResponse{})

	if _, err := drain.write.Write(append([]byte("second\n"), marker...)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		session.awaitOutputBarriers(bashPPBridgeResponse{OutputBarrierStdout: 1})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("awaiting later successful worker marker blocked behind failed marker sequence")
	}
	if _, err := output.WriteString("local\n"); err != nil {
		t.Fatal(err)
	}
	drain.closeWrite()
	<-drain.finished

	if got, want := output.String(), "first\nsecond\nlocal\n"; got != want {
		t.Fatalf("failed-marker fallback reordered output: got %q want %q", got, want)
	}
}
