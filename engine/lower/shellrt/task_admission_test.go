package shellrt_test

import (
	"context"
	"io"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestEmptyJoinDoesNotReleaseLaterTask(t *testing.T) {
	previous := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previous)
	for range 500 {
		session, err := shellrt.NewSession(shellrt.WithStdio(nil, io.Discard, io.Discard))
		if err != nil {
			t.Fatal(err)
		}
		first := session.Go(func(ctx context.Context, child *shellrt.Session) error {
			// Joining zero descendants is immediate. The failure remains part
			// of this launch, so a later task cannot start before it commits.
			if err := child.Join(); err != nil {
				return err
			}
			return &shellrt.ExitError{Status: 7}
		})
		var escaped atomic.Bool
		session.Go(func(context.Context, *shellrt.Session) error {
			escaped.Store(true)
			return nil
		})
		first.Wait()
		err = session.Close()
		if escaped.Load() || shellrt.ExitCode(err) != 7 || session.Active() != 0 {
			t.Fatalf("escaped=%v err=%v active=%d", escaped.Load(), err, session.Active())
		}
	}
}
