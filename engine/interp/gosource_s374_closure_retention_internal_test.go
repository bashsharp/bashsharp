//go:build full

package interp

import (
	"runtime"
	"strings"
	"testing"
	"weak"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Keeping a closure must not keep unrelated locals from its creating frame.
// A long-lived callback in a loop otherwise retains each dead buffer even
// though the source program keeps only a shared counter live.
func TestS374ClosureCaptureRetainsOnlyNamedBindings(t *testing.T) {
	p, err := gosource.Parse(strings.NewReader(`package main
var live int
func main(){ _ = func() int {return live} }
`), "closure.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var lit *syntax.BashPPFuncLit
	syntax.Walk(p.File, func(n syntax.Node) bool {
		if x, ok := n.(*syntax.BashPPFuncLit); ok {
			lit = x
		}
		return true
	})
	if lit == nil {
		t.Fatal("missing closure")
	}
	r := &Runner{bashPPGoSource: true, bashPPFuncActive: 1}
	live := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: "42"}}
	var refs []weak.Pointer[bashPPCell]
	for batch := 0; batch < 2; batch++ {
		for i := 0; i < 8; i++ {
			refs = append(refs, s374CaptureWithDeadBuffer(r, lit, live))
		}
		runtime.GC()
		retained := 0
		for _, ref := range refs {
			if ref.Value() != nil {
				retained++
			}
		}
		t.Logf("closures=%d dead buffers retained=%d (%d KiB)", len(r.bashPPClosures), retained, retained*64)
		if retained != 0 {
			t.Fatalf("closures retain %d unrelated locals", retained)
		}
		for _, fn := range r.bashPPClosures {
			if fn.scope.lookup("live") != live {
				t.Fatal("captured binding lost its identity")
			}
		}
	}
	runtime.KeepAlive(r)
}

//go:noinline
func s374CaptureWithDeadBuffer(r *Runner, lit *syntax.BashPPFuncLit, live *bashPPCell) weak.Pointer[bashPPCell] {
	dead := &bashPPCell{vr: expand.NewObject(make([]byte, 64<<10))}
	r.bashPPScope = newBashPPScope(nil)
	r.bashPPScope.entries["dead"] = dead
	r.bashPPScope.entries["live"] = live
	ref := weak.Make(dead)
	r.bashPPMakeClosure(lit)
	r.bashPPScope = nil
	return ref
}
