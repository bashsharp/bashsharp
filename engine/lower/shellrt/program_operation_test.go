package shellrt_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec"
)

func TestReceiveFailureDefersCancellationAndRanksTask(t *testing.T) {
	var group sync.WaitGroup
	for range 12 {
		p, out, diagnostic := newProgram(t)
		group.Go(func() {
			err := p.Run(func(p *shellrt.Program) {
				channel := shellrt.MustChannel(shellrt.MakeChannel[int](p.Channels, 0))
				p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
					return p.Child(ctx, child).RunSourceTask(func() {
						child.SetStatus(7)
					})
				})
				_, _, receiveErr := shellrt.Receive(p.Context, p.Session, p.Channels, channel)
				derived, enterErr := p.Enter(shellrt.Site{Name: "ordinary"}, false)
				if enterErr != nil {
					panic(enterErr)
				}
				derived.ReceiveFailure(receiveErr)
				if diagnostic.String() != "" {
					t.Error("cancellation was printed before task arbitration")
				}
				p.Println("")
			})
			if got := shellrt.SourceFailure(err); got == nil || got.Error() != "bash++: task failed: exit status 7" || shellrt.ExitCode(got) != 7 || out.String() != "\n" || diagnostic.String() != "" {
				t.Errorf("err=%v out=%q diagnostics=%q", err, out.String(), diagnostic.String())
			}
			if p.Session.Active() != 0 {
				t.Error("task remains active after Run")
			}
		})
	}
	group.Wait()
}

func TestReceiveFailureReportsChannelErrorOnce(t *testing.T) {
	p, out, diagnostic := newProgram(t)
	err := p.Run(func(p *shellrt.Program) {
		p.ReceiveFailure(shellrt.ErrForeignChannel)
		p.Println("continued")
	})
	if err != nil || p.Status() != 2 || out.String() != "continued\n" || diagnostic.String() != shellrt.ErrForeignChannel.Error()+"\n" {
		t.Fatalf("err=%v status=%d stdout=%q stderr=%q", err, p.Status(), out.String(), diagnostic.String())
	}
}

func TestReceiveFailureKeepsExternalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, _, diagnostic := newProgram(t, shellrt.WithContext(ctx))
	err := p.Run(func(p *shellrt.Program) {
		cancel()
		p.ReceiveFailure(ctx.Err())
	})
	if !errors.Is(err, context.Canceled) || diagnostic.String() != "" {
		t.Fatalf("err=%v stderr=%q", err, diagnostic.String())
	}
}

func TestClosedChannelTaskKeepsStatusAndCause(t *testing.T) {
	p, _, diagnostic := newProgram(t)
	var cause error
	err := p.Run(func(p *shellrt.Program) {
		channel := shellrt.MustChannel(shellrt.MakeChannel[int](p.Channels, 0))
		shellrt.MustChannelOperation(shellrt.CloseChannel(p.Channels, channel))
		p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
			return p.Child(ctx, child).RunSourceTask(func() {
				cause = shellrt.Send(ctx, child, p.Channels, channel, 1)
				shellrt.MustChannelOperation(cause)
			})
		})
	})
	if cause == nil || !errors.Is(err, cause) || shellrt.ExitCode(err) != 2 || shellrt.SourceFailure(err).Error() != "bash++: task failed: exit status 2" || diagnostic.String() != "bash++: send on closed channel\n" {
		t.Fatalf("err=%v cause=%v status=%d stderr=%q", err, cause, shellrt.ExitCode(err), diagnostic.String())
	}
}

func TestShellRegionReturnsOriginalFailure(t *testing.T) {
	for _, mode := range []string{"provider", "cancel", "task"} {
		t.Run(mode, func(t *testing.T) {
			providerError := errors.New("provider unavailable")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls, deferred := 0, false
			factory := shellexec.New(shellexec.BashPP(), shellexec.RunnerOptions(interp.ExecHandler(func(ctx context.Context, args []string) error {
				calls++
				if len(args) != 1 || args[0] != "provider" || !shellrt.Agentic(ctx) || !interp.HandlerCtx(ctx).Agentic {
					return fmt.Errorf("bad request: %q", args)
				}
				if mode == "cancel" {
					cancel()
					<-ctx.Done()
					return ctx.Err()
				}
				return providerError
			})))
			p, _, diagnostic := newProgram(t, shellrt.WithContext(ctx), shellrt.WithShellFactory(factory))
			err := p.Run(func(p *shellrt.Program) {
				body := func(p *shellrt.Program) {
					defer func() { deferred = true }()
					p.Block().ShellRegion("provider")
					t.Error("backend failure continued source body")
				}
				if mode == "task" {
					task := p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
						childProgram := p.Child(ctx, child)
						return childProgram.RunSourceTask(func() { body(childProgram) })
					})
					task.Wait()
					return
				}
				body(p)
			})
			want := providerError
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls != 1 || !deferred {
				t.Fatalf("err=%v want=%v calls=%d deferred=%v", err, want, calls, deferred)
			}
			wantDiagnostic := ""
			if mode == "task" {
				wantDiagnostic = "provider unavailable\n"
			}
			if diagnostic.String() != wantDiagnostic {
				t.Fatalf("stderr=%q want=%q", diagnostic.String(), wantDiagnostic)
			}
		})
	}
}

func TestSourceRecoverPreservesShellFailure(t *testing.T) {
	failure := errors.New("backend failure")
	for _, sourcePanic := range []bool{false, true} {
		p, _, diagnostic := newProgram(t)
		var caught any
		err := p.Run(func(p *shellrt.Program) {
			defer func() { caught = shellrt.PreserveAbort(recover()) }()
			if sourcePanic {
				panic("recoverable")
			}
			panic(shellrt.ShellAbort{Err: failure})
		})
		if sourcePanic {
			if err != nil || caught != "recoverable" {
				t.Fatalf("source panic changed: err=%v caught=%v", err, caught)
			}
		} else if !errors.Is(err, failure) || caught != nil || diagnostic.String() != "" {
			t.Fatalf("backend failure consumed: err=%v caught=%v stderr=%q", err, caught, diagnostic.String())
		}
	}
}
