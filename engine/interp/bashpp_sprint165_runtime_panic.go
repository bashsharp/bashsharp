// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Runtime faults raised with a string payload.
//
// Sprint 162 made Go's run-time errors recoverable panics whose value is an
// interface holding the runtime's error type (bashpp_sprint162_runtime_error.
// go), so `recover().(error).Error()` answers the runtime's wording. Several
// fault sites — the nil dereference, the indexing bounds panic, makeslice,
// the channel close/send faults, the uncomparable-type comparison, the
// range-over-func iteration fault — still raise through bashPPRaise with the
// message alone, so the recovered value presents the dynamic type `string`
// and `recover().(error)` fails with `interface conversion: string is not
// error`. The next defect behind four next-defect rows.
//
// The message itself says which runtime value Go panics with: every message
// the runtime prefixes with `runtime error: ` is a runtime.Error (an
// errorString, or a boundsError for the two bounds forms), and the
// unprefixed messages the runtime raises as plainError are the fixed set
// below. A string that is neither stays a plain string panic — a program's
// own `panic("…")` never becomes an error.

// bashPPRuntimePlainErrors are the messages the runtime raises as
// plainError: runtime.Error values whose Error text carries no prefix.
var bashPPRuntimePlainErrors = map[string]bool{
	"close of nil channel":           true,
	"close of closed channel":        true,
	"send on closed channel":         true,
	"makechan: size out of range":    true,
	"assignment to entry in nil map": true,
}

// bashPPRuntimeErrorPayload boxes a string-raised runtime fault as the
// runtime's error value. It reports false for any text that is not one of
// the runtime's own messages.
func bashPPRuntimeErrorPayload(text string) (*bashPPInterfaceValue, bool) {
	if rest, ok := strings.CutPrefix(text, bashPPRuntimeErrorMessage); ok {
		typeName := bashPPRuntimeErrorString
		if strings.HasPrefix(rest, "index out of range") || strings.HasPrefix(rest, "slice bounds out of range") {
			typeName = bashPPRuntimeBoundsError
		}
		return bashPPRuntimeErrorValue(typeName, text), true
	}
	if bashPPRuntimePlainErrors[text] {
		return bashPPRuntimeErrorValue(bashPPRuntimePlainError, text), true
	}
	return nil, false
}

// goSourcePanicWrap binds the runtime's panicwrap fault for a value method
// reached through an interface whose dynamic value is a nil pointer: the
// itab wrapper `(*T).F` that adapts the value method for the pointer type
// panics with `value method main.T.F called using nil *T pointer` (a
// plainError) when it is INVOKED — a deferred `i.M()` binds without fault
// and panics when the deferred call runs — which is a different wording
// from the nil dereference a direct `t.F()` on a nil *T faults with. It
// returns the wrapper, or nil when the selection is not that case.
func (r *Runner) goSourcePanicWrap(iv *bashPPInterfaceValue, sel bashPPSelection, method string) *bashPPFunc {
	if !r.bashPPGoSource || iv == nil || iv.cell == nil || sel.method == nil || sel.method.decl == nil || sel.method.decl.Receiver == nil {
		return nil
	}
	if sel.method.decl.Receiver.Pointer || !iv.cell.pointer || !iv.cell.nilPointer {
		return nil
	}
	recv := sel.method.decl.Receiver.RecvType
	if recv == nil {
		return nil
	}
	typ := recv.Value
	pkg := r.goSourcePackageAt(recv.Pos())
	if pkg == "" {
		pkg = "main"
	}
	text := "value method " + pkg + "." + typ + "." + method + " called using nil *" + typ + " pointer"
	lit := &syntax.BashPPFuncLit{Kw: &syntax.Lit{Value: "func"}, Params: sel.method.decl.Params, Results: sel.method.decl.Results}
	return &bashPPFunc{lit: lit, runtimeError: &bashPPRuntimeErrorCall{text: text, method: method, panicWrap: bashPPRuntimeErrorValue(bashPPRuntimePlainError, text)}}
}

// goSourcePanicStructuredValue boxes a structured `panic(v)` argument — a
// struct or collection value, a pointer — as the interface value `panic`
// receives, with the argument's declared type as the dynamic type, so
// `recover().(T)` and `recover().(*T)` see it. Only the interface-typed
// argument was boxed before; a struct value travelled as its rendered text
// and recovered as a string.
func (r *Runner) goSourcePanicStructuredValue(cell *bashPPCell) *bashPPInterfaceValue {
	if !r.bashPPGoSource || cell == nil || cell.declType == nil {
		return nil
	}
	if !cell.pointer && cell.vr.Kind != expand.Object {
		return nil
	}
	return &bashPPInterfaceValue{dynamic: cell.declType, cell: cell}
}

// goSourcePanicNativeValue evaluates a dependency-owned `panic(v)` argument —
// `panic(fmt.Errorf(…))`, `panic(errors.New(…))` — into the interface value
// `panic` receives, keeping the dependency's value as the dynamic value so
// `recover().(error)`, `.Error()` and `%v` act on it. The argument used to
// travel as its source text. The report text is the value's Error text when
// it is an error, as the runtime prints it. It reports false for an
// argument the interpreter owns; a nil boxed value with ok means the
// evaluation already stopped the statement.
func (r *Runner) goSourcePanicNativeValue(expr syntax.BashPPExpr) (*bashPPInterfaceValue, string, bool) {
	if !r.bashPPGoSource || expr == nil || !r.bashPPNativeExpr(expr) {
		return nil, "", false
	}
	value, err := r.bashPPBridgeExpr(expr)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.exit.fatal(err)
		}
		return nil, "", true
	}
	cell := goSourceNativeValueCell(value)
	if cell.interfaceValue != nil {
		if cell.interfaceValue.nilIface {
			const message = "panic called with nil argument"
			return bashPPRuntimeErrorValue(bashPPRuntimePanicNil, message), message, true
		}
		return cell.interfaceValue, r.goSourcePanicNativeText(value, cell.vr.String()), true
	}
	return &bashPPInterfaceValue{dynamic: cell.declType, cell: cell}, r.goSourcePanicNativeText(value, cell.vr.String()), true
}

// goSourcePanicNativeText renders a dependency-owned panic value the way the
// runtime prints it: an error by its Error text; anything else by the text
// the value already has.
func (r *Runner) goSourcePanicNativeText(value bashPPBridgeValue, fallback string) string {
	if value.Kind != "handle" && value.Interface != "error" {
		return fallback
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return fallback
	}
	values, err := r.bashPPNativeRequest(r.ectx, req, bashPPBridgeRequest{Op: "call", Selector: "Error", Receiver: &value})
	if err != nil || len(values) != 1 || values[0].Kind != "string" {
		return fallback
	}
	return values[0].Text
}
