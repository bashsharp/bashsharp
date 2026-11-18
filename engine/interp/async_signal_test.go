//go:build unix

package interp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func asyncSignalFile(t *testing.T, source string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(source), "signal.sh")
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestAsyncSelfSignalTargetsForegroundOwner(t *testing.T) {
	failedExec := filepath.Join(t.TempDir(), "failed-exec")
	if err := os.WriteFile(failedExec, []byte("#!/definitely/missing/interpreter\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, foreground := range []string{"wait", "read input", "while :; do :; done", "exec /bin/sleep 5", "exec " + failedExec, "alias recovery"} {
		t.Run(foreground, func(t *testing.T) {
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, released := make(chan struct{}), make(chan struct{})
			stdin, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			defer writer.Close()
			var output bytes.Buffer // bashpp-racegate:safe-synchronized writes precede entered closure; cleanup joins the sender.
			r, err := New(StdIO(stdin, &output, &output), ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					if args[0] == "hold" {
						close(entered)
						<-ctx.Done()
						close(released)
						return ctx.Err()
					}
					// Force the signal to win before the default handler can
					// publish or launch either the valid or failing exec.
					select {
					case <-entered:
						return next(ctx, args)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			var file *syntax.File
			if foreground == "alias recovery" {
				const setup = "shopt -s expand_aliases; alias recovered='(kill -TERM $$; printf sender:%s \"$?\"; hold) & wait'\n"
				if err := WithBashSource([]byte(setup + "recovered\n"))(r); err != nil {
					t.Fatal(err)
				}
				if err := r.Run(caller, asyncSignalFile(t, setup)); err != nil {
					t.Fatal(err)
				}
			} else {
				file = asyncSignalFile(t, "(kill -TERM $$; printf 'sender:%s' \"$?\"; hold) & "+foreground)
			}
			done := make(chan error, 1)
			runFinished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				<-runFinished
				r.Reset()
			})
			go func() {
				defer close(runFinished)
				if foreground == "alias recovery" {
					if !r.RunAliasExpandedSourceLine(caller, 2) {
						done <- errors.New("alias line was not recovered")
						return
					}
					done <- r.exit.err
					return
				}
				done <- r.Run(caller, file)
			}()
			select {
			case err := <-done:
				var status ExitStatus
				if !errors.As(err, &status) || status != 143 {
					t.Fatalf("owner status = %v, want 143", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("owner waited for its sender or a hypothetical exec")
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("sender did not survive its successful kill")
			}
			if got := output.String(); got != "sender:0" {
				t.Fatalf("sender output = %q, want sender:0", got)
			}
			select {
			case <-released:
				t.Fatal("owner termination canceled the sender")
			default:
			}
			cancel()
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("sender lost caller cancellation")
			}
		})
	}
}

func TestAsyncSelfSignalBetweenRunsAndReset(t *testing.T) {
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := New(ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); r.Reset() })
	if err := r.Run(caller, asyncSignalFile(t, "(hold; kill -TERM $$) &")); err != nil {
		t.Fatal(err)
	}
	<-entered
	close(release)
	<-r.lastBangProc().done
	if err := r.Run(caller, asyncSignalFile(t, ":")); err == nil || err.Error() != ExitStatus(143).Error() {
		t.Fatalf("between-Run signal lost: %v", err)
	}
	r.Reset()
	if err := r.Run(caller, asyncSignalFile(t, ":")); err != nil {
		t.Fatalf("Reset retained a previous owner's signal: %v", err)
	}
}

func TestAsyncSelfSignalFailedPublishedReplacement(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	defer r.Reset()
	attempt := &execReplacementAttempt{ready: make(chan struct{})}
	r.execReplacement.current.Store(attempt)
	close(attempt.ready) // Failed start: no process identity was published.
	child := r.Subshell()
	child.asyncList = true
	var out bytes.Buffer // bashpp-racegate:safe-private synchronous child Run owns the writer.
	StdIO(nil, &out, &out)(child)
	if err := child.Run(t.Context(), asyncSignalFile(t, "kill -TERM $$; printf '%s' \"$?\"")); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "1" {
		t.Fatalf("failed replacement kill = %q, want 1", got)
	}
	if r.execReplacement.pending != nil {
		t.Fatal("failed published replacement redirected its signal to the owner")
	}
	r.execReplacement.current.CompareAndSwap(attempt, nil)
}

func TestAsyncSelfSignalUsesCurrentOwnerDisposition(t *testing.T) {
	disposition, saved := saveSignalDisposition(syscall.SIGTERM)
	t.Cleanup(func() {
		signal.Reset(syscall.SIGTERM)
		if saved {
			restoreSignalDisposition(syscall.SIGTERM, disposition)
		}
	})
	for _, action := range []string{"echo PARENT-TRAP", ""} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var output syncBuffer
			r, err := New(StdIO(nil, &output, &output), ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					switch args[0] {
					case "hold":
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
					case "release":
						<-entered
						close(release)
					}
					return nil
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Reset()
			// The sender inherits ignored TERM; the parent installs its new
			// disposition only after that sender has been created.
			source := "trap '' TERM; (hold; kill -TERM $$; echo SENDER-OK) & trap '" + action + "' TERM; release; wait; echo PARENT-AFTER"
			if err := r.Run(t.Context(), asyncSignalFile(t, source)); err != nil {
				t.Fatal(err)
			}
			<-r.lastBangProc().done
			got := output.String()
			if !strings.Contains(got, "SENDER-OK\n") || !strings.Contains(got, "PARENT-AFTER\n") {
				t.Fatalf("owner/sender did not survive: %q", got)
			}
			if strings.Count(got, "PARENT-TRAP\n") != boolInt(action != "") {
				t.Fatalf("wrong owner's disposition used: %q", got)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAsyncSelfSignalExitCleanup(t *testing.T) {
	for _, sig := range []string{"TERM", "KILL"} {
		t.Run("external-child-exit-override/"+sig, func(t *testing.T) {
			r, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Reset()
			file := asyncSignalFile(t, "trap 'exit 7' EXIT; /bin/sh -c 'kill -"+sig+" $$'")
			for _, stmt := range file.Stmts {
				err = r.Run(t.Context(), stmt)
			}
			var status ExitStatus
			want := ExitStatus(143)
			if sig == "KILL" {
				want = 137
			}
			if !errors.As(err, &status) || status != want {
				t.Fatalf("external child status = %v, want %v", err, want)
			}
			err = r.Run(t.Context(), &syntax.File{})
			if !errors.As(err, &status) || status != 7 {
				t.Fatalf("ordinary child signal overrode EXIT: %v, want 7", err)
			}
		})
	}
	for _, mode := range []string{"file", "incremental"} {
		for _, sig := range []string{"TERM", "KILL"} {
			t.Run(mode+"/"+sig, func(t *testing.T) {
				var output syncBuffer
				r, err := New(StdIO(nil, &output, &output))
				if err != nil {
					t.Fatal(err)
				}
				defer r.Reset()
				// A sender-local trap must never shadow the owner's default.
				// KILL metadata is retained by Bash, but cannot catch the signal.
				source := "trap 'printf \"EXIT-CLEANUP\\n\"; exit 7' EXIT; trap 'echo WRONG-KILL' KILL; (trap 'echo WRONG-SENDER' TERM; kill -" + sig + " $$; printf 'SENDER-OK\\n') & wait; echo WRONG-AFTER"
				file := asyncSignalFile(t, source)
				if mode == "file" {
					err = r.Run(t.Context(), file)
				} else {
					for _, stmt := range file.Stmts {
						if err = r.Run(t.Context(), stmt); err != nil {
							break
						}
					}
					if strings.Contains(output.String(), "EXIT-CLEANUP") {
						t.Fatal("statement ran EXIT before final cleanup")
					}
					err = r.Run(t.Context(), &syntax.File{})
				}
				want := ExitStatus(143)
				if sig == "KILL" {
					want = 137
				}
				var status ExitStatus
				if !errors.As(err, &status) || status != want {
					t.Fatalf("owner status = %v, want %v", err, want)
				}
				<-r.lastBangProc().done
				got := output.String()
				if strings.Contains(got, "WRONG") || !strings.Contains(got, "SENDER-OK\n") {
					t.Fatalf("wrong owner lifetime: %q", got)
				}
				if strings.Count(got, "EXIT-CLEANUP\n") != boolInt(sig == "TERM") {
					t.Fatalf("wrong EXIT cleanup for %s: %q", sig, got)
				}
			})
		}
	}

}
func TestAsyncSelfSignalPreservesCallerContext(t *testing.T) {
	for _, derived := range []bool{false, true} {
		t.Run(fmt.Sprint(derived), func(t *testing.T) {
			deadline := time.Now().Add(time.Minute)
			caller, stopDeadline := context.WithDeadline(t.Context(), deadline)
			defer stopDeadline()
			caller, cancel := context.WithCancelCause(caller)
			defer cancel(nil)
			cause := errors.New("caller cancellation")
			entered := make(chan context.Context, 1)
			finished := make(chan error, 1)
			var r *Runner
			var err error
			r, err = New(ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					if args[0] == "nested" {
						// A new Done channel is an independent embedder scope;
						// background creation must not unwrap it to the outer Run.
						inner, cancelInner := context.WithCancelCause(ctx)
						defer cancelInner(nil)
						child := r.Subshell()
						defer child.Reset()
						if err := child.Run(inner, asyncSignalFile(t, "hold &")); err != nil {
							return err
						}
						<-entered
						cancelInner(cause)
						return nil
					}
					entered <- ctx
					<-ctx.Done()
					finished <- context.Cause(ctx)
					return ctx.Err()
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Reset()
			if derived {
				if err := r.Run(caller, asyncSignalFile(t, "nested")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := r.Run(caller, asyncSignalFile(t, "hold &")); err != nil {
					t.Fatal(err)
				}
				childCtx := <-entered
				if got, ok := childCtx.Deadline(); !ok || !got.Equal(deadline) {
					t.Fatalf("caller deadline lost: %v/%v", got, ok)
				}
				select {
				case err := <-finished:
					t.Fatalf("normal owner return canceled background: %v", err)
				default:
				}
				cancel(cause)
			}
			select {
			case got := <-finished:
				if got != cause {
					t.Fatalf("caller cancellation cause = %v, want %v", got, cause)
				}
			case <-time.After(time.Second):
				t.Fatal("background did not inherit caller cancellation")
			}
		})
	}
}

func TestAsyncSelfSignalCallerDeadlineErrors(t *testing.T) {
	for _, ownCancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(ownCancelFirst), func(t *testing.T) {
			cause := errors.New("caller deadline")
			caller, cancel := context.WithTimeoutCause(t.Context(), time.Second, cause)
			defer cancel()
			entered := make(chan context.Context, 1)
			r, err := New(ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					entered <- ctx
					<-ctx.Done()
					return ctx.Err()
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Reset()
			if err := r.Run(caller, asyncSignalFile(t, "hold &")); err != nil {
				t.Fatal(err)
			}
			child := <-entered
			descendant, cancelDescendant := context.WithCancel(child)
			defer cancelDescendant()
			timed, cancelTimed := context.WithTimeout(child, time.Minute)
			defer cancelTimed()
			if ownCancelFirst {
				r.Reset()
			}
			<-child.Done()
			wantErr, wantCause := error(context.DeadlineExceeded), error(cause)
			if ownCancelFirst {
				wantErr, wantCause = context.Canceled, context.Canceled
			}
			for _, ctx := range []context.Context{child, descendant, timed} {
				<-ctx.Done()
				if ctx.Err() != wantErr || context.Cause(ctx) != wantCause {
					t.Fatalf("child/descendant cancellation = %v/%v, want %v/%v", ctx.Err(), context.Cause(ctx), wantErr, wantCause)
				}
			}
			<-caller.Done()
			for _, ctx := range []context.Context{child, descendant, timed} {
				if ctx.Err() != wantErr || context.Cause(ctx) != wantCause {
					t.Fatal("a later caller deadline changed the first cancellation")
				}
			}
		})
	}
}
