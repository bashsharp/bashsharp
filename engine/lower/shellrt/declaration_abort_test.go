package shellrt_test

import (
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"testing"
)

func TestDeclarationAbortRootBoundary(t *testing.T) {
	p := new(shellrt.Program)
	completed := false
	p.RootStatement(func() { panic(shellrt.DeclarationAbort{}) })
	p.RootStatement(func() { completed = true })
	if !completed {
		t.Fatal("following root statement did not execute")
	}
	for _, payload := range []any{shellrt.ShellExit{}, shellrt.ShellAbort{Err: nil}, "source panic"} {
		func() {
			defer func() {
				if got := recover(); got != payload {
					t.Fatalf("control transfer changed: %#v", got)
				}
			}()
			p.RootStatement(func() { panic(payload) })
		}()
	}
	var got any
	func() { defer func() { got = recover() }(); shellrt.PreserveAbort(shellrt.DeclarationAbort{}) }()
	if _, ok := got.(shellrt.DeclarationAbort); !ok {
		t.Fatalf("source recover swallowed declaration refusal: %#v", got)
	}
}
