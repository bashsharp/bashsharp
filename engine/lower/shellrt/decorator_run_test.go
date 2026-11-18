// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package shellrt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec"
)

// TestDecoratorCallRun pins the compiled Call.Run: a shell check evaluated in
// the program's session with the call's Args as $1..$n and bound vars, in a
// subshell — it reads session variables, its assignments never leak, its
// commands go through the session's own handler chain with the ctx the
// decorator passes, and the call's status is untouched.
func TestDecoratorCallRun(t *testing.T) {
	previous := shellrt.Decorators
	defer func() { shellrt.Decorators = previous }()
	type key struct{}
	var dispatched []string
	var sawCtx bool
	factory := shellexec.New(shellexec.RunnerOptions(interp.ExecHandler(func(ctx context.Context, args []string) error {
		dispatched = append(dispatched, strings.Join(args, " "))
		if ctx.Value(key{}) == "capped" {
			sawCtx = true
		}
		return nil
	})))
	p, out, diagnostic := newProgram(t, shellrt.WithShellFactory(factory))
	p.ShellRegion("outer=visible")
	var statuses []int
	shellrt.Decorators = map[string]shellrt.DecoratorFunc{
		"check": func(ctx context.Context, c *shellrt.Call, args []shellrt.DecoratorArg) error {
			capped := context.WithValue(ctx, key{}, "capped")
			for _, a := range args {
				statuses = append(statuses, c.Run(capped, a.Value, map[string]string{"BOUND": "it's"}))
			}
			c.Next(ctx)
			statuses = append(statuses, c.Run(ctx, `test "$STATUS" = 4`, map[string]string{"STATUS": "4"}))
			return nil
		},
	}
	call := &shellrt.Call{Name: "work", Args: []any{"a", "b c"}}
	rungs := []shellrt.Decorator{{Name: "check", Args: func(*shellrt.Program) []shellrt.DecoratorArg {
		return []shellrt.DecoratorArg{
			{Value: `test "$1" = a && test "$2" = "b c" && test "$#" = 2`},
			{Value: `test "$outer" = visible && test "$BOUND" = "it's"`},
			{Value: `leak=1; external-probe; false`},
			{Value: `if (`},
		}
	}}}
	if !p.Decorate(call, rungs, func(region *shellrt.Program) error {
		region.ShellRegion("echo body; exit_status_marker=1")
		region.SetStatus(4)
		return nil
	}) {
		t.Fatalf("chain failed: %s", diagnostic)
	}
	if call.Status != 4 {
		t.Fatalf("call status = %d, want the body's 4", call.Status)
	}
	if want := []int{0, 0, 1, 2, 0}; !equalInts(statuses, want) {
		t.Fatalf("check statuses = %v, want %v (stderr %s)", statuses, want, diagnostic)
	}
	p.ShellRegion(`echo "leak=${leak-unset} BOUND=${BOUND-unset}"`)
	if got := out.String(); got != "body\nleak=unset BOUND=unset\n" {
		t.Fatalf("stdout = %q", got)
	}
	if len(dispatched) != 1 || dispatched[0] != "external-probe" || !sawCtx {
		t.Fatalf("check commands did not go through the session handler with the decorator's ctx: %v ctx=%v", dispatched, sawCtx)
	}
	var none *shellrt.Call
	if none.Run(context.Background(), "true", nil) != 1 || (&shellrt.Call{}).Run(context.Background(), "true", nil) != 1 {
		t.Fatal("a Call outside a chain must not run anything")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
