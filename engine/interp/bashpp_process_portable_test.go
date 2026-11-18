// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Portable counterparts of the os/exec-derived surface fixtures in
// bashpp_process_surface_test.go. Source provenance is pinned there and in
// bashpp_process_upstream_test.go (Go 862c888e612ac346c7c4d99c9392bdfd265f33b0,
// BSD-3-Clause). Only the child launcher changes: this test binary supplies
// deterministic stdout/stderr/exit and readiness-before-cancellation, instead
// of relying on a host POSIX shell. The public run/start/Lines/Wait/Close
// spellings and lifecycle assertions remain the measured interface.
func TestBashPPPortableProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "edges":
		fmt.Fprint(os.Stdout, "a\n\nb")
	case "empty":
	case "newline":
		fmt.Fprint(os.Stdout, "\n")
	case "terminated":
		fmt.Fprint(os.Stdout, "a\nb\n")
	case "exit":
		fmt.Fprint(os.Stdout, "one\ntwo\n")
		os.Exit(7)
	case "large":
		for i := 0; i < 20000; i++ {
			fmt.Fprintf(os.Stdout, "out %d\n", i)
			fmt.Fprintf(os.Stderr, "err %d\n", i)
		}
	case "sleep":
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(30 * time.Second)
	case "flood":
		for i := 0; i < 100000; i++ {
			fmt.Fprintf(os.Stdout, "line %d\n", i)
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func portableProcessCall(t *testing.T, mode string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Quote(executable) + `, "-test.run=^TestBashPPPortableProcessHelper$", "--", ` + strconv.Quote(mode)
}

func TestBashPPPortableProcessSurface(t *testing.T) {
	for _, operation := range []string{"run", "start"} {
		for _, tc := range []struct{ mode, want string }{{"edges", "[a][][b]"}, {"empty", ""}, {"newline", "[]"}, {"terminated", "[a][b]"}, {"exit", "[one][two]"}} {
			t.Run(operation+"/"+tc.mode, func(t *testing.T) {
				tail := `printf ' status=%s err=[%s]\n' p.Status "$err"`
				if operation == "start" {
					tail = `status, err := p.Wait(); printf ' status=%s err=[%s]\n' "$status" "$err"`
				}
				src := "func main() {\np, err := " + operation + "(" + portableProcessCall(t, tc.mode) + ")\nfor line := range p.Lines() { printf '[%s]' \"$line\" }\n" + tail + "\n}\nmain()\n"
				out, stderr, err := processSurfaceRun(t, src)
				code := 0
				if tc.mode == "exit" {
					code = 7
				}
				want := fmt.Sprintf("%s status=%d err=[]\n", tc.want, code)
				if err != nil || stderr != "" || out != want {
					t.Fatalf("output=%q stderr=%q err=%v want=%q", out, stderr, err, want)
				}
			})
		}
	}
	t.Run("double-wait", func(t *testing.T) {
		src := "func main() {\np, err := start(" + portableProcessCall(t, "exit") + ")\n" + `for line := range p.Lines() { : }
 s1, e1 := p.Wait()
 s2, e2 := p.Wait()
 p.Close()
 echo "closed=$?"
 p.Wait()
 echo "again=$? $s1/$s2 [$e1][$e2]"
}
main()
`
		out, stderr, err := processSurfaceRun(t, src)
		if err != nil || stderr != "" || out != "closed=0\nagain=7 7/7 [][]\n" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
	for _, operation := range []string{"run", "start"} {
		t.Run(operation+"/large", func(t *testing.T) {
			tail := `printf 'n=%s status=%s\n' "$n" p.Status`
			if operation == "start" {
				tail = `status, err := p.Wait(); echo "n=$n status=$status"`
			}
			src := "func main() {\np, err := " + operation + "(" + portableProcessCall(t, "large") + ")\nn := 0\nfor line := range p.Lines() { n++ }\n" + tail + "\n}\nmain()\n"
			out, stderr, err := processSurfaceRun(t, src)
			wantLines := 0
			if operation == "start" {
				wantLines = 20000
			}
			if err != nil || out != "n=20000 status=0\n" || strings.Count(stderr, "\n") != wantLines {
				t.Fatalf("out=%q stderr lines=%d err=%v", out, strings.Count(stderr, "\n"), err)
			}
		})
	}
	for _, closeExplicitly := range []bool{false, true} {
		t.Run(fmt.Sprintf("early-close/%v", closeExplicitly), func(t *testing.T) {
			tail := ""
			if closeExplicitly {
				tail = "p.Close()\nstatus, err := p.Wait()\necho \"status=$status canceled=$([ -n \"$err\" ] && echo yes || echo no)\"\n"
			}
			src := "func main() {\np, err := start(" + portableProcessCall(t, "flood") + ")\nfor line := range p.Lines() { echo \"$line\"; break }\n" + tail + "}\nmain()\n"
			var out, diagnostic strings.Builder
			r := captureRunner(t, &diagnostic, StdIO(nil, &out, &diagnostic))
			began := time.Now()
			err := r.Run(context.Background(), parseBashPPInternal(t, src))
			want := "line 0\n"
			if closeExplicitly {
				want += "status=1 canceled=yes\n"
			}
			if err != nil || out.String() != want || time.Since(began) > 10*time.Second || len(r.bashPPProcs.procs) != 0 {
				t.Fatalf("out=%q err=%v diagnostics=%q remaining=%d", out.String(), err, diagnostic.String(), len(r.bashPPProcs.procs))
			}
		})
	}
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var out lockedBuffer
		var diagnostic strings.Builder
		r := captureRunner(t, &diagnostic, StdIO(nil, &out, &diagnostic))
		src := "func main() {\np, err := start(" + portableProcessCall(t, "sleep") + ")\nfor line := range p.Lines() { echo \"got $line\" }\n}\nmain()\n"
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			for {
				if strings.Contains(out.String(), "got ready") {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		}()
		began := time.Now()
		_ = r.Run(ctx, parseBashPPInternal(t, src))
		<-finished
		if !strings.Contains(out.String(), "got ready") || time.Since(began) > 5*time.Second {
			t.Fatalf("cancellation did not reap ready child: %q %s", out.String(), diagnostic.String())
		}
		if p := r.bashPPProcessHandle("p"); p != nil {
			_, err := p.Wait()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Wait=%v", err)
			}
		}
		if len(r.bashPPProcs.procs) != 0 {
			t.Fatal("canceled process remains registered")
		}
	})
	t.Run("no-leak", func(t *testing.T) {
		args := portableProcessCall(t, "sleep")
		before := runtime.NumGoroutine()
		for i := 0; i < 10; i++ {
			out, stderr, err := processSurfaceRun(t, "func main() {\np, err := start("+args+")\nfor line := range p.Lines() { break }\np.Close()\n}\nmain()\n")
			if err != nil || out != "" || stderr != "" {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before+3 && time.Now().Before(deadline) {
			runtime.GC()
			time.Sleep(20 * time.Millisecond)
		}
		if after := runtime.NumGoroutine(); after > before+3 {
			t.Fatalf("goroutines grew %d -> %d", before, after)
		}
	})
}

// A blocked producer must not prevent its consumer from writing to the
// foreground sink before advancing Lines. The handshake makes the observed
// Windows early-close deadlock deterministic without relying on scheduling.
type portableBlockedProcessWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *portableBlockedProcessWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}
func TestBashPPProcessPrivatePipeDoesNotLockForeground(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	sink := &portableBlockedProcessWriter{entered: make(chan struct{}), release: make(chan struct{})}
	pipe := &bashPPProcessWriter{w: sink}
	producerDone := make(chan struct{})
	defer func() { close(sink.release); <-producerDone }()
	go func() { defer close(producerDone); _, _ = c.writer(pipe).Write([]byte("pending")) }()
	<-sink.entered
	var foreground strings.Builder
	done := make(chan struct{})
	go func() { defer close(done); _, _ = c.writer(&foreground).Write([]byte("consumed")) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked private pipe holds foreground writer lock")
	}
	if foreground.String() != "consumed" {
		t.Fatalf("foreground=%q", foreground.String())
	}
	// printf/echo use an outer record lock as well as per-Write synchronization.
	child := &Runner{stdout: pipe, bashPPConcurrent: c}
	unlock := child.bashPPLockLogicalOutput("echo")
	if !c.logicalMu.TryLock() {
		unlock()
		t.Fatal("private pipe holds foreground logical output lock")
	}
	c.logicalMu.Unlock()
	unlock()
}
