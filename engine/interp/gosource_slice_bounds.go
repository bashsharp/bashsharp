package interp

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A checked Go slice expression faults at runtime, so its bounds failure
// participates in panic/recover instead of becoming a shell diagnostic.
func (r *Runner) goSourceSliceBoundsPanic(expr *syntax.BashPPSliceExpr, low, high, max bashPPCollectionIndexValue, limit, capacity int, slice bool) error {
	unit := "length"
	if slice {
		unit = "capacity"
	}
	var message string
	if expr.SecondColon.IsValid() {
		switch {
		case max.less(bashPPCollectionIndexInt(0)):
			message = fmt.Sprintf("[::%s]", max.text)
		case max.greaterThan(capacity):
			message = fmt.Sprintf("[::%s] with %s %d", max.text, unit, capacity)
		case high.less(bashPPCollectionIndexInt(0)):
			message = fmt.Sprintf("[:%s:]", high.text)
		case max.less(high):
			message = fmt.Sprintf("[:%s:%s]", high.text, max.text)
		case low.less(bashPPCollectionIndexInt(0)):
			message = fmt.Sprintf("[%s::]", low.text)
		default:
			message = fmt.Sprintf("[%s:%s:]", low.text, high.text)
		}
	} else {
		switch {
		case high.less(bashPPCollectionIndexInt(0)):
			message = fmt.Sprintf("[:%s]", high.text)
		case high.greaterThan(limit):
			message = fmt.Sprintf("[:%s] with %s %d", high.text, unit, limit)
		case low.less(bashPPCollectionIndexInt(0)):
			message = fmt.Sprintf("[%s:]", low.text)
		default:
			message = fmt.Sprintf("[%s:%s]", low.text, high.text)
		}
	}
	saved := r.curStmtPos
	r.curStmtPos = expr.Lbrack
	r.goSourceRuntimePanic("runtime error: slice bounds out of range " + message)
	r.curStmtPos = saved
	return errBashPPScalarInterrupted
}
