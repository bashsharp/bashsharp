package interp_test

import (
	"context"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
	"time"
)

// TestBgPidTimeouts exercises constructs that previously caused a 250ms timeout
// when evaluating $! without a job carrier, because pidReady was left open.
func TestBgPidTimeouts(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{"block", `f() { sleep 1; }; { f; } & pid=$!`},
		{"assignment", `x=1 & pid=$!`},
		{"pipeline_assignment", `sleep 1 | x=1 & pid=$!`},
		{"pipeline_empty_block", `sleep 1 | { :; } & pid=$!`},
		{"subshell_func", `f() { sleep 1; }; ( f ) & pid=$!`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()

			p, err := syntax.NewParser().Parse(strings.NewReader(tc.script), "")
			if err != nil {
				t.Fatal(err)
			}
			r, _ := interp.New()
			var out strings.Builder
			interp.StdIO(nil, &out, &out)(r)
			err = r.Run(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}

			elapsed := time.Since(start)
			// A timeout would cause elapsed > 250ms.
			// We check against 200ms to be safe.
			if elapsed > 200*time.Millisecond {
				t.Fatalf("took %v, expected <200ms (likely hit pidReady timeout)", elapsed)
			}
		})
	}
}
