package shellrt_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec"
)

type legacyShell struct{ shellrt.ShellRunner }

func (s legacyShell) Clone(streams shellrt.Stdio) (shellrt.ShellRunner, error) {
	cloned, err := s.ShellRunner.Clone(streams)
	return legacyShell{cloned}, err
}

func legacyShellFactory(state shellrt.State, streams shellrt.Stdio) (shellrt.ShellRunner, error) {
	runner, err := shellexec.New()(state, streams)
	return legacyShell{runner}, err
}

func TestShellAdmissionWaitsForImmediateFailure(t *testing.T) {
	for range 100 {
		p, _, diagnostic := newProgram(t, shellrt.WithShellFactory(shellexec.New()))
		var escaped atomic.Bool
		err := p.Run(func(p *shellrt.Program) {
			p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
				program := p.Child(ctx, child)
				return program.RunSourceTask(func() { program.ShellRegion("false") })
			})
			p.Session.Go(func(context.Context, *shellrt.Session) error {
				escaped.Store(true)
				return nil
			})
		})
		if err == nil || shellrt.ExitCode(err) != 1 || escaped.Load() || diagnostic.String() != "" {
			t.Fatalf("err=%v escaped=%v stderr=%q", err, escaped.Load(), diagnostic.String())
		}
	}
}

func TestShellAdmissionReleasesAtProviderOrExitTrap(t *testing.T) {
	for _, inTrap := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		entered, release := make(chan struct{}), make(chan struct{})
		factory := shellexec.New(shellexec.RunnerOptions(interp.ExecHandler(func(ctx context.Context, args []string) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})))
		session, _ := newSession(t, shellrt.WithContext(ctx), shellrt.WithShellFactory(factory))
		source := "provider"
		if inTrap {
			source = "trap provider EXIT"
		}
		task := session.Go(func(ctx context.Context, child *shellrt.Session) error {
			return child.Shell(ctx, source)
		})
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("provider did not start")
		}
		close(release)
		if err := task.Wait(); err != nil || ctx.Err() != nil {
			t.Fatalf("trap=%v err=%v context=%v", inTrap, err, ctx.Err())
		}
		cancel()
	}
}
