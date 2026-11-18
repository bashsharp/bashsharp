// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"go/constant"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Go run-time errors as recoverable panics.
//
// A Go program that divides by zero, fails a one-result type assertion or
// panics with a nil argument does not stop with a diagnostic: the runtime
// raises a panic whose value implements `runtime.Error` (and therefore
// `error`), a deferred call may recover it, `recover().(error)` succeeds and
// `.Error()` yields the runtime's wording. Only an unrecovered one terminates
// the program, printed as `panic: runtime error: …`.
//
// Classic Bash++ keeps its own diagnostics for the same conditions; every
// raise below is gated on an original Go program (bashPPGoSource), so the
// classic surface is unchanged.
//
// The panic value is an interface value whose dynamic type is the runtime's
// concrete error type (`runtime.errorString`, `*runtime.TypeAssertionError`,
// `*runtime.PanicNilError`, …). Its payload is carried as a string-shaped
// bridge value so that a dependency call — `fmt.Println(recover())`,
// `log.Fatalf("%v", err)` — prints the message the way Go prints an error,
// while the interpreter answers the two methods of runtime.Error itself.

// Dynamic type names of the runtime's error values, spelled as %T prints them.
const (
	bashPPRuntimeErrorString  = "runtime.errorString"
	bashPPRuntimeErrorAddress = "runtime.errorAddressString"
	bashPPRuntimePlainError   = "runtime.plainError"
	bashPPRuntimeBoundsError  = "runtime.boundsError"
	bashPPRuntimeTypeAssert   = "*runtime.TypeAssertionError"
	bashPPRuntimePanicNil     = "*runtime.PanicNilError"
	bashPPRuntimeErrorMessage = "runtime error: "
)

// bashPPRuntimeErrorType reports whether a dynamic type names one of the
// runtime's error values.
func bashPPRuntimeErrorType(typ syntax.BashPPTypeExpr) bool {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return false
	}
	switch named.Name.Value {
	case bashPPRuntimeErrorString, bashPPRuntimeErrorAddress, bashPPRuntimePlainError, bashPPRuntimeBoundsError, bashPPRuntimeTypeAssert, bashPPRuntimePanicNil:
		return true
	}
	return false
}

// bashPPRuntimeErrorValue builds the interface value a runtime error panics
// with: dynamic type typeName, payload text.
func bashPPRuntimeErrorValue(typeName, text string) *bashPPInterfaceValue {
	return bashPPRuntimeErrorValueWithAddr(typeName, text, 0)
}

func bashPPRuntimeErrorValueWithAddr(typeName, text string, addr uint64) *bashPPInterfaceValue {
	dynamic := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: typeName}}
	// The payload is the message as a plain string cell: it crosses the
	// dependency boundary as a Go string, which prints exactly as an error's
	// Error text does under %v and %s, and it keeps every method call on the
	// value inside the interpreter, where the two methods of runtime.Error
	// are answered by bashPPRuntimeErrorMethod.
	cell := &bashPPCell{declType: dynamic, vr: expand.Variable{Set: true, Kind: expand.String, Str: text}, scalarKind: constant.String}
	result := &bashPPInterfaceValue{dynamic: dynamic, cell: cell}
	if addr != 0 {
		result.runtimeAddr = &addr
	}
	return result
}

// bashPPRuntimeErrorText reads the message of a runtime error value.
func bashPPRuntimeErrorText(iv *bashPPInterfaceValue) string {
	if iv == nil || iv.cell == nil {
		return ""
	}
	return iv.cell.vr.String()
}

// bashPPRuntimeErrorInterface is the runtime.Error interface: error plus the
// RuntimeError marker method.
func bashPPRuntimeErrorInterface() *syntax.BashPPInterfaceType {
	iface := bashPPPredeclaredErrorInterface()
	marker := &syntax.BashPPMethodSpec{Name: &syntax.Lit{Value: "RuntimeError"}}
	iface.Elems = append(iface.Elems, &syntax.BashPPInterfaceElem{Method: marker})
	return iface
}

// bashPPRaiseRuntimeError starts a panic with a runtime error value and
// returns the sentinel an expression evaluator hands back for a call that has
// already transferred control, so every consumer keeps the unwinding state
// unchanged. typeName is the runtime's concrete type; text is the complete
// message (`runtime error: integer divide by zero`).
func (r *Runner) bashPPRaiseRuntimeError(typeName, text string) error {
	r.bashPPPanicTrace(nil)
	r.bashPPRaiseValue(text, bashPPRuntimeErrorValue(typeName, text))
	return errBashPPScalarInterrupted
}

// bashPPPanicTrace records the interpreted frames a GoSource panic report
// prints; the predeclared panic records them from its call site, a runtime
// error from the statement being run.
func (r *Runner) bashPPPanicTrace(call *syntax.BashPPCall) {
	if !r.bashPPGoSource {
		return
	}
	pos := r.curStmtPos
	if call != nil {
		pos = call.Pos()
	}
	// A multi-file program names the source the position lies in, as every
	// positioned diagnostic does.
	r.bashPPPanic.traceSource = r.filename
	if r.bashPPGoSourceFile != nil {
		if source, ok := r.bashPPGoSourceFile.SourceAt(pos); ok {
			r.bashPPPanic.traceSource = source.Name
		}
	}
	r.bashPPPanic.traceLine = pos.Line()
	r.bashPPPanic.traceFrames = r.bashPPPanic.traceFrames[:0]
	for _, frame := range r.callStack {
		r.bashPPPanic.traceFrames = append(r.bashPPPanic.traceFrames, frame.funcName)
	}
}

// bashPPRuntimeErrorImplements answers bashPPImplements for a runtime error
// value: its method set is {Error, RuntimeError}, plus Addr for fault panics,
// so it satisfies `error`, `runtime.Error` and the runtime's fault-address
// interface only when the worker supplied an actual fault address.
func (r *Runner) bashPPRuntimeErrorImplements(actual syntax.BashPPTypeExpr, iface *syntax.BashPPInterfaceType) error {
	methods, err := r.bashPPInterfaceMethodSet("interface", iface, make(map[string]bool))
	if err != nil {
		return err
	}
	for _, key := range methods.order {
		name := methods.byName[key].name
		if name != "Error" && name != "RuntimeError" && name != "Addr" {
			return &bashPPRuntimeErrorMissingMethod{typ: bashPPTypeText(actual), method: name}
		}
	}
	return nil
}

type bashPPRuntimeErrorMissingMethod struct{ typ, method string }

func (e *bashPPRuntimeErrorMissingMethod) Error() string {
	return "BASHPP-EINTERFACE-MISSING: " + e.typ + " does not implement interface (missing method " + e.method + ")"
}

// bashPPRuntimeErrorMethod binds a method of a runtime error value; Error
// yields the message, RuntimeError yields nothing, and Addr yields the
// SetPanicOnFault address when the runtime supplied one.
func (r *Runner) bashPPRuntimeErrorMethod(iv *bashPPInterfaceValue, method string) (*bashPPFunc, bool) {
	switch method {
	case "Error", "RuntimeError", "Addr":
		if method == "Addr" && iv.runtimeAddr == nil {
			break
		}
		// The literal carries the method's signature — `func() string` for
		// Error, `func()` for RuntimeError — so result binding sees the
		// declared result list; the body is answered by
		// bashPPInvokeRuntimeErrorMethod, never interpreted.
		lit := &syntax.BashPPFuncLit{Kw: &syntax.Lit{Value: "func"}}
		switch method {
		case "Error":
			lit.Results = []*syntax.BashPPField{{FieldType: &syntax.Lit{Value: "string"}, FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}}}
		case "Addr":
			lit.Results = []*syntax.BashPPField{{FieldType: &syntax.Lit{Value: "uintptr"}, FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "uintptr"}}}}
		}
		call := &bashPPRuntimeErrorCall{text: bashPPRuntimeErrorText(iv), method: method}
		if iv.runtimeAddr != nil {
			call.addr = *iv.runtimeAddr
		}
		return &bashPPFunc{lit: lit, runtimeError: call}, true
	}
	r.errf("type %s has no method %s\n", bashPPTypeText(iv.dynamic), method)
	r.exit.code = 2
	return nil, false
}

// bashPPRuntimeErrorCall is the callable bound by bashPPRuntimeErrorMethod.
type bashPPRuntimeErrorCall struct {
	text   string
	method string
	addr   uint64
	// panicWrap is set on the itab wrapper of a value method reached through
	// a nil pointer in an interface: invoking it raises the runtime's
	// panicwrap fault instead of running a body. See
	// bashpp_sprint165_runtime_panic.go.
	panicWrap *bashPPInterfaceValue
}

func (r *Runner) bashPPInvokeRuntimeErrorMethod(fn *bashPPFunc) []string {
	if fn.runtimeError.panicWrap != nil {
		r.bashPPResultCells = nil
		r.bashPPPanicTrace(nil)
		r.bashPPRaiseValue(fn.runtimeError.text, fn.runtimeError.panicWrap)
		return nil
	}
	if fn.runtimeError.method != "Error" && fn.runtimeError.method != "Addr" {
		r.bashPPResultCells = nil
		return nil
	}
	if fn.runtimeError.method == "Addr" {
		text := strconv.FormatUint(fn.runtimeError.addr, 10)
		cell := &bashPPCell{
			vr:          expand.Variable{Set: true, Kind: expand.String, Str: text},
			exactScalar: constant.MakeUint64(fn.runtimeError.addr), scalarKind: constant.Int, typeName: "uintptr",
			declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "uintptr"}},
		}
		r.bashPPResultCells = []*bashPPCell{cell}
		return []string{text}
	}
	cell := &bashPPCell{
		vr:         expand.Variable{Set: true, Kind: expand.String, Str: fn.runtimeError.text},
		scalarKind: constant.String, typeName: "string",
		declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}},
	}
	r.bashPPResultCells = []*bashPPCell{cell}
	return []string{fn.runtimeError.text}
}

// goSourceRuntimeTypeText spells a type the way the runtime spells it in a
// panic message: a program-declared named type carries its package (`main.T`),
// the empty interface is `interface {}`, and composite shapes follow reflect.
func (r *Runner) goSourceRuntimeTypeText(typ syntax.BashPPTypeExpr) string {
	switch x := typ.(type) {
	case nil:
		return "nil"
	case *syntax.BashPPNamedType:
		if x.Name == nil {
			return "nil"
		}
		name := x.Name.Value
		switch name {
		case "any", "interface{}", "interface {}":
			return "interface {}"
		case "byte":
			return "uint8"
		case "rune":
			return "int32"
		}
		if strings.Contains(name, ".") || bashPPBuiltinType(name) {
			return name
		}
		// Linked source packages flatten their declarations under a hygiene
		// name. That name is an identity key, not a Go spelling: recoverable
		// runtime errors must retain the package that declared the type.
		if tag := goSourceLinkedPackage(name); tag != "" {
			if pkg := r.goSourceLinkedPackageName(tag); pkg != "" {
				return pkg + "." + goSourceDeclaredName(name)
			}
		}
		if _, declared := r.bashPPTypes[name]; declared {
			if pkg := r.goSourcePackageAt(x.Pos()); pkg != "" {
				return pkg + "." + name
			}
			return "main." + name
		}
		return name
	case *syntax.BashPPPointerType:
		return "*" + r.goSourceRuntimeTypeText(x.Element)
	case *syntax.BashPPCollectionType:
		if x.Kind == "map" {
			return "map[" + r.goSourceRuntimeTypeText(x.Key) + "]" + r.goSourceRuntimeTypeText(x.Element)
		}
		length := ""
		if x.Length != nil {
			length = x.Length.Value
		}
		return "[" + length + "]" + r.goSourceRuntimeTypeText(x.Element)
	case *syntax.BashPPInterfaceType:
		if len(x.Methods) == 0 {
			return "interface {}"
		}
		return goSourceReflectTypeText(typ)
	case *syntax.BashPPFuncType:
		return bashPPGoCanonicalTypeText(typ)
	case *syntax.BashPPStructType:
		return goSourceReflectTypeText(typ)
	}
	return bashPPTypeText(typ)
}

// goSourceLinkedPackageName resolves a flattened package tag through the
// declaration that carries it. A type use may be positioned in its caller,
// so its position alone cannot identify the declaring source file.
func (r *Runner) goSourceLinkedPackageName(tag string) string {
	if r.bashPPGoSourceFile == nil || tag == "" {
		return ""
	}
	for _, stmt := range r.bashPPGoSourceFile.Stmts {
		var name *syntax.Lit
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			name = d.Name
		case *syntax.BashPPFuncDecl:
			name = d.Name
			if d.Receiver != nil && d.Receiver.RecvType != nil {
				name = d.Receiver.RecvType
			}
		}
		if name == nil || goSourceLinkedPackage(name.Value) != tag {
			continue
		}
		// Linked declarations can have synthesized statement/keyword positions.
		// The declaration identifier retains its original source position.
		pos := name.Pos()
		if !pos.IsValid() {
			pos = stmt.Pos()
		}
		if source, ok := r.bashPPGoSourceFile.SourceAt(pos); ok && source.Package != "" {
			return source.Package
		}
	}
	return ""
}

// goSourceRuntimeTypeName is the type spelling used inside Go runtime
// messages. Classic Bash++ deliberately keeps its existing diagnostic text.
func (r *Runner) goSourceRuntimeTypeName(typ syntax.BashPPTypeExpr) string {
	if r.bashPPGoSource {
		return r.goSourceRuntimeTypeText(typ)
	}
	return bashPPTypeText(typ)
}

// goSourceTypeAssertionText is the message of a failed one-result assertion
// `x.(T)`: static is x's interface type, dynamic the value's type (nil for a
// nil interface), asserted the T, missing the method a non-interface dynamic
// type lacks when T is an interface.
func (r *Runner) goSourceTypeAssertionText(static, dynamic, asserted syntax.BashPPTypeExpr, missing string) string {
	staticText := r.goSourceRuntimeTypeText(static)
	if _, ok := r.bashPPInterfaceType(asserted); ok {
		if dynamic == nil {
			if staticText == "interface {}" {
				staticText = "interface"
			}
			return "interface conversion: " + staticText + " is nil, not " + r.goSourceRuntimeTypeText(asserted)
		}
		return "interface conversion: " + r.goSourceRuntimeTypeText(dynamic) + " is not " + r.goSourceRuntimeTypeText(asserted) + ": missing method " + missing
	}
	dynamicText := "nil"
	if dynamic != nil {
		dynamicText = r.goSourceRuntimeTypeText(dynamic)
	}
	assertedText := r.goSourceRuntimeTypeText(asserted)
	text := "interface conversion: " + staticText + " is " + dynamicText + ", not " + assertedText
	if dynamic != nil && dynamicText == assertedText {
		// Go's own disambiguation: identical spellings from different packages
		// (an unexported field qualifies an anonymous struct by its package —
		// fixedbugs/issue18911.go) versus from different scopes in one package
		// (a function-local type declaration). Compare the linked-package tag
		// of each type's source, the same tag struct identity is keyed on.
		if r.goSourceTypePackageTag(dynamic) != r.goSourceTypePackageTag(asserted) {
			text += " (types from different packages)"
		} else {
			text += " (types from different scopes)"
		}
	}
	return text
}

// goSourceTypePackageTag reports the linked-package tag of the source a type
// expression was spelled in, or "" for the program package or a synthesized
// type with no source position.
func (r *Runner) goSourceTypePackageTag(typ syntax.BashPPTypeExpr) string {
	if !r.bashPPGoSource || typ == nil {
		return ""
	}
	pos := typ.Pos()
	if !pos.IsValid() {
		return ""
	}
	return r.goSourcePackageAt(pos)
}

// bashPPPanicValueText renders a panic value the way the runtime prints it:
// an error by its Error text, a value of a program-declared scalar type with
// its type (`main.MyInt(4)`, `main.MyStr("ms")`), a basic value as itself.
// Anything else keeps the text the caller already rendered.
func (r *Runner) bashPPPanicValueText(value any, fallback string) string {
	iv, ok := value.(*bashPPInterfaceValue)
	if !ok || iv == nil || iv.nilIface || iv.cell == nil {
		return fallback
	}
	if bashPPRuntimeErrorType(iv.dynamic) {
		return bashPPRuntimeErrorText(iv)
	}
	// A native error stored in an interface keeps its bridge handle in the
	// concrete payload. Render its Error method at the panic boundary, just
	// as Go does, while retaining the original handle for recover.
	if r.bashPPGoSource && iv.cell.vr.Kind == expand.Object {
		if native, ok := iv.cell.vr.Obj.(*bashPPBridgeValue); ok && native != nil {
			return r.goSourcePanicNativeText(*native, fallback)
		}
	}
	if r.bashPPGoSource && iv.cell.vr.Kind == expand.String && bashPPQualifiedDynamic(iv) {
		// A dependency can implement error with a defined scalar, notably
		// syscall.Errno. Ask that concrete value for Error() only when the
		// unrecovered panic is rendered; recover keeps the original value.
		// The program's own scalar types (MyInt) are rendered below.
		boxed := &bashPPCell{declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "error"}}, interfaceValue: iv}
		if native, err := r.bashPPBridgeCell(boxed); err == nil {
			return r.goSourcePanicNativeText(native, fallback)
		}
	}
	named, ok := iv.dynamic.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil || iv.cell.vr.Kind != expand.String {
		return fallback
	}
	name := named.Name.Value
	if bashPPBuiltinType(name) || strings.Contains(name, ".") {
		return fallback
	}
	decl, declared := r.bashPPTypes[name]
	if !declared || decl.alias {
		return fallback
	}
	base, ok := decl.typeExpr.(*syntax.BashPPNamedType)
	if !ok || base.Name == nil || !bashPPBuiltinType(base.Name.Value) {
		return fallback
	}
	text := iv.cell.vr.Str
	if base.Name.Value == "string" {
		text = strconv.Quote(text)
	}
	return r.goSourceRuntimeTypeText(iv.dynamic) + "(" + text + ")"
}

// goSourceTypeAssertionFailure spells the failure of `x.(T)` from the
// operand's static type, its interface value and the asserted type; for an
// interface T the missing method is the first one the dynamic type lacks.
func (r *Runner) goSourceTypeAssertionFailure(static syntax.BashPPTypeExpr, iv *bashPPInterfaceValue, asserted syntax.BashPPTypeExpr, assertIface *syntax.BashPPInterfaceType) (string, error) {
	var dynamic syntax.BashPPTypeExpr
	if iv != nil && !iv.nilIface {
		dynamic = iv.dynamic
	}
	missing := ""
	if assertIface != nil && dynamic != nil {
		methods, err := r.bashPPInterfaceMethodSet("interface", assertIface, make(map[string]bool))
		if err != nil {
			return "", err
		}
		implementationErr := r.bashPPImplements(dynamic, assertIface)
		if implementationErr != nil {
			text := implementationErr.Error()
			// Admission already resolved the actual method set and its signatures.
			// Preserve its failing method, never substitute the first required method.
			for _, key := range methods.order {
				name := methods.byName[key].name
				if strings.HasPrefix(text, "BASHPP-EINTERFACE-MISSING:") && strings.HasSuffix(text, "(missing method "+name+")") ||
					strings.HasPrefix(text, "BASHPP-EINTERFACE-SIGNATURE:") && strings.HasSuffix(text, " method "+name+" has wrong signature") {
					missing = name
					break
				}
			}
		}
		if missing == "" {
			return "", fmt.Errorf("BASHPP-EASSERT-DIAGNOSTIC: cannot identify the failing interface method: %v", implementationErr)
		}
	}
	return r.goSourceTypeAssertionText(static, dynamic, asserted, missing), nil
}

// bashPPPanicArgument settles the value and printed text of an original Go
// program's `panic(v)`. A nil argument — the untyped nil or a nil interface —
// panics with a *runtime.PanicNilError since Go 1.21; any other value prints
// by its dynamic type (see bashPPPanicValueText).
func (r *Runner) bashPPPanicArgument(c *syntax.BashPPCall, value any, text string) (any, string) {
	nilArgument := false
	if iv, ok := value.(*bashPPInterfaceValue); ok && iv != nil && iv.nilIface {
		nilArgument = true
	} else if len(c.ArgExprs) == 1 {
		if id, ok := c.ArgExprs[0].(*syntax.BashPPIdent); ok && id.Name.Value == "nil" && (r.bashPPScope == nil || r.bashPPScope.lookup("nil") == nil) {
			nilArgument = true
		}
	}
	if nilArgument {
		const message = "panic called with nil argument"
		return bashPPRuntimeErrorValue(bashPPRuntimePanicNil, message), message
	}
	return value, r.bashPPPanicValueText(value, text)
}

// bashPPPanicScalarValue is the payload of `panic(v)` for a scalar v. A value
// of a program-declared type keeps that type as the dynamic type of the
// interface `panic` receives, so `recover().(MyInt)` and the panic report
// (`panic: main.MyInt(4)`) see it; an untyped or predeclared scalar travels
// as the plain Go value.
func (r *Runner) bashPPPanicScalarValue(scalar bashPPScalar) any {
	if scalar.typ == "" || bashPPBuiltinType(scalar.typ) || strings.Contains(scalar.typ, ".") {
		return bashPPScalarAny(scalar.value)
	}
	if _, declared := r.bashPPTypes[scalar.typ]; !declared {
		return bashPPScalarAny(scalar.value)
	}
	dynamic, typeName := bashPPScalarNamedType(scalar.typ)
	cell := &bashPPCell{
		vr:          expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarString(scalar.value)},
		exactScalar: scalar.value, scalarKind: scalar.value.Kind(),
		declType: dynamic, typeName: typeName,
	}
	return &bashPPInterfaceValue{dynamic: dynamic, cell: cell}
}

// bashPPPredeclaredRecover reports whether expr is a call of the predeclared
// recover in an original Go program — `recover()` with no function or
// variable of that name shadowing it.
func (r *Runner) bashPPPredeclaredRecover(expr syntax.BashPPExpr) bool {
	return r.bashPPGoSource && bashPPRecoverExpr(expr) && r.bashPPFuncs["recover"] == nil && (r.bashPPScope == nil || r.bashPPScope.lookup("recover") == nil)
}

// bashPPRecoverCell is the cell a `recover()` expression yields: an `any`
// holding the recovered interface value, or the nil interface when nothing
// was in flight.
func (r *Runner) bashPPRecoverCell() *bashPPCell {
	iv, _ := r.bashPPRecoverInterfaceValue()
	cell := &bashPPCell{declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}, interfaceValue: iv}
	if iv.cell != nil {
		cell.vr, cell.scalarKind = iv.cell.vr, iv.cell.scalarKind
	} else {
		cell.vr = expand.Variable{Set: true, Kind: expand.String}
	}
	return cell
}

// bashPPReturnRecover claims `return recover()` in an original Go program.
func (r *Runner) bashPPReturnRecover(call *syntax.BashPPCall) (*bashPPCell, bool) {
	if call == nil || !r.bashPPPredeclaredRecover(call) {
		return nil, false
	}
	return r.bashPPRecoverCell(), true
}

// bashPPQualifiedDynamic reports whether an interface holds a value of a
// package-qualified (dependency) named type, such as syscall.Errno.
func bashPPQualifiedDynamic(iv *bashPPInterfaceValue) bool {
	named, ok := iv.dynamic.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && strings.Contains(named.Name.Value, ".")
}
