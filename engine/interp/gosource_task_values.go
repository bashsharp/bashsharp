package interp

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The native variable's type supplies nil's assignment context. This creates
// no object and carries no handle; normal tuple validation still precedes all
// target writes.
func (r *Runner) goSourceNativeNilCandidate(target *bashPPCell, expr syntax.BashPPExpr) *bashPPCell {
	if !r.bashPPGoSource || target == nil || !goSourceNilLiteral(expr) {
		return nil
	}
	v, ok := target.vr.Obj.(*bashPPBridgeValue)
	if !ok || v == nil {
		return nil
	}
	// A slice the dependency returned is nilable too; its wire spelling is
	// "[]T", which cannot be an array — those spell their length as "[N]T"
	// and keep refusing nil on the paths below.
	if !strings.HasPrefix(v.Type, "*") && !strings.HasPrefix(v.Type, "chan ") && !strings.HasPrefix(v.Type, "chan<-") && !strings.HasPrefix(v.Type, "<-chan ") && !strings.HasPrefix(v.Type, "[]") {
		return nil
	}
	wireType := v.Type
	// Display names such as *big.Int need not be source import aliases. Bind
	// the canonical native package identity to this session's import table.
	if strings.HasPrefix(v.NativeType, "*") {
		name := strings.TrimLeft(v.NativeType, "*")
		stars := v.NativeType[:len(v.NativeType)-len(name)]
		for alias, path := range r.bashPPImports {
			if strings.HasPrefix(name, path+".") && syntax.BashPPValidIdent(alias) {
				wireType = stars + alias + strings.TrimPrefix(name, path)
				break
			}
		}
	}
	return goSourceNativeValueCell(bashPPBridgeValue{Kind: "nil", Type: wireType, NativeType: v.NativeType, Session: v.Session})
}

// goSourceCapturedHandleCell admits a variable holding an authenticated native
// object or a channel in this task group. Object identity alone is insufficient:
// a synchronized assignment must change the value seen by the captured cell.
// It runs only before that cell enters the inherited capture-identity memo.
func (r *Runner) goSourceCapturedHandleCell(cell *bashPPCell) (bool, bool) {
	if cell.channel != nil {
		if cell.channelOwner == nil || cell.channelOwner != r.bashPPConcurrent || r.bashPPChanBoundary {
			r.exit.fatal(fmt.Errorf("gosource: captured channel belongs to another task group"))
			return false, true
		}
		return true, true
	}
	if cell.pointer && cell.pointerValue != nil {
		// A pointer to a resident sync object (new(sync.Mutex), &wg of a
		// parameter) must keep naming the one host-only object; copying the
		// pointer cell would leave the task without the variable.
		if pointee, _, _, err := cell.pointerValue.read(); err == nil {
			if b, ok := pointee.(*bashPPBridgeValue); ok && b != nil && residentSyncValue(*b) != nil {
				return true, true
			}
		}
	}
	v, ok := cell.vr.Obj.(*bashPPBridgeValue)
	if !ok || v == nil {
		return false, false
	}
	if v.residentSync != nil {
		return true, true
	}
	if v.Kind == "nil" {
		// A typed nil carries no object authority; future non-nil values are
		// still checked by every dependency operation.
		return true, true
	}
	if err := bashPPNativeScopeOf(r).checkHandle(*v); err != nil {
		r.exit.fatal(fmt.Errorf("gosource: captured native variable: %w", err))
		return false, true
	}
	session := r.bashPPTools.bridge
	if session == nil {
		r.exit.fatal(fmt.Errorf("gosource: captured native variable: %w", errBashPPStaleNativeHandle))
		return false, true
	}
	// Session identity is immutable after startup. Acquire its publication
	// lock before inspecting it; do not hold that lock while any task runs.
	session.start.Lock()
	id, done := session.id, session.done
	session.start.Unlock()
	if done != nil {
		select {
		case <-done:
			r.exit.fatal(fmt.Errorf("gosource: captured native variable: %w", errBashPPStaleNativeHandle))
			return false, true
		default:
		}
	}
	if id == "" || v.Session != id {
		r.exit.fatal(fmt.Errorf("gosource: captured native variable belongs to another dependency session"))
		return false, true
	}
	return true, true
}
