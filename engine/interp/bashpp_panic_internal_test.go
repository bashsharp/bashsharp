// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPDeferredSelectorDispatch proves a deferred selector reaches the
// import evaluator, like a direct one, rather than being run as a shell
// command named after the last element of the selector — and that it carries
// the values captured when the defer ran.
func TestBashPPDeferredSelectorDispatch(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	r := newInjectedBashPPRunner(t, eval)
	src := "import \"fmt\"\nfunc f() {\n v=1\n defer fmt.Println($v)\n v=2\n}\nf()\n"
	if err := r.Run(context.Background(), parseBashPPInternal(t, src)); err != nil {
		t.Fatal(err)
	}
	if len(eval.calls) != 1 {
		t.Fatalf("evaluator calls: %d, want 1", len(eval.calls))
	}
	call := eval.calls[0]
	if got := strings.Join(call.Selector, "."); got != "fmt.Println" {
		t.Fatalf("selector %q", got)
	}
	// Go fixes a deferred call's arguments where the defer ran, so the value
	// handed over is 1, not the 2 the variable holds by the time the frame
	// unwinds.
	if len(call.Args) != 1 || call.Args[0] != `"1"` {
		t.Fatalf("args %q", call.Args)
	}
}

// TestBashPPDeferredSelectorRunsWhilePanicking proves a cleanup that reaches
// an imported package still runs when the frame is being abandoned rather than
// returned from.
func TestBashPPDeferredSelectorRunsWhilePanicking(t *testing.T) {
	eval := &recordingBashPPEval{resolved: map[string]string{"fmt": "fmt"}}
	r := newInjectedBashPPRunner(t, eval)
	src := "import \"fmt\"\nfunc f() {\n defer fmt.Println(cleanup)\n panic(boom)\n}\nf()\n"
	if err := r.Run(context.Background(), parseBashPPInternal(t, src)); err == nil {
		t.Fatal("an unrecovered panic must report a non-zero status")
	}
	if len(eval.calls) != 1 {
		t.Fatalf("evaluator calls: %d, want 1", len(eval.calls))
	}
	if got := eval.calls[0].Args; len(got) != 1 || got[0] != `"cleanup"` {
		t.Fatalf("args %q", got)
	}
}

// TestBashPPPanicRestoresFrameState is the panic-safety half of the frame
// refactor: whichever way a frame leaves — returning, recovering, or being
// abandoned by a panic that terminates the shell — the caller's execution
// context is exactly what it was.
//
// It reads runner state directly because that is the invariant under test. A
// leak here does not show up as a wrong script output straight away; it shows
// up later, in an unrelated command that inherits a frame's positional
// parameters or a stale call stack.
func TestBashPPPanicRestoresFrameState(t *testing.T) {
	scripts := map[string]string{
		"unrecovered":     "func f() {\n panic(boom)\n}\nf()\n",
		"recovered":       "func f() {\n defer func() {\n  recover()\n }()\n panic(boom)\n}\nf()\n",
		"nested":          "func inner() {\n panic(deep)\n}\nfunc outer() {\n defer func() {\n  recover()\n }()\n inner()\n}\nouter()\n",
		"cleanup panics":  "func f() {\n defer func() {\n  panic(second)\n }()\n panic(first)\n}\nf()\n",
		"cleanup exits":   "func f() {\n defer func() {\n  exit 3\n }()\n panic(boom)\n}\nf()\n",
		"ordinary return": "func f() (n int) {\n defer func() {\n  n=2\n }()\n return 1\n}\nx := f()\n",
	}
	for name, src := range scripts {
		t.Run(name, func(t *testing.T) {
			r, err := New(StdIO(nil, io.Discard, io.Discard), Lang(syntax.LangBashPP))
			if err != nil {
				t.Fatal(err)
			}
			r.Reset()
			params := slices.Clone(r.Params)
			env, scope := r.writeEnv, r.bashPPScope
			_ = r.Run(context.Background(), parseBashPPInternal(t, src))
			if got := len(r.callStack); got != 0 {
				t.Errorf("call stack depth %d, want 0", got)
			}
			if got := len(r.bashPPDeferStack); got != 0 {
				t.Errorf("defer stack depth %d, want 0", got)
			}
			if r.bashPPFuncActive != 0 {
				t.Errorf("funcActive %d, want 0", r.bashPPFuncActive)
			}
			if r.bashPPDeferDepth != 0 {
				t.Errorf("deferDepth %d, want 0", r.bashPPDeferDepth)
			}
			if r.bashPPPanic.active {
				t.Errorf("panic still active after the script ended")
			}
			if r.inFunc {
				t.Errorf("still inside a function frame")
			}
			if !slices.Equal(r.Params, params) {
				t.Errorf("params %q, want %q", r.Params, params)
			}
			if r.writeEnv != env {
				t.Errorf("write environment not restored")
			}
			if r.bashPPScope != scope {
				t.Errorf("typed scope not restored")
			}
		})
	}
}
