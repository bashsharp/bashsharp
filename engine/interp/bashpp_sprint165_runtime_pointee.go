// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"github.com/bashsharp/bashsharp/engine/expand"
)

// goSourceInterfacePointee transports the pointee of a pointer to an
// interface-typed variable — `new(any)`, `&x` with `var x any` — as the
// interface value it holds: a nil interface as the typed nil, a boxed value
// with its interface identity. The pointer path read the cell's scalar text
// and shipped an empty string in the interface's place, so the dependency saw
// a non-nil interface holding "" where Go has a nil one. It reports false for
// a pointee that is not an interface variable.
func (r *Runner) goSourceInterfacePointee(ptr *bashPPPointer) (bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || ptr == nil || len(ptr.path) != 0 || ptr.target == nil {
		return bashPPBridgeValue{}, false, nil
	}
	target := ptr.target
	if _, iface := r.bashPPInterfaceType(target.declType); !iface || target.pointer || target.vr.Kind == expand.Object {
		return bashPPBridgeValue{}, false, nil
	}
	text := r.bashPPBridgeTypeIdentity(target.declType)
	if target.interfaceValue == nil || target.interfaceValue.nilIface {
		return bashPPBridgeValue{Kind: "nil", Type: text, Interface: text}, true, nil
	}
	value, err := r.bashPPBridgeCell(target)
	if err != nil {
		return bashPPBridgeValue{}, true, err
	}
	if value.Interface == "" {
		value.Interface = text
	}
	return value, true, nil
}
