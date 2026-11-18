// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceNativeAddressable reports whether expr names storage owned by an
// imported dependency: an imported package variable, or a field/element path
// rooted at one.
func (r *Runner) goSourceNativeAddressable(expr syntax.BashPPExpr) bool {
	if !r.bashPPGoSource || len(r.bashPPImports) == 0 {
		return false
	}
	return r.goSourceNativeAddressRoot(expr)
}

func (r *Runner) goSourceNativeAddressRoot(expr syntax.BashPPExpr) bool {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceNativeAddressRoot(x.X)
	case *syntax.BashPPSelectorExpr:
		if id, ok := x.X.(*syntax.BashPPIdent); ok {
			alias := id.Name.Value
			_, imported := r.bashPPImports[alias]
			return imported && (r.bashPPScope == nil || r.bashPPScope.lookup(alias) == nil)
		}
		return r.goSourceNativeAddressRoot(x.X)
	case *syntax.BashPPIndexExpr:
		return r.goSourceNativeAddressRoot(x.X)
	}
	return false
}

func (r *Runner) goSourceNativeAddressExpr(expr syntax.BashPPExpr) bool {
	address, ok := bashPPUnparenExpr(expr).(*syntax.BashPPAddressExpr)
	return ok && r.goSourceNativeAddressable(address.X)
}

func (r *Runner) goSourceNativeAddress(expr syntax.BashPPExpr) (bashPPBridgeValue, error) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceNativeAddress(x.X)
	case *syntax.BashPPSelectorExpr:
		if id, ok := x.X.(*syntax.BashPPIdent); ok {
			alias := id.Name.Value
			if _, imported := r.bashPPImports[alias]; imported {
				return r.goSourceNativeVariableAddress(alias + "." + x.Sel.Value)
			}
		}
		base, err := r.goSourceNativeAddress(x.X)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		return r.bashPPNativeAccess(r.ectx, "field-pointer", base, x.Sel.Value)
	case *syntax.BashPPIndexExpr:
		base, err := r.goSourceNativeAddress(x.X)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		key, err := r.bashPPBridgeExpr(x.Index)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		return r.bashPPNativeAccess(r.ectx, "index-pointer", base, "", key)
	}
	return bashPPBridgeValue{}, fmt.Errorf("BASHPP-EPOINTER-TARGET: operand is not dependency-owned storage")
}

func (r *Runner) goSourceNativeVariableAddress(selector string) (bashPPBridgeValue, error) {
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	values, err := r.bashPPNativeRequest(r.ectx, req, bashPPBridgeRequest{Op: "var-pointer", Selector: selector})
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	if len(values) != 1 {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: native variable address returned %d values", len(values))
	}
	return values[0], nil
}

func (r *Runner) goSourceNativePointerCell(name string) *bashPPBridgeValue {
	if !r.bashPPGoSource {
		return nil
	}
	native := r.bashPPNativeCellValue(name)
	if native == nil || native.Kind != "handle" || !strings.HasPrefix(native.Type, "*") {
		return nil
	}
	return native
}

func (r *Runner) goSourceNativePointee(err error) (any, bool, error) {
	native, ok := r.goSourceNativePointerError(err)
	if !ok {
		return nil, false, nil
	}
	value, accessErr := r.bashPPNativeAccess(r.ectx, "deref", *native, "")
	if accessErr != nil {
		return nil, true, accessErr
	}
	read, _, err := bashPPNativeReadValue(value)
	return read, true, err
}

func (r *Runner) goSourceNativePointeeAssign(err error, rhs syntax.BashPPExpr) (bool, error) {
	native, ok := r.goSourceNativePointerError(err)
	if !ok {
		return false, nil
	}
	value, bridgeErr := r.bashPPBridgeExpr(rhs)
	if bridgeErr != nil {
		return true, bridgeErr
	}
	_, accessErr := r.bashPPNativeAccess(r.ectx, "deref-set", *native, "", value)
	return true, accessErr
}

func (r *Runner) goSourceNativePointerError(err error) (*bashPPBridgeValue, bool) {
	var native *bashPPNativePointerValueError
	if !r.bashPPGoSource || !errors.As(err, &native) || native.value == nil {
		return nil, false
	}
	return native.value, true
}

// goSourceNativeAssignBase evaluates the operand of a field assignment whose
// storage the dependency owns. `pkg.Var.Field = v` writes the package
// variable itself, so an operand that is an imported variable, or a field
// path rooted at one, is taken by address: read as a value it is a copy, and
// the write would land in that copy and be lost. The native field write
// follows the pointer, including the implicit dereference of a pointer-typed
// variable. Every other operand keeps the receiver evaluation.
func (r *Runner) goSourceNativeAssignBase(expr syntax.BashPPExpr) (bashPPBridgeValue, error) {
	if r.bashPPGoSource && r.goSourceImportedVariablePath(expr) {
		return r.goSourceNativeAddress(expr)
	}
	return r.bashPPNativeReceiver(expr)
}

// goSourceImportedVariablePath reports a selector path whose root is an
// imported package variable not shadowed by a local binding of the alias.
func (r *Runner) goSourceImportedVariablePath(expr syntax.BashPPExpr) bool {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceImportedVariablePath(x.X)
	case *syntax.BashPPSelectorExpr:
		if id, ok := x.X.(*syntax.BashPPIdent); ok {
			alias := id.Name.Value
			_, imported := r.bashPPImports[alias]
			return imported && (r.bashPPScope == nil || r.bashPPScope.lookup(alias) == nil)
		}
		return r.goSourceImportedVariablePath(x.X)
	}
	return false
}
