// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"testing"
	"time"
)

func TestSprint198MixedSemantics(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"literal-and-projection", "SHELL_VALUE=expanded\nx := \"$SHELL_VALUE\"\necho \"$x\" \"$SHELL_VALUE\"\nx = \"next\"\necho \"$x\"\n", "$SHELL_VALUE expanded\nnext\n"},
		{"keyword-identifier", "done := 1\nthen := 0\nthen = 2\necho \"$done:$then\"\n", "1:2\n"},
		{"goto", "x := 1\ngoto end\nx = 2\nend: echo \"$x\"\n", "1\n"},
		{"block-scope", "x := 1\n{ x := 2; echo \"$x\"; }\necho \"$x\"\n", "2\n1\n"},
		// Mixed tasks currently copy Go cells. This records the limitation,
		// unlike Go source mode where lexical captures share cells. Shell state
		// remains isolated in both directions; channels synchronize observations.
		{"mixed-task-snapshot-limitation", `
x := 1
SHELL_STATE=parent
parent_dir=$PWD
ready := make(chan int)
release := make(chan int)
finished := make(chan int)
func worker() {
 x = 2
 SHELL_STATE=child
 cd /
 ready <- 1
 <-release
 echo "child:$x:$SHELL_STATE:$PWD"
 finished <- 1
}
go worker()
<-ready
echo "parent:$x:$SHELL_STATE"
[ "$PWD" = "$parent_dir" ] || echo wrong-directory
x = 3
release <- 1
<-finished
`, "parent:1:parent\nchild:2:child:/\n"},

		{"return-context", "f() { return 7; }\nf\necho $?\nfunc g() int { return 9; }\nx := g()\necho \"$x\"\n", "7\n9\n"},
	} {
		t.Run(tc.name, func(t *testing.T) { wantOutput(t, bashPPRun(t, tc.src), tc.want) })
	}
}

func TestSprint198GotoCancellation(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = r.Run(ctx, bashPPParse(t, "again: goto again"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSprint198ShellOptionsRuntime(t *testing.T) {
	wantOutput(t, bashPPRun(t, "set -- first second\necho \"$#:$1\"\nprintf -- '%s\\n' ok\n"), "2:first\nok\n")
	wantOutput(t, bashPPRun(t, "func f() { x := 2; x --; x ++; echo \"$x\"; }\nf()\n"), "2\n")
}
