// Copyright (c) 2026, the bash++ authors.
// See LICENSE for licensing information.

package interp

// bashPPPrepareInterfaceAssignment applies Go's implicit boxing when a value
// which already exists as a typed cell is assigned to an interface target.
// Receive and select paths have an evaluated cell rather than a source
// expression, so they cannot use bashPPInterfaceAssignCandidate directly.
func (r *Runner) bashPPPrepareInterfaceAssignment(target, candidate *bashPPCell) {
	if target == nil || candidate == nil || candidate.interfaceValue != nil || target.declType == nil {
		return
	}
	if _, ok := r.bashPPInterfaceType(target.declType); !ok {
		return
	}
	if err := r.bashPPBindInterfaceParam(candidate, target.declType); err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if candidate.interfaceValue != nil {
		candidate.declType = target.declType
	}
}
