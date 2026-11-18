package interp

// Sprint: #247; Story: #673; Story-ID: f24307569417

import (
	"runtime"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceGoschedCall answers runtime.Gosched in the interpreter's own
// process. A program's goroutines are interpreter goroutines, so the yield
// they ask for is a yield of this scheduler; forwarding it to the dependency
// process only yielded the helper's request goroutine, at the cost of a full
// bridge round trip per call (test/gcgort.go makes thousands of them).
func (r *Runner) goSourceGoschedCall(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	if call == nil || len(call.ArgExprs) != 0 || len(call.Args) != 0 || call.Ellipsis.IsValid() {
		return nil, false, nil
	}
	name, ok := r.goSourceStackSelector(call)
	if !ok || name != "runtime.Gosched" {
		return nil, false, nil
	}
	runtime.Gosched()
	return nil, true, nil
}
