// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Method dispatch and interface assertions across the bridge boundary: a
// value's TYPE, not where its storage lives, decides where a method runs and
// who answers whether it implements an interface.

// goSourceDependencyHandle is the dependency handle a cell holds, directly
// or as the pointee of a pointer — the *strings.Builder a local function
// returned, `&b` of an imported type — or nil for an interpreter-owned cell.
func (r *Runner) goSourceDependencyHandle(cell *bashPPCell) *bashPPBridgeValue {
	// The carrier test and the payload it selects answer from one snapshot.
	cell = cell.view()
	if cell == nil {
		return nil
	}
	var held any
	switch {
	case cell.pointer && cell.pointerValue != nil:
		pointee, _, _, err := cell.pointerValue.read()
		if err != nil {
			return nil
		}
		held = pointee
	case cell.vr.Kind == expand.Object:
		held = cell.vr.Obj
	}
	handle, ok := held.(*bashPPBridgeValue)
	if !ok || handle == nil {
		return nil
	}
	// A pointer the dependency handed over (`&b` of an imported type) is the
	// pointer value whose one element is the handle.
	if handle.Kind == "pointer" && len(handle.Elements) == 1 && handle.Elements[0].Kind == "handle" {
		return handle
	}
	if handle.Kind != "handle" {
		return nil
	}
	return handle
}

func (r *Runner) goSourceDependencyOwnedCell(cell *bashPPCell) bool {
	return r.goSourceDependencyHandle(cell) != nil
}

// goSourceOriginalMethodCall reports a two-part call `x.M(...)` whose
// receiver variable is declared with a local named type that declares M (or
// holds an interface whose dynamic type does): the original body is the
// interpreter's to run even when the variable's VALUE is held natively — a
// channel of a local channel type, a typed nil of a local function type.
// Such a call was routed to the dependency on the strength of the native
// value and refused as a mutation of interpreter-owned references.
func (r *Runner) goSourceOriginalMethodCall(name, method string) bool {
	if !r.bashPPGoSource || r.bashPPScope == nil {
		return false
	}
	cell := r.bashPPScope.lookup(name).view()
	if cell == nil {
		return false
	}
	typ := cell.declType
	if iv := cell.interfaceValue; iv != nil {
		if iv.nilIface {
			return false
		}
		typ = iv.dynamic
	}
	if ptr, ok := typ.(*syntax.BashPPPointerType); ok {
		typ = ptr.Element
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return false
	}
	return r.bashPPMethods[named.Name.Value][method] != nil
}

// goSourceNativeReceiverMethod binds a method selected on a computed
// receiver that evaluated to a dependency-owned value — `f().Error()` where
// the local f returns an error the dependency made, `m[k].Method()` over
// native elements. The receiver has no name for the dependency dispatch to
// recognise, so the method value is fetched from the dependency (the
// `member` access every named native receiver already uses) and bound as
// the native callable the computed-callee path invokes. It reports false
// for a receiver the interpreter owns.
func (r *Runner) goSourceNativeReceiverMethod(receiver *bashPPCell, method string) (*bashPPFunc, bool, error) {
	if !r.bashPPGoSource || receiver == nil {
		return nil, false, nil
	}
	// Each unwrap step reads the payload word ONCE: the receiver can be a cell
	// a task shares, and testing the word and then dereferencing it again
	// could descend into a different store's interface value.
	cell := receiver
	for cell != nil {
		iface := cell.viewInterface()
		if iface == nil || iface.nilIface {
			break
		}
		cell = iface.cell
	}
	handle := r.goSourceDependencyHandle(cell)
	if handle == nil {
		return nil, false, nil
	}
	base, err := r.bashPPBridgeCell(cell)
	if err != nil {
		return nil, true, err
	}
	value, err := r.bashPPNativeAccess(r.ectx, "member", base, method)
	if err != nil {
		return nil, true, err
	}
	if value.Kind != "handle" || !(value.Function || strings.HasPrefix(value.Type, "func(")) {
		return nil, true, fmt.Errorf("gosource: %s is not a method of the dependency value", method)
	}
	if handle.NativeType != "" {
		value.Callable = handle.NativeType + "." + method
		if cell.view().pointer {
			value.Callable = "*" + value.Callable
		}
	}
	fn := &bashPPFunc{native: &value}
	if sig := syntax.BashPPTypeExprFromText(value.Type); sig != nil {
		if ft, ok := sig.(*syntax.BashPPFuncType); ok {
			fn.lit = &syntax.BashPPFuncLit{Params: ft.Params, Results: ft.Results}
		}
	}
	return fn, true, nil
}

// goSourceNativeAssertsInterface answers `x.(I)` for an interface value
// whose dynamic value the dependency owns — the *errors.errorString behind
// errors.New, the *fmt.wrapError behind fmt.Errorf, `&b` of an imported
// type — when I is an interface: a local one, or an imported one such as
// fmt.Stringer, which the interpreter cannot tell from a concrete imported
// type by its spelling. The interpreter's method-set check knows nothing of
// a dependency type's methods and answered false for every such assertion;
// the dependency's own assignability check is the authority, and its reply
// says whether the asserted type is an interface at all. It reports
// claimed=false for an interpreter-owned dynamic value and for a concrete
// asserted type, which keep the identity comparison.
func (r *Runner) goSourceNativeAssertsInterface(iv *bashPPInterfaceValue, asserted syntax.BashPPTypeExpr) (matched, claimed bool, err error) {
	if !r.bashPPGoSource || iv == nil || iv.nilIface || iv.cell == nil {
		return false, false, nil
	}
	if !r.goSourceDependencyOwnedCell(iv.cell) {
		return false, false, nil
	}
	_, localInterface := r.bashPPInterfaceType(asserted)
	if !localInterface && !r.goSourceImportedTypeName(bashPPTypeText(asserted)) {
		return false, false, nil
	}
	value, err := r.bashPPBridgeCell(iv.cell)
	if err != nil {
		return false, true, err
	}
	value.Interface = ""
	answer, err := r.bashPPNativeTypeRequest("assignable", asserted, value)
	if err != nil {
		return false, true, err
	}
	if !localInterface && answer.Interface == "" {
		// A concrete imported type: identity, not assignability, decides.
		return false, false, nil
	}
	return answer.Kind == "bool" && answer.Text == "true", true, nil
}

// goSourceNativeAssertIdentical answers `x.(T)` and type switch cases for a
// dependency-owned dynamic value when T is an imported concrete type. Spelling
// canonicalisation handles aliases like ast.GenDecl vs go/ast.GenDecl, but the
// dependency's reflect type is the authority for values crossing as handles.
func (r *Runner) goSourceNativeAssertIdentical(iv *bashPPInterfaceValue, asserted syntax.BashPPTypeExpr) (matched, claimed bool, err error) {
	if !r.bashPPGoSource || iv == nil || iv.nilIface || iv.cell == nil {
		return false, false, nil
	}
	if !r.goSourceDependencyOwnedCell(iv.cell) {
		return false, false, nil
	}
	if !r.goSourceImportedTypeName(bashPPTypeText(asserted)) {
		return false, false, nil
	}
	value, err := r.bashPPBridgeCell(iv.cell)
	if err != nil {
		return false, true, err
	}
	value.Interface = ""
	answer, err := r.bashPPNativeTypeRequest("identical", asserted, value)
	if err != nil {
		return false, true, err
	}
	if answer.Kind != "bool" {
		return false, true, fmt.Errorf("gosource: type identity of %s answered %s", bashPPTypeText(asserted), answer.Kind)
	}
	// A dependency value carries the worker-authenticated package-path identity
	// of its dynamic type. Prefer that stable identity when a large interpreted
	// package has materialised another object for a per-file import spelling:
	// the reflect registry's object miss must not make *go/ast.Ident cease to be
	// the *ast.Ident named by this source file.
	if answer.Text != "true" && value.NativeType != "" {
		dynamic, dynamicOK := r.goSourceNativeTypePathIdentity(value.NativeType)
		target, targetOK := r.goSourceNativeTypePathIdentity(bashPPTypeText(asserted))
		if dynamicOK && targetOK && dynamic == target {
			return true, true, nil
		}
	}
	return answer.Text == "true", true, nil
}
