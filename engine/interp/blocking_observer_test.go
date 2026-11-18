package interp_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBlockingObserverImmediateAndExternal(t *testing.T) {
	for _, cancelAtBoundary := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		notifications, calls := 0, 0
		ctx = interp.WithBlockingObserver(ctx, func() {
			notifications++
			if cancelAtBoundary {
				cancel()
			}
		})
		runner, err := interp.New(interp.StdIO(nil, io.Discard, io.Discard), interp.ExecHandler(func(context.Context, []string) error {
			calls++
			if notifications != 1 {
				t.Error("provider entered before blocking notification")
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		file, err := syntax.NewParser().Parse(strings.NewReader("false; echo immediate; provider"), "observer.sh")
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range file.Stmts[:2] {
			runner.Run(ctx, stmt)
			if notifications != 0 {
				t.Fatal("immediate builtin released launch")
			}
		}
		err = runner.Run(ctx, file.Stmts[2])
		if cancelAtBoundary {
			if calls != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		} else if calls != 1 || err != nil {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		if notifications != 1 {
			t.Fatalf("notifications=%d", notifications)
		}
	}
}
