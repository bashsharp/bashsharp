// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"context"
	"os"
	"testing"
)

func TestExecCancelSignal(t *testing.T) {
	t.Parallel()
	if got := execCancelSignal(context.Background()); got != os.Interrupt {
		t.Fatalf("ordinary cancellation signal = %v, want interrupt", got)
	}

	for _, name := range []string{"INT", "TERM", "QUIT"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sig, ok := signalByName(name)
			if !ok {
				t.Skipf("%s is unavailable on this platform", name)
			}
			bg := &bgProc{}
			bg.killedSignal.Store(int32(sigNum(sig)))
			ctx := context.WithValue(context.Background(), bgProcCtxKey{}, bg)
			if got, want := execCancelSignal(ctx), signalForOS(sig); got != want {
				t.Fatalf("carrier cancellation signal = %v, want %v", got, want)
			}
		})
	}
}
