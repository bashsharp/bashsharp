// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

// TestBashPPDecoratorCallRun pins Call.Run: a native decorator evaluates a
// shell check in the decorated call's frame — the call's Args as $1..$n,
// the frame's variables (a shell function's dynamic scope, a typed one's
// captured scope), bound vars — in a nested scope whose assignments do not
// leak, and returns the check's status without disturbing the call's own.
func TestBashPPDecoratorCallRun(t *testing.T) {
	var statuses []int
	natives := map[string]interp.DecoratorFunc{
		"check": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			for _, a := range args {
				statuses = append(statuses, c.Run(ctx, a.Value, map[string]string{"BOUND": "yes"}))
			}
			c.Next(ctx)
			statuses = append(statuses, c.Run(ctx, `test "$STATUS" = 4`, map[string]string{"STATUS": "4"}))
			return nil
		},
	}
	out, stderr, err := runDecorated(t, `outer=visible
@check('test "$1" = a && test "$2" = b', 'test "$outer" = visible', 'test "$BOUND" = yes', 'leak=1; false', 'echo "check sees $#: $*"')
function f() { echo "body $1 $2"; return 4; }
f a b
echo "status=$? leak=${leak-unset} BOUND=${BOUND-unset}"
@check('test "$1" = 7')
func g(n int) int { return $n }
v := g(7)
echo "v=$v"
`, interp.Decorators(natives))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "check sees 2: a b\nbody a b\nstatus=4 leak=unset BOUND=unset\nv=7\n"))
	qt.Assert(t, qt.DeepEquals(statuses, []int{0, 0, 0, 1, 0, 0, 0, 0}))
}

func TestBashPPDecoratorCallRunFaults(t *testing.T) {
	natives := map[string]interp.DecoratorFunc{
		"check": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			if st := c.Run(ctx, "if (", nil); st != 2 {
				t.Errorf("parse fault status = %d, want 2", st)
			}
			c.Next(ctx)
			return nil
		},
	}
	out, stderr, err := runDecorated(t, "@check()\nfunction f() { echo body; }\nf\n", interp.Decorators(natives))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "body\n"))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "f: ")))
	// A nil receiver or a Call outside a chain cannot run anything.
	var none *interp.Call
	qt.Assert(t, qt.Equals(none.Run(context.Background(), "true", nil), 1))
	qt.Assert(t, qt.Equals((&interp.Call{}).Run(context.Background(), "true", nil), 1))
}

// TestBashPPDecoratorInSubshells pins that a native decorator resolves in
// every subshell form — ( … ), a pipeline stage, $( … ) — not only at top
// level: the registry is host wiring the subshell must inherit.
func TestBashPPDecoratorInSubshells(t *testing.T) {
	natives := map[string]interp.DecoratorFunc{
		"tag": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			c.Next(ctx)
			return nil
		},
	}
	out, stderr, err := runDecorated(t, `@tag()
function f() { echo ran; }
f
( f )
f | cat
x=$(f); echo "$x"
`, interp.Decorators(natives))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "ran\nran\nran\nran\n"))
}
