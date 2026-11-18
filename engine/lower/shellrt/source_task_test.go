package shellrt_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestSourceTaskFailureReapsBlockedDescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := shellrt.NewProgram(shellrt.WithContext(ctx), shellrt.WithStdio(nil, io.Discard, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Session.Close()
	err = p.RunSourceTask(func() {
		p.Session.Go(func(ctx context.Context, child *shellrt.Session) error { child.Arm(); <-ctx.Done(); return ctx.Err() })
		panic(p.PushPanic("boom"))
	})
	var status interface{ ExitStatus() int }
	if !errors.As(err, &status) || status.ExitStatus() != 2 {
		t.Fatalf("failure=%v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("descendant waited for external timeout")
	}
	if p.Session.Active() != 0 {
		t.Fatal("descendant remains active")
	}
}
