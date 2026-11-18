package interp

// Sprint: #118; Story: #50; Story-ID: cf81e4868348
import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"math"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (r *Runner) bashPPBridgeHandles(call *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || call == nil {
		return false
	}
	// unsafe builtins over interpreter storage; see bashpp_s247_unsafe.go.
	if r.goSourceUnsafeLocalCall(call) {
		return false
	}
	if call.CalleeExpr != nil {
		selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr)
		if !ok {
			return false
		}
		return r.bashPPNativeExpr(selector.X) ||
			r.goSourceNativeScalarReceiver(selector.X) ||
			r.bashPPNativePointerExpr(selector.X) ||
			r.bashPPPromotedNativeReceiver(selector.X, selector.Sel.Value) != nil
	}
	if len(call.Fun) < 1 {
		return false
	}
	if len(call.Fun) >= 2 {
		if _, ok := r.bashPPImports[call.Fun[0].Value]; ok {
			// unsafe.Sizeof, Alignof and Offsetof are constant operators
			// the evaluator folds from the operand's declared type; the
			// dependency has no callable symbol for them.
			return !r.goSourceUnsafeConstantOperator(call)
		}
		if len(call.Fun) == 2 && r.goSourceOriginalMethodCall(call.Fun[0].Value, call.Fun[1].Value) {
			return false
		}
		if r.bashPPNativeCellValue(call.Fun[0].Value) != nil {
			return true
		}
		if len(call.Fun) == 2 && (r.goSourceNativeScalarReceiver(&syntax.BashPPIdent{Name: call.Fun[0]}) ||
			r.bashPPNativePointerExpr(&syntax.BashPPIdent{Name: call.Fun[0]})) {
			return true
		}
		var receiver syntax.BashPPExpr = &syntax.BashPPIdent{Name: call.Fun[0]}
		for _, part := range call.Fun[1 : len(call.Fun)-1] {
			receiver = &syntax.BashPPSelectorExpr{X: receiver, Sel: part}
		}
		if len(call.Fun) > 2 && r.bashPPNativeExpr(receiver) {
			return true
		}
		// A method promoted from an embedded imported type is the dependency's
		// to run even though the receiver spelling names a local struct.
		if r.bashPPPromotedNativeReceiver(receiver, call.Fun[len(call.Fun)-1].Value) != nil {
			return true
		}
	}
	if len(call.Fun) == 1 {
		if r.bashPPGoSourceNativeFunc(call.Fun[0].Value) {
			return true
		}
		if value := r.bashPPNativeCellValue(call.Fun[0].Value); value != nil && value.Kind == "handle" && (value.Function || strings.HasPrefix(value.Type, "func(")) {
			return true
		}
		for alias := range r.bashPPImports {
			if strings.HasPrefix(alias, ".:") {
				if _, ok := r.bashPPLookupFunc(call); !ok {
					return true
				}
			}
		}
	}
	return false
}

func (r *Runner) bashPPGoSourceNativeFunc(name string) bool {
	if !r.bashPPGoSource || r.bashPPGoSourceFile == nil {
		return false
	}
	if r.bashPPNativeFuncNames == nil || r.bashPPNativeFuncNamesFile != r.bashPPGoSourceFile {
		r.bashPPNativeFuncNames = r.bashPPBuildNativeFuncNames()
		r.bashPPNativeFuncNamesFile = r.bashPPGoSourceFile
	}
	return r.bashPPNativeFuncNames[name]
}

// bashPPBuildNativeFuncNames collects, once per source file, the unqualified
// names that bashPPGoSourceNativeFunc recognizes. The names derive only from
// the file's static declarations and the mapped-package metadata, so the set
// is stable for the life of a given bashPPGoSourceFile. The returned map is
// never nil, so a program with no companions still caches the empty answer.
func (r *Runner) bashPPBuildNativeFuncNames() map[string]bool {
	names := make(map[string]bool)
	_, funcs, _, _, _ := r.bashPPGoSourceNativeCompanions(r.bashPPGoSourceSourceDir())
	for _, fn := range funcs {
		names[fn.Name] = true
	}
	mapped, _ := r.bashPPGoSourceMappedCompanions()
	for _, pkg := range mapped {
		for _, fn := range pkg.Funcs {
			names[fn.RuntimeName] = true
		}
	}
	return names
}
func (r *Runner) bashPPBridgeCall(ctx context.Context, call *syntax.BashPPCall) ([]bashPPBridgeValue, error) {
	// A reflected method value of an original receiver runs here; see
	// bashpp_s248_reflected_method.go.
	if values, claimed, err := r.goSourceLocalReflectCall(call); claimed {
		return values, err
	}
	// The integer sync/atomic functions address interpreter storage, so they
	// are answered here rather than prepared as a dependency request; see
	// gosource_atomic.md.
	if values, claimed, err := r.goSourceAtomicCall(call); claimed {
		return values, err
	}
	// unsafe.String reads interpreter-owned byte storage through an original
	// pointer, so it is answered here as well.
	if values, claimed, err := r.goSourceUnsafeStringCall(call); claimed {
		return values, err
	}
	// unsafe.Slice over a native indexed pointer must remain in the dependency
	// process so the resulting slice keeps the exact mapped backing store.
	if values, claimed, err := r.goSourceUnsafeSliceCall(call); claimed {
		return values, err
	}
	// runtime.Gosched yields the interpreter's own scheduler; see
	// gosource_s247_gosched.go.
	if values, claimed, err := r.goSourceGoschedCall(call); claimed {
		return values, err
	}
	// Stack introspection reads the interpreter's own frames; see
	// bashpp_sprint162_nilptr2_stack.go.
	if values, claimed, err := r.goSourceRuntimeStackCall(call); claimed {
		return values, err
	}
	// Finalizers are kept on the interpreter's own allocations; see
	// gosource_finalizer.go.
	if values, claimed, err := r.goSourceFinalizerCall(ctx, call); claimed {
		return values, err
	}
	if values, claimed, err := r.goSourceExecutableCall(ctx, call); claimed {
		return values, err
	}
	if values, claimed, err := r.goSourceLocalFmtWriterCall(ctx, call); claimed {
		return values, err
	}
	q, err := r.bashPPPrepareNativeCall(ctx, call)
	if err != nil {
		return nil, err
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return nil, err
	}
	r.bashPPReflectValueReceiver(req, call, &q)
	if !r.goSourceNativeSleepBoundary(ctx, req, q) {
		return nil, errBashPPScalarInterrupted
	}
	values, err := r.bashPPNativeRequest(ctx, req, q)
	if err != nil {
		return values, err
	}
	return r.bashPPNativeFunctionResults(req, call, values)
}

func (r *Runner) bashPPNativeFunctionResults(req bashPPEvalRequest, call *syntax.BashPPCall, values []bashPPBridgeValue) ([]bashPPBridgeValue, error) {
	if call == nil || call.ResultFuncType == nil {
		return values, nil
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("gosource: native function result has invalid arity")
	}
	if req.Bridge == nil || values[0].Kind != "handle" || values[0].Handle == 0 || values[0].Session != req.Bridge.id {
		return nil, fmt.Errorf("gosource: native function result lost its authenticated handle")
	}
	if values[0].Function || strings.HasPrefix(values[0].Type, "func(") || strings.HasPrefix(values[0].NativeType, "func(") {
		values[0].Type = bashPPTypeText(call.ResultFuncType)
		return values, nil
	}
	return nil, fmt.Errorf("gosource: native function result has invalid signature")
}

func (r *Runner) goSourceUnsafeSliceCall(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || call == nil || len(call.Fun) != 2 || call.Fun[1].Value != "Slice" || r.bashPPImports[call.Fun[0].Value] != "unsafe" || len(call.ArgExprs) != 2 || call.Ellipsis.IsValid() {
		return nil, false, nil
	}
	address, ok := call.ArgExprs[0].(*syntax.BashPPAddressExpr)
	if !ok {
		return nil, false, nil
	}
	index, ok := address.X.(*syntax.BashPPIndexExpr)
	if !ok || !r.bashPPNativeExpr(index.X) {
		return nil, false, nil
	}
	ptr, _, err := r.bashPPNativeIndexedPointer(index)
	if err != nil {
		return nil, true, err
	}
	length, err := r.bashPPEvalScalarExpr(call.ArgExprs[1])
	if err != nil {
		return nil, true, err
	}
	count, ok := constant.Int64Val(constant.ToInt(length.value))
	if !ok || count < 0 {
		return nil, true, fmt.Errorf("runtime error: unsafe.Slice: len out of range")
	}
	value, err := r.bashPPNativeAccess(r.ectx, "unsafe-slice", ptr, "", bashPPBridgeValue{Kind: "int", Text: fmt.Sprint(count)})
	return []bashPPBridgeValue{value}, true, err
}

// bashPPReflectValueReceiver gives the reviewed reflect.ValueOf(local) path an
// authenticated route back to the addressable interpreter cell. The worker
// carries this origin through Method/MethodByName and Interface, so a later
// invocation can run the original body while that request remains parked.
func (r *Runner) bashPPReflectValueReceiver(req bashPPEvalRequest, call *syntax.BashPPCall, q *bashPPBridgeRequest) {
	if q == nil || q.Receiver != nil || len(q.Args) != 1 || len(call.ArgExprs) != 1 {
		return
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || req.Imports[alias] != "reflect" || name != "ValueOf" || q.Args[0].Origin != 0 {
		return
	}
	var addressable func(syntax.BashPPExpr) *bashPPCell
	addressable = func(expr syntax.BashPPExpr) *bashPPCell {
		switch x := expr.(type) {
		case *syntax.BashPPParenExpr:
			return addressable(x.X)
		case *syntax.BashPPIdent:
			if r.bashPPScope == nil {
				return nil
			}
			cell := r.bashPPScope.lookup(x.Name.Value)
			// One read of the payload word: testing it and then dereferencing
			// it again could unwrap a different store's interface value.
			if iface := cell.viewInterface(); iface != nil && !iface.nilIface {
				return iface.cell
			}
			return cell
		}
		return nil
	}
	cell := addressable(call.ArgExprs[0])
	if cell == nil {
		cell = r.bashPPReflectValueSnapshot(req, q.Args[0])
	}
	if cell == nil {
		return
	}
	// The reflect.Value being built describes one value, so the declared type
	// and the type name that stands in for it are taken together.
	snapshot := cell.view()
	elem := snapshot.declType
	typeName := snapshot.typeName
	if typeName == "" {
		typeName = bashPPTypeText(elem)
	}
	typeName = strings.TrimPrefix(strings.TrimPrefix(typeName, "*"), "main.")
	if elem == nil && typeName != "" {
		elem = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: typeName}}
	}
	for _, local := range req.localTypePlan().transported(typeName) {
		if len(local.Methods) > 0 {
			origin := bashPPTransportOrigin(req.Bridge, &bashPPPointer{target: cell, elem: elem})
			q.Args[0].Origin, q.Args[0].Session = origin, req.Bridge.id
			return
		}
	}
}
func (r *Runner) bashPPPrepareNativeCall(ctx context.Context, call *syntax.BashPPCall) (bashPPBridgeRequest, error) {
	if !r.bashPPBridgeHandles(call) {
		return bashPPBridgeRequest{}, fmt.Errorf("gosource: call is not an imported dependency operation")
	}
	q := bashPPBridgeRequest{Op: "call", Spread: call.Ellipsis.IsValid(), Args: make([]bashPPBridgeValue, 0, len(call.ArgExprs))}
	if r.bashPPGoSourceFile != nil && call.Pos().IsValid() {
		if source, ok := r.bashPPGoSourceFile.SourceAt(call.Pos()); ok {
			q.SourceFile = source.Name
			q.SourceLine = int(call.Pos().Line())
			q.sourceProgram = source.PackagePath == ""
		}
	}
	if selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		receiver, err := r.bashPPNativeMethodReceiver(selector.X, selector.Sel.Value)
		if err != nil {
			return bashPPBridgeRequest{}, err
		}
		q.Receiver = &receiver
		q.Selector = selector.Sel.Value
	} else if len(call.Fun) >= 2 {
		if _, ok := r.bashPPImports[call.Fun[0].Value]; ok && len(call.Fun) == 2 {
			q.Selector = call.Fun[0].Value + "." + call.Fun[1].Value
			if len(call.TypeArgs) > 0 {
				// An imported generic function is registered per
				// instantiation; the call names the instantiation under the
				// enclosing frame's type bindings, beside the selector every
				// host-side policy reads. See
				// bashpp_sprint171_imported_instances.go.
				args := make([]syntax.BashPPTypeExpr, len(call.TypeArgs))
				for i, arg := range call.TypeArgs {
					args[i] = r.bashPPBindTypeExpr(arg.ArgType)
				}
				q.Instance = bashPPImportedInstanceSuffix(args)
			}
		} else {
			var receiverExpr syntax.BashPPExpr = &syntax.BashPPIdent{Name: call.Fun[0]}
			for _, part := range call.Fun[1 : len(call.Fun)-1] {
				receiverExpr = &syntax.BashPPSelectorExpr{X: receiverExpr, Sel: part}
			}
			receiver, err := r.bashPPNativeMethodReceiver(receiverExpr, call.Fun[len(call.Fun)-1].Value)
			if err != nil {
				return bashPPBridgeRequest{}, err
			}
			q.Receiver = &receiver
			q.Selector = call.Fun[len(call.Fun)-1].Value
		}
	} else if value := r.bashPPNativeCellValue(call.Fun[0].Value); value != nil && value.Kind == "handle" && (value.Function || strings.HasPrefix(value.Type, "func(")) {
		copy := *value
		q.Receiver = &copy
	} else {
		q.Selector = call.Fun[0].Value
	}
	if q.Receiver == nil {
		if alias, name, ok := strings.Cut(q.Selector, "."); ok && r.bashPPImports[alias] == "log" {
			switch name {
			case "Print", "Println", "Printf":
				q.LogPrint = name
			}
		}
	}
	if len(call.ArgExprs) != len(call.Args) {
		return bashPPBridgeRequest{}, fmt.Errorf("gosource: missing evaluated dependency arguments")
	}
	if len(call.ArgExprs) == 1 {
		if inner, ok := call.ArgExprs[0].(*syntax.BashPPCall); ok {
			if r.bashPPBridgeHandles(inner) {
				values, err := r.bashPPBridgeCall(ctx, inner)
				if err != nil {
					return bashPPBridgeRequest{}, err
				}
				q.Args = append(q.Args, values...)
				return q, nil
			}
			if fn, ok := r.bashPPLookupFunc(inner); ok {
				cells, err := r.goSourceCallResultCells(inner, fn)
				if err != nil {
					return bashPPBridgeRequest{}, err
				}
				for _, cell := range cells {
					value, err := r.bashPPBridgeCell(cell)
					if err != nil {
						return bashPPBridgeRequest{}, err
					}
					q.Args = append(q.Args, value)
				}
				return q, nil
			}
		}
	}
	localCallback := false
	if alias, name, ok := strings.Cut(q.Selector, "."); ok && q.Receiver == nil && r.bashPPImports[alias] == "slices" {
		switch name {
		case "SortFunc", "SortStableFunc", "EqualFunc", "IndexFunc", "ContainsFunc":
			localCallback = true
		}
	}
	for i, expr := range call.ArgExprs {
		r.goSourceReflectingFunction = len(call.ArgExprs) == 1 && goSourceReflectValueOfOperand(r.bashPPImports, q, expr)
		r.goSourceLocalCallbackArg = localCallback && i == len(call.ArgExprs)-1
		value, err := r.bashPPBridgeExpr(expr)
		r.goSourceReflectingFunction = false
		r.goSourceLocalCallbackArg = false
		if err != nil {
			var positioned *goSourceError
			if errors.As(err, &positioned) {
				return bashPPBridgeRequest{}, err
			}
			return bashPPBridgeRequest{}, &goSourceError{prefix: r.bashErrPrefix(expr.Pos()), err: err}
		}
		q.Args = append(q.Args, value)
	}
	q.argCells = r.bashPPNativeArgCells(call.ArgExprs, q.Args)
	q.transferProof = call.ExclusiveSliceArgs
	return q, nil
}
func (r *Runner) bashPPBridgeScalar(expr syntax.BashPPExpr) (bashPPScalar, bool, error) {
	if !r.bashPPGoSource {
		return bashPPScalar{}, false, nil
	}
	handled := false
	switch e := expr.(type) {
	case *syntax.BashPPCall:
		handled = r.bashPPBridgeHandles(e)
	case *syntax.BashPPSelectorExpr:
		handled = r.bashPPNativeExpr(e)
	case *syntax.BashPPIndexExpr:
		handled = r.bashPPNativeExpr(e.X)
	case *syntax.BashPPSliceExpr:
		handled = r.bashPPNativeExpr(e.X)
	case *syntax.BashPPDerefExpr:
		handled = r.bashPPNativeExpr(e.X)
	}
	if !handled {
		return bashPPScalar{}, false, nil
	}
	value, err := r.bashPPBridgeExpr(expr)
	if err != nil {
		return bashPPScalar{}, true, err
	}
	scalar, err := value.scalar()
	return scalar, true, err
}
func (value bashPPBridgeValue) scalar() (bashPPScalar, error) {
	scalar := bashPPScalar{typ: value.Type, runtime: true}
	switch value.Kind {
	case "string":
		scalar.value = constant.MakeString(value.stringText())
	case "bool":
		scalar.value = constant.MakeBool(value.Text == "true")
	case "int", "uint":
		scalar.value = constant.MakeFromLiteral(value.Text, token.INT, 0)
	case "complex":
		if special, ok := bashPPNonFiniteComplexText(value.Text); ok {
			return bashPPNonFiniteComplexScalar(special, value.Type), nil
		}
		scalar.value = bashPPParseComplex(value.Text)
	case "float":
		if bits, ok := bashPPNaNBitsScalar(value.Text, value.Type); ok {
			return bits, nil
		}
		// math.NaN() and math.Inf(1) cross as the text go/constant cannot
		// hold; they are the runtime non-finite scalar, not an invalid one.
		if special, ok := bashPPNonFiniteText(value.Text); ok {
			return bashPPNonFiniteScalar(special, value.Type), nil
		}
		scalar.value = constant.MakeFromLiteral(value.Text, token.FLOAT, 0)
		if number, err := strconv.ParseFloat(value.Text, 64); err == nil {
			scalar.negativeZero = number == 0 && math.Signbit(number)
		}
	default:
		return scalar, fmt.Errorf("gosource: native %s (%s) is not scalar", value.Kind, value.Type)
	}
	if scalar.value == nil || scalar.value.Kind() == constant.Unknown {
		return scalar, fmt.Errorf("gosource: invalid native scalar")
	}
	return scalar, nil
}
func bridgeScalar(value bashPPScalar) (bashPPBridgeValue, error) {
	out := bashPPBridgeValue{Type: value.typ}
	if value.hasNonFinite {
		return bashPPBridgeValue{Kind: "float", Type: value.typ, Text: bashPPScalarStorageString(value)}, nil
	}
	if value.hasNonFiniteComplex {
		return bashPPBridgeValue{Kind: "complex", Type: value.typ, Text: strconv.FormatComplex(value.nonFiniteComplex, 'g', -1, 128)}, nil
	}
	if value.value == nil {
		return out, fmt.Errorf("gosource: absent scalar value")
	}
	switch value.value.Kind() {
	case constant.String:
		out = bashPPBridgeString(constant.StringVal(value.value))
		out.Type = value.typ
	case constant.Bool:
		out.Kind = "bool"
		out.Text = strconv.FormatBool(constant.BoolVal(value.value))
	case constant.Int:
		out.Kind = "int"
		out.Text = value.value.ExactString()
		if strings.HasPrefix(value.typ, "uint") || value.typ == "byte" {
			out.Kind = "uint"
		}
	case constant.Complex:
		out.Kind = "complex"
		out.Text = strconv.FormatComplex(bashPPComplexNumber(value.value), 'g', -1, 128)
	case constant.Float:
		out.Kind = "float"
		number, _ := constant.Float64Val(value.value)
		out.Text = strconv.FormatFloat(number, 'g', -1, 64)
		if value.negativeZero && number == 0 {
			out.Text = "-0"
		}
	default:
		return out, fmt.Errorf("gosource: unsupported native scalar kind %s", value.value.Kind())
	}
	return out, nil
}
func (r *Runner) bashPPBridgeExpr(expr syntax.BashPPExpr) (bashPPBridgeValue, error) {
	if lit, ok := expr.(*syntax.BashPPFuncLit); ok && r.bashPPGoSource {
		// A literal used only as a native-call argument needs no shell-visible
		// closure handle. The dependency session owns it for exactly as long as
		// its callback registration; appending it to the Runner's lifetime-long
		// closure registry made loops of t.Run retain every completed subtest.
		return r.bashPPBridgeFunction(r.bashPPNewClosure(lit))
	}
	callable := false
	switch x := expr.(type) {
	case *syntax.BashPPFuncLit, *syntax.BashPPIdent:
		callable = true
	case *syntax.BashPPSelectorExpr:
		// A method value the program owns — `t.M` handed to sync.Once.Do —
		// crosses as the bound closure it denotes, not as a field read.
		callable = x.MethodValue && !r.bashPPNativeExpr(x.X)
	}
	if callable {
		if cell, handled, err := r.goSourceCallableCell(expr); handled {
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			if fn, ok := r.bashPPClosure(cell.vr.Str); ok {
				return r.bashPPBridgeFunction(fn)
			}
		}
	}
	if value, ok, err := r.goSourceTypedNilBridgeValue(expr); ok {
		return value, err
	}
	if value, handled, err := r.goSourceRecoverBridgeValue(expr); handled {
		return value, err
	}
	switch x := expr.(type) {
	case *syntax.BashPPBasicLit:
		if x.Value != nil {
			switch x.Kind {
			case "STRING":
				v := constant.MakeFromLiteral(x.Value.Value, token.STRING, 0)
				if v.Kind() == constant.String {
					return bashPPBridgeString(constant.StringVal(v)), nil
				}
			case "INT":
				v := constant.MakeFromLiteral(x.Value.Value, token.INT, 0)
				if v.Kind() == constant.Int {
					return bashPPBridgeValue{Kind: "int", Text: v.ExactString()}, nil
				}
			case "FLOAT":
				v := constant.MakeFromLiteral(x.Value.Value, token.FLOAT, 0)
				if v.Kind() == constant.Float {
					num, _ := constant.Float64Val(v)
					return bashPPBridgeValue{Kind: "float", Text: strconv.FormatFloat(num, 'g', -1, 64)}, nil
				}
			case "CHAR":
				v := constant.MakeFromLiteral(x.Value.Value, token.CHAR, 0)
				if v.Kind() == constant.Int {
					return bashPPBridgeValue{Kind: "int", Text: v.ExactString()}, nil
				}
			}
		}
	case *syntax.BashPPUnaryExpr:
		if cell, handled, err := r.goSourceChannelValueCell(x); handled {
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			return r.bashPPBridgeCell(cell)
		}
	case *syntax.BashPPParenExpr:
		return r.bashPPBridgeExpr(x.X)
	case *syntax.BashPPConvertExpr:
		// `unsafe.Pointer(&b[0])` names interpreter storage; it crosses as the
		// pointer it is, not as a scalar. Integer and forged operands keep the
		// scalar path below.
		if r.bashPPGoSource && r.goSourceUnsafePointerType(r.bashPPConvertTarget(x)) &&
			!goSourceNilLiteral(x.X) && !r.goSourceUnsafeIntegerOperand(x.X) && !r.goSourceUnsafeReflectAddressOperand(x.X) {
			ptr, _, claimed, err := r.goSourceUnsafePointerExpr(x)
			if claimed {
				if err != nil {
					return bashPPBridgeValue{}, err
				}
				return r.bashPPBridgePointerValue(ptr)
			}
		}
	case *syntax.BashPPTypeAssertExpr:
		if r.goSourceNativeTypeAssert(x) {
			_, cell, err := r.bashPPTypeAssert(x, false)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			return r.bashPPBridgeCell(cell)
		}
	case *syntax.BashPPIdent:
		if x.Name.Value == "nil" {
			return bashPPBridgeValue{Kind: "nil"}, nil
		}
		if r.bashPPScope != nil {
			if cell := r.bashPPScope.lookup(x.Name.Value).view(); cell != nil {
				switch {
				case cell.pointer || cell.interfaceValue != nil:
					return r.bashPPBridgeCell(cell)
				case cell.vr.Kind == expand.Object:
					if value, ok := cell.vr.Obj.(*bashPPBridgeValue); ok {
						return *value, nil
					}
					return r.bashPPBridgeCollection(cell.vr.Obj, cell.valueMeta, cell.declType)
				}
			} else {
				if x.Name.Value == "true" {
					return bashPPBridgeValue{Kind: "bool", Text: "true"}, nil
				}
				if x.Name.Value == "false" {
					return bashPPBridgeValue{Kind: "bool", Text: "false"}, nil
				}
			}
		} else {
			if x.Name.Value == "true" {
				return bashPPBridgeValue{Kind: "bool", Text: "true"}, nil
			}
			if x.Name.Value == "false" {
				return bashPPBridgeValue{Kind: "bool", Text: "false"}, nil
			}
		}
	case *syntax.BashPPCall:
		// `fmt.Println(recover())`: the recovered value crosses as the
		// interface value it is, with its dynamic type.
		if r.bashPPPredeclaredRecover(x) {
			iv, _ := r.bashPPRecoverInterfaceValue()
			return r.bashPPBridgeCell(&bashPPCell{declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}, interfaceValue: iv})
		}
		if values, claimed, err := r.goSourceUnsafeSliceCall(x); claimed {
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			if len(values) != 1 {
				return bashPPBridgeValue{}, fmt.Errorf("unsafe.Slice returned %d values", len(values))
			}
			return values[0], nil
		}
		if r.bashPPBridgeHandles(x) {
			values, err := r.bashPPBridgeCall(r.ectx, x)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			if r.exit.exiting {
				return bashPPBridgeValue{}, errBashPPNativeExited
			}
			if len(values) != 1 {
				return bashPPBridgeValue{}, fmt.Errorf("gosource: expression requires one native result, got %d", len(values))
			}
			return values[0], nil
		}
		if r.bashPPGoSource {
			if fn, ok := r.bashPPLookupFunc(x); ok {
				cells, err := r.goSourceCallResultCells(x, fn)
				if err != nil {
					return bashPPBridgeValue{}, err
				}
				if len(cells) != 1 {
					return bashPPBridgeValue{}, fmt.Errorf("gosource: expression requires one local result, got %d", len(cells))
				}
				return r.bashPPBridgeCell(cells[0])
			}
			if r.exit.exiting || r.exit.fatalExit || r.exit.err != nil {
				return bashPPBridgeValue{}, errBashPPScalarInterrupted
			}
		}
	case *syntax.BashPPSelectorExpr:
		if value := r.bashPPNativeLocalField(x); value != nil {
			return *value, nil
		}
		if r.bashPPNativeExpr(x.X) {
			base, err := r.bashPPNativeReceiver(x.X)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			value, err := r.bashPPNativeAccess(r.ectx, "member", base, x.Sel.Value)
			if err == nil && value.Kind == "handle" && strings.HasPrefix(value.Type, "func(") && base.NativeType != "" {
				value.Callable = base.NativeType + "." + x.Sel.Value
			}
			return value, err
		}
		if id, ok := x.X.(*syntax.BashPPIdent); ok {
			if _, imported := r.bashPPImports[id.Name.Value]; imported {
				req, err := r.bashPPEvalRequest()
				if err != nil {
					return bashPPBridgeValue{}, err
				}
				values, err := r.bashPPNativeRequest(r.ectx, req, bashPPBridgeRequest{Op: "get", Selector: id.Name.Value + "." + x.Sel.Value})
				if err != nil {
					return bashPPBridgeValue{}, err
				}
				if len(values) != 1 {
					return bashPPBridgeValue{}, fmt.Errorf("gosource: native symbol returned no value")
				}
				value := values[0]
				if value.Kind == "handle" && strings.HasPrefix(value.Type, "func(") {
					value.Callable = r.bashPPImports[id.Name.Value] + "." + x.Sel.Value
				}
				return value, nil
			}
		}
	case *syntax.BashPPIndexExpr:
		if r.bashPPNativeExpr(x.X) {
			return r.bashPPNativeIndex(x)
		}
		if typ, ok := r.goSourceStaticExprType(x); ok && r.bashPPNativeType(typ) {
			value, meta, err := r.bashPPReadExpr(x)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			native, ok := value.(*bashPPBridgeValue)
			if !ok && meta == nil && bashPPPlainScalarElement(value) {
				// A dependency-defined basic type (go/token.Token) is held in
				// the interpreter's collection as the scalar it is; it crosses
				// through the scalar path below with its declared identity.
				break
			}
			if !ok && meta != nil && meta.kind == "pointer" {
				ptr, isPointer := value.(*bashPPPointer)
				if value == nil || isPointer {
					return r.bashPPBridgePointerValue(ptr)
				}
			}
			if !ok || native == nil || meta == nil {
				return bashPPBridgeValue{}, fmt.Errorf("gosource: native collection element lost its authenticated handle")
			}
			checked, _, err := r.goSourceNativeAssignedValue(*native, typ)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			return *checked.(*bashPPBridgeValue), nil
		}
	case *syntax.BashPPSliceExpr:
		if r.bashPPNativeExpr(x.X) {
			return r.bashPPNativeSlice(x)
		}
	case *syntax.BashPPDerefExpr:
		if r.bashPPNativeExpr(x.X) {
			base, err := r.bashPPBridgeExpr(x.X)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			return r.bashPPNativeAccess(r.ectx, "deref", base, "")
		}
		ptr, err := r.bashPPPointerExprValue(x.X)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		if ptr == nil {
			return bashPPBridgeValue{}, r.goSourceRuntimeFault(errBashPPNilDereference)
		}
		value, meta, typ, err := ptr.read()
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		return r.bashPPBridgeCollection(value, meta, typ)
	case *syntax.BashPPAddressExpr:
		if lit, ok := x.X.(*syntax.BashPPCompositeLit); ok && r.bashPPNativeType(lit.LitType) {
			return r.bashPPNativeComposite(lit, true)
		}
		if r.goSourceNativeAddressable(x.X) {
			return r.goSourceNativeAddress(x.X)
		}
		// An indexed element of a dependency-owned slice is addressable in
		// that dependency. Preserve that address instead of trying to build an
		// interpreter pointer over the (deliberately opaque) native handle.
		if index, ok := x.X.(*syntax.BashPPIndexExpr); ok {
			if value, handled, err := r.bashPPNativeIndexedPointer(index); handled {
				return value, err
			}
		}
		ptr, err := r.bashPPPointerExprValue(x)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		return r.bashPPBridgePointerValue(ptr)
	case *syntax.BashPPNewExpr:
		// `new(T)` is the pointer it allocates, exactly as `&T{}` is.
		ptr, err := r.bashPPPointerExprValue(x)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		return r.bashPPBridgePointerValue(ptr)
	case *syntax.BashPPCompositeLit:
		if r.bashPPNativeType(x.LitType) {
			return r.bashPPNativeComposite(x, false)
		}
		value, meta, err := r.bashPPEvalComposite(x, x.LitType)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		return r.bashPPBridgeCollection(value, meta, x.LitType)
	}
	// A predeclared value call — append(xs, 1), copy(dst, src), make(...) — is
	// implemented over cells rather than as a callable, so the scalar evaluator
	// cannot look it up; see bashPPValueBuiltinBridge in
	// bashpp_collection_bridge.go. An unclaimed name keeps the scalar path.
	if bridged, claimed, err := r.bashPPValueBuiltinBridge(expr); claimed {
		return bridged, err
	}
	// An interpreter-owned structured read — board[i], xs[1:], v.Inner — has no
	// scalar spelling and crosses as the collection it is; see
	// bashPPStructuredBridgeRead in bashpp_collection_growth.go. Scalar reads
	// report false and keep the scalar evaluator's own diagnostics below.
	if value, handled, err := r.goSourceBridgeCollectionRead(expr); handled {
		return value, err
	}
	if value, meta, ok := r.bashPPStructuredBridgeRead(expr); ok {
		return r.bashPPBridgeCollection(value, meta, meta.typ)
	}
	scalar, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	value, err := bridgeScalar(scalar)
	if err != nil {
		return value, err
	}
	value, err = r.bashPPBridgeDefinedScalar(value)
	if boxed := r.bashPPConvertBoxedType(expr, nil, value.Type); boxed != nil {
		value.Type = r.bashPPBridgeTypeIdentity(boxed)
		return value, err
	}
	return bashPPBridgeInstantiatedScalar(value, r.bashPPExprScalarType(expr)), err
}

// bashPPNativeIndexedPointer returns the dependency-owned address of one
// indexed slice element. The worker performs the bounds check and returns a
// pointer handle whose reflect.Value points into the original slice backing
// store; no interpreter-side copy or writeback is involved.
func (r *Runner) bashPPNativeIndexedPointer(index *syntax.BashPPIndexExpr) (bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || index == nil || !r.bashPPNativeExpr(index.X) {
		return bashPPBridgeValue{}, false, nil
	}
	base, err := r.bashPPBridgeExpr(index.X)
	if err != nil {
		return bashPPBridgeValue{}, true, err
	}
	key, err := r.bashPPBridgeExpr(index.Index)
	if err != nil {
		return bashPPBridgeValue{}, true, err
	}
	value, err := r.bashPPNativeAccess(r.ectx, "index-pointer", base, "", key)
	return value, true, err
}

// bashPPBridgeFloatText normalises one shell-held float, including the exact
// rational spelling the interpreter uses for a non-representable constant.
func bashPPBridgeFloatText(text string) (string, bool) {
	if _, ok := bashPPNaNBitsScalar(text, ""); ok {
		return text, true
	}
	number := constant.MakeFromLiteral(text, token.FLOAT, 0)
	if number.Kind() == constant.Unknown {
		numerator, denominator, ok := strings.Cut(text, "/")
		if !ok {
			return "", false
		}
		top := constant.MakeFromLiteral(numerator, token.FLOAT, 0)
		bottom := constant.MakeFromLiteral(denominator, token.FLOAT, 0)
		if top.Kind() == constant.Unknown || bottom.Kind() == constant.Unknown || constant.Sign(bottom) == 0 {
			return "", false
		}
		number = constant.BinaryOp(top, token.QUO, bottom)
	}
	value, _ := constant.Float64Val(number)
	return strconv.FormatFloat(value, 'g', -1, 64), true
}

// bashPPBridgeDefinedScalar re-reads a scalar the shell carries as text at the
// underlying kind of the original defined type that names it, so a value of
// `type Celsius float64` crosses the boundary as a float and keeps the
// materialised Celsius identity rather than arriving as a string.
func (r *Runner) bashPPBridgeDefinedScalar(value bashPPBridgeValue) (bashPPBridgeValue, error) {
	out, err := r.bashPPBridgeDefinedScalarKind(value)
	out.Type = r.bashPPReservedTypeSpelling(out.Type)
	return out, err
}

func (r *Runner) bashPPBridgeDefinedScalarKind(value bashPPBridgeValue) (bashPPBridgeValue, error) {
	if value.Type == "" {
		return value, nil
	}
	named := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value.Type}}
	// An alias names the defined type it denotes (type tkn = Tkn): the value
	// crosses with that type's identity, whose method set the helper knows.
	if canonical := r.bashPPCanonicalAssignableType(named); canonical != named {
		value.Type = r.bashPPBridgeTypeIdentity(canonical)
		if next, ok := canonical.(*syntax.BashPPNamedType); ok && len(next.TypeArgs) == 0 {
			named = next
		}
	}
	underlying := bashPPTypeText(r.bashPPUnderlyingType(named))
	if underlying == value.Type {
		return value, nil
	}
	switch {
	case underlying == "string":
		return value, nil
	case underlying == "bool":
		if value.Text != "true" && value.Text != "false" {
			return value, fmt.Errorf("gosource: %s value %q is not a bool", value.Type, value.Text)
		}
		value.Kind = "bool"
	case underlying == "float32" || underlying == "float64":
		// The shell may hold an exact non-integer constant in its rational
		// form, which is the interpreter's own spelling and not a Go literal.
		number, ok := bashPPBridgeFloatText(value.Text)
		if !ok {
			return value, fmt.Errorf("gosource: %s value %q is not a %s", value.Type, value.Text, underlying)
		}
		value.Kind, value.Text = "float", number
	case bashPPIntegerType(underlying):
		integer := constant.MakeFromLiteral(value.Text, token.INT, 0)
		if integer.Kind() != constant.Int || !bashPPIntegerRepresentable(underlying, integer) {
			return value, fmt.Errorf("gosource: %s value %q is not an %s", value.Type, value.Text, underlying)
		}
		value.Kind = "int"
		if strings.HasPrefix(underlying, "uint") || underlying == "byte" {
			value.Kind = "uint"
		}
	}
	return value, nil
}

// bashPPBridgePointerValue transports a pointer to an original value as the
// pointee it addresses, so the dependency observes a real *T — Go's &{1 2}
// rather than a struct. The identity resolves to the original interpreter
// storage for method callbacks. General native out-parameters fail before the
// dependency executes because native writes have no complete alias contract.
func (r *Runner) bashPPBridgePointerValue(ptr *bashPPPointer) (bashPPBridgeValue, error) {
	if ptr == nil {
		return bashPPBridgeValue{Kind: "nil"}, nil
	}
	value, meta, typ, err := ptr.read()
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	session := req.Bridge
	// The origin is known before the pointee crosses, and a pointer met
	// again on its own transport path is a back-reference, not a second
	// walk; see bashpp_sprint165_runtime2_cycle.go.
	origin := bashPPTransportOrigin(session, ptr)
	onPath, leave := r.bashPPTransportEnter(origin)
	if onPath {
		return bashPPTransportBackReference(session, origin, meta, typ), nil
	}
	defer leave()
	// Slices below this walk decode into worker-persisted storage the
	// worker keeps past the call, so their regions are pinned while the
	// walk registers them; see bashPPTransportSliceOrigin.
	session.mu.Lock()
	session.slicePinDepth++
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.slicePinDepth--
		session.mu.Unlock()
	}()
	inner, isInterface, err := r.goSourceInterfacePointee(ptr)
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	if !isInterface {
		inner, err = r.bashPPBridgeCollection(value, meta, typ)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
	}
	pointerType := "*" + inner.Type
	if isInterface {
		// The pointee is the interface variable, whatever it holds.
		pointerType = "*" + inner.Interface
	}
	return bashPPBridgeValue{Origin: origin, Session: session.id, Kind: "pointer", Type: pointerType, Elements: []bashPPBridgeValue{inner}, Within: r.bashPPTransportWithin(session, ptr), localOrigin: ptr}, nil
}

func (s *bashPPNativeSession) applyNativePointerUpdates(req bashPPEvalRequest, reply bashPPBridgeResponse) error {
	if len(reply.PtrUpdates) == 0 {
		return nil
	}
	owner := req.CallbackOwner
	if owner == nil {
		return fmt.Errorf("gosource: native pointer writeback has no request owner")
	}
	for _, update := range reply.PtrUpdates {
		if update.Origin == 0 || update.Session != s.id {
			return fmt.Errorf("gosource: native pointer writeback has invalid origin")
		}
		ptr, live := s.originLookup(update.Origin)
		if !live {
			// The worker prunes released origins before rendering
			// writebacks, but a reply already in flight may still name
			// one: its target storage is unreachable, so the write is
			// unobservable and dropped. An origin that never existed
			// still fails closed.
			if s.originExpired(update.Origin) {
				continue
			}
			return fmt.Errorf("gosource: native pointer writeback target expired")
		}
		if len(update.Elements) != 1 {
			return fmt.Errorf("gosource: native pointer writeback needs one value")
		}
		// A structural pointee may nest native values this session still owns.
		// They arrived on its own authenticated connection, so they carry its
		// identity — a handle from any other session still fails closed.
		s.bashPPAuthenticateCallbackValue(&update.Elements[0])
		if err := owner.bashPPWriteBridgePointer(ptr, update.Elements[0]); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) bashPPWriteBridgePointer(ptr *bashPPPointer, value bashPPBridgeValue) error {
	if ptr != nil && ptr.unsafeView() != nil {
		return fmt.Errorf("BASHPP-EUNSAFE-WRITE: writes through reinterpreted blank views are unsupported")
	}
	if ptr != nil && ptr.unsafeOverlay() != nil && ptr.unsafeOverlay().target.blank {
		return fmt.Errorf("BASHPP-EUNSAFE-WRITE: writes through reinterpreted blank views cannot preserve blank-field bytes")
	}
	if ptr != nil && ptr.unsafeSlice() != nil {
		return fmt.Errorf("BASHPP-EUNSAFE-WRITE: writes through a reinterpreted slice header are unsupported")
	}
	if err := goSourceUnsafeDerefCheck(ptr); err != nil {
		return err
	}
	if ptr == nil {
		return errBashPPNilDereference
	}
	if ptr.target.object != nil && ptr.target.object.readonly {
		return fmt.Errorf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through pointer", ptr.target.object.owner)
	}
	if ptr.target.constant || ptr.target.vr.ReadOnly {
		return fmt.Errorf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer")
	}
	_, _, typ, err := ptr.read()
	if err != nil {
		return err
	}
	converted, meta, err := r.bashPPBridgeContents(value, typ)
	if err != nil {
		return fmt.Errorf("gosource: native pointer writeback: %w", err)
	}
	if len(ptr.path) == 0 {
		bashPPStoreCellValue(ptr.target, converted, meta)
		return nil
	}
	parent, parentMeta, _, err := ptr.readParent()
	if err != nil {
		return err
	}
	last := ptr.path[len(ptr.path)-1]
	if last.field != "" {
		mapping, ok := parent.(map[string]any)
		if !ok {
			return fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer field path no longer names struct storage")
		}
		var layout map[string]*bashPPCollectionMeta
		if parentMeta != nil {
			layout = parentMeta.mapping
		}
		bashPPStorageSetField(mapping, layout, last.field, converted, meta)
		return nil
	}
	sequence, ok := parent.([]any)
	if !ok || last.index < 0 || last.index >= len(sequence) {
		return fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer index no longer names collection storage")
	}
	sequence[last.index] = converted
	if parentMeta != nil {
		parentMeta.sequence[last.index] = meta
	}
	return nil
}

// bashPPBridgeIntegerCarrier reports the numeric wire kind for a collection
// element carried as its decimal spelling. Only a well-formed integer literal
// that is representable in the destination integer type qualifies, so an
// ordinary string element (declared type string, or a named string type) is
// left to cross as text. This is the large-unsigned carrier — a []uint64
// element above math.MaxInt64 that the signed int carrier cannot hold.
func (r *Runner) bashPPBridgeIntegerCarrier(typ syntax.BashPPTypeExpr, text string) (kind string, ok bool) {
	name, isName := r.bashPPUnderlyingType(typ).(*syntax.BashPPNamedType)
	if !isName {
		return "", false
	}
	dest, ok := r.bashPPUnderlyingIntegerName(name.Name.Value)
	if !ok || !bashPPCollectionIntegerText(dest, text) {
		return "", false
	}
	switch dest {
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
		return "uint", true
	default:
		return "int", true
	}
}

func (r *Runner) bashPPBridgeCollection(value any, meta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) (bashPPBridgeValue, error) {
	if meta != nil && meta.interfaceValue != nil {
		cell := meta.interfaceValue.cell
		if meta.interfaceValue.nilIface || cell == nil {
			return bashPPBridgeValue{Kind: "nil"}, nil
		}
		if bashPPRuntimeErrorType(meta.interfaceValue.dynamic) {
			return bashPPBridgeString(bashPPRuntimeErrorText(meta.interfaceValue)), nil
		}
		if cell.pointer {
			if r.bashPPGoSource {
				return r.bashPPBridgeCell(cell)
			}
			return r.bashPPBridgePointerValue(cell.pointerValue)
		}
		if cell.vr.Kind == expand.Object {
			return r.bashPPBridgeCollection(cell.vr.Obj, cell.valueMeta, cell.declType)
		}
		if bashPPRuntimeErrorType(cell.declType) {
			return bashPPBridgeString(cell.vr.String()), nil
		}
		scalar, err := bridgeScalar(r.bashPPScalarFromCell(cell))
		return bashPPBridgeInstantiatedScalar(scalar, cell.declType), err
	}
	if meta != nil && meta.typ != nil {
		typ = meta.typ
	}
	result := bashPPBridgeValue{Type: r.bashPPBridgeTypeIdentity(typ)}
	switch value := value.(type) {
	case *bashPPBridgeValue:
		if value == nil || !r.bashPPGoSource {
			return bashPPBridgeValue{}, fmt.Errorf("gosource: missing native field value")
		}
		return *value, nil
	case *bashPPPointer:
		// An element or field holding a pointer to original storage — the
		// []*T shape — crosses as the pointee it addresses, with the identity
		// that lets a method callback bind back to that same storage.
		if !r.bashPPGoSource {
			return result, fmt.Errorf("gosource: unsupported interpreter collection value %T", value)
		}
		return r.bashPPBridgePointerValue(value)
	case []any:
		result.Kind = "slice"
		visibleLength := bashPPSequenceLen(meta, value)
		viewCapacity := bashPPSequenceCap(meta, value)
		var children []*bashPPCollectionMeta
		if meta != nil {
			children = meta.sequence
		}
		inferredArray := false
		if meta != nil {
			result.Kind = meta.kind
			if result.Kind == "inferred-array" {
				result.Kind = "array"
				inferredArray = true
			}
		}
		collection, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
		if !ok {
			return result, fmt.Errorf("gosource: missing collection element identity")
		}
		// An inferred-length array literal ([...]T) carries the placeholder "..."
		// in its declared type. The dependency's reflect-based type resolver only
		// parses a concrete length, so emit the realised element count instead.
		if inferredArray {
			result.Type = "[" + strconv.Itoa(len(value)) + "]" + r.bashPPBridgeTypeIdentity(collection.Element)
		}
		// A named array type the helper does not materialise — its length is a
		// constant name or expression the helper cannot evaluate — still has a
		// realised length here. Transport the structural spelling the resolver
		// can parse, exactly like the inferred-length form above.
		if result.Kind == "array" && !inferredArray && r.bashPPGoSource && !r.bashPPBridgeResolvableArrayType(typ, collection) {
			result.Type = "[" + strconv.Itoa(len(value)) + "]" + r.bashPPBridgeTypeIdentity(collection.Element)
		}
		// Byte arrays cross in compact form: one value per element would
		// inflate a 10 MB buffer to gigabytes of JSON, decoded garbage
		// and pointee snapshots. Only a byte element selects it, and only
		// a fully extractable image compacts; anything else keeps the
		// per-element path below.
		if result.Kind == "array" && r.bashPPGoSource && bashPPByteElementType(r, collection.Element) {
			if raw, ok := bashPPByteArrayBytes(value); ok {
				return bashPPBridgeValue{Kind: "bytes", Type: result.Type, Bytes: raw}, nil
			}
		}
		if r.bashPPGoSource && result.Kind == "slice" {
			if visibleLength < 0 || viewCapacity < visibleLength || viewCapacity > cap(value) || (meta != nil && viewCapacity > cap(children)) {
				return result, fmt.Errorf("gosource: invalid interpreter slice shape len=%d cap=%d", visibleLength, viewCapacity)
			}
			view := value[:visibleLength:viewCapacity]
			result.sliceView = &bashPPNativeSlice{view: view, meta: meta, typ: typ}
			result.Storage, result.Offset = bashPPTransportSliceOrigin(r.bashPPTools.bridge, view, meta, typ)
			result.Length = visibleLength
			result.Capacity = viewCapacity
			value = view[:viewCapacity]
			// Capacity-tail elements are observable after reslicing. Their
			// zero-value metadata lives in the metadata backing array even though
			// its logical length, like the payload's, stops at visibleLength.
			if meta != nil {
				children = children[:viewCapacity]
			}
		}
		result.Elements = make([]bashPPBridgeValue, 0, len(value))
		for i, item := range value {
			var child *bashPPCollectionMeta
			if i < len(children) {
				child = children[i]
			}
			converted, err := r.bashPPBridgeCollection(item, child, collection.Element)
			if err != nil {
				return result, err
			}
			if converted.Kind == "nil" && result.Kind == "slice" {
				if zero, ok := bridgeZeroScalar(bashPPTypeText(r.bashPPUnderlyingType(collection.Element))); ok {
					converted = zero
				}
			}
			result.Elements = append(result.Elements, converted)
		}
		return result, nil
	case map[string]any:
		switch shape := r.bashPPUnderlyingType(typ).(type) {
		case *syntax.BashPPCollectionType:
			if shape.Kind != "map" {
				return result, fmt.Errorf("gosource: mapping without map type")
			}
			result.Kind = "map"
			if bashPPSprint165MapHasTypedKeys(meta) {
				for _, entry := range bashPPSprint165MapEntries(meta) {
					keyValue, err := r.bashPPBridgeCollection(entry.key, entry.keyMeta, shape.Key)
					if err != nil {
						return result, err
					}
					item, child, found := bashPPSprint165MapEntryValue(value, meta, entry.storage)
					if !found {
						continue
					}
					converted, err := r.bashPPBridgeCollection(item, child, shape.Element)
					if err != nil {
						return result, err
					}
					result.Entries = append(result.Entries, bashPPBridgeEntry{Key: keyValue, Value: converted})
				}
				return result, nil
			}
			for key, item := range bashPPStorageSnapshot(value) {
				keyValue := bashPPBridgeValue{Type: bashPPTypeText(shape.Key), Text: key}
				switch bashPPTypeText(r.bashPPUnderlyingType(shape.Key)) {
				case "string":
					keyValue.Kind = "string"
					keyValue.Bytes = []byte(key)
				case "bool":
					keyValue.Kind = "bool"
				default:
					keyValue.Kind = "int"
				}
				var child *bashPPCollectionMeta
				if meta != nil {
					child = bashPPLayoutGet(meta.mapping, key)
				}
				converted, err := r.bashPPBridgeCollection(item, child, shape.Element)
				if err != nil {
					return result, err
				}
				result.Entries = append(result.Entries, bashPPBridgeEntry{Key: keyValue, Value: converted})
			}
			return result, nil
		case *syntax.BashPPStructType:
			result.Kind = "struct"
			result.Fields = map[string]bashPPBridgeValue{}
			// Flattening includes embedded fields under their promoted names,
			// which is where the interpreter keeps their storage; the worker's
			// FieldByName and the generated codecs address the same names.
			for name, fieldType := range bashPPFieldSequence(shape.Fields) {
				// Blank fields have layout but no addressable storage. The
				// native declaration supplies their zero values.
				if r.bashPPGoSource && name == "_" {
					continue
				}
				item, exists := bashPPStorageGet(value, name)
				if !exists {
					return result, fmt.Errorf("gosource: missing struct field %s", name)
				}
				var child *bashPPCollectionMeta
				if meta != nil {
					child = bashPPLayoutGet(meta.mapping, name)
				}
				converted, err := r.bashPPBridgeCollection(item, child, fieldType)
				if err != nil {
					return result, err
				}
				result.Fields[name] = converted
			}
			return result, nil
		default:
			return result, fmt.Errorf("gosource: missing mapping type schema")
		}
	case string:
		if meta != nil && meta.kind == "func" {
			cell := &bashPPCell{
				vr:       expand.Variable{Set: true, Kind: expand.String, Str: value},
				declType: typ,
			}
			return r.bashPPBridgeCell(cell)
		}
		// Scalar collection storage is text-shaped. Recover its wire kind from
		// the declared element type before transport; otherwise complex and
		// defined numeric elements become Go strings at the native boundary.
		underlying := bashPPTypeText(r.bashPPUnderlyingType(typ))
		if (underlying == "complex64" || underlying == "complex128") && bashPPSprint162ComplexCollectionText(value) {
			result.Kind = "complex"
		} else if underlying == "float32" || underlying == "float64" {
			// A float element keeps its float wire kind: the dependency's
			// decoder refuses a string for a float64 slot, and NaN/Inf
			// spellings are floats, not integer carriers.
			result.Kind = "float"
		} else if kind, ok := r.bashPPBridgeIntegerCarrier(typ, value); ok {
			result.Kind = kind
		} else {
			result.Kind = "string"
		}
		result.Text = value
		if result.Kind == "string" {
			result.Bytes = []byte(value)
		}
		var err error
		result, err = r.bashPPBridgeDefinedScalar(result)
		if err != nil {
			return result, err
		}
	case bool:
		result.Kind = "bool"
		result.Text = strconv.FormatBool(value)
	case int:
		result.Kind = "int"
		result.Text = strconv.Itoa(value)
	case int64:
		result.Kind = "int"
		result.Text = strconv.FormatInt(value, 10)
	case float64:
		result.Kind = "float"
		result.Text = strconv.FormatFloat(value, 'g', -1, 64)
	case complex64:
		result.Kind = "complex"
		result.Text = strconv.FormatComplex(complex128(value), 'g', -1, 64)
	case complex128:
		result.Kind = "complex"
		result.Text = strconv.FormatComplex(value, 'g', -1, 128)
	case nil:
		// A nil slice or map keeps its declared type and zero state; see
		// bashPPNilCollectionBridge in bashpp_collection_growth.go.
		if nilValue, ok := r.bashPPNilCollectionBridge(meta, typ); ok {
			return nilValue, nil
		}
		result.Kind = "nil"
		return result, nil
	default:
		return result, fmt.Errorf("gosource: unsupported interpreter collection value %T", value)
	}
	return result, nil
}
func (r *Runner) bashPPBridgeShortDecl(ctx context.Context, d *syntax.BashPPShortDecl) bool {
	if !r.bashPPBridgeHandles(d.Call) {
		return false
	}
	values, err := r.bashPPBridgeCall(ctx, d.Call)
	if err == nil && r.exit.exiting {
		// The dependency process terminated the program; the recorded status
		// must not be replaced by an arity diagnostic.
		return true
	}
	if err == nil && len(values) != len(d.Lhs) {
		err = fmt.Errorf("assignment mismatch: %d variables but %d native results", len(d.Lhs), len(values))
	}
	if err != nil {
		if !r.bashPPPanicking() {
			r.exit.fatal(err)
		}
		return true
	}
	for i, lhs := range d.Lhs {
		if lhs.Value == "_" {
			continue
		}
		r.bashPPBindNativeValue(lhs.Value, values[i])
	}
	return true
}

// bashPPBindNativeValue declares name from one native value. A scalar becomes
// an ordinary typed interpreter variable; anything else stays a session handle
// so the dependency keeps ownership, identity and mutation of the value.
func (r *Runner) bashPPBindNativeValue(name string, value bashPPBridgeValue) {
	if value.localCell != nil {
		r.goSourceBindLocalReflectCell(name, value.localCell)
		return
	}
	scalar, err := value.scalar()
	if err == nil {
		r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(scalar)})
		cell := r.bashPPScope.lookup(name)
		cell.scalarKind = scalar.kind()
		cell.negativeZero = scalar.negativeZero
		cell.nonFinite, cell.hasNonFinite = scalar.nonFinite, scalar.hasNonFinite
		cell.nonFiniteComplex, cell.hasNonFiniteComplex = scalar.nonFiniteComplex, scalar.hasNonFiniteComplex
		cell.typeName = value.Type
		cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value.Type}}
		if value.Interface != "" {
			// A defined scalar such as syscall.Errno can arrive as an error
			// interface. Keep the static interface and its concrete payload so
			// `err != nil` observes a non-nil interface, not a loose scalar.
			boxed := goSourceNativeValueCell(value)
			cell.declType = boxed.declType
			cell.interfaceValue = boxed.interfaceValue
		}
		return
	}
	copy := value
	r.bashPPDeclareName(name, expand.NewObject(&copy))
	cell := r.bashPPScope.lookup(name)
	// A dependency handle is reference storage just as much as a local map or
	// pointer is. Give it an identity at the binding boundary so readonly can
	// freeze the handle (and every alias of it) before a native field-set is
	// dispatched back to the dependency.
	cell.object = &bashPPObjectIdentity{owner: name}
	cell.typeName = value.Type
	if value.Interface != "" {
		// Use the same typed transport boundary as native expression results.
		// Array dynamic types need their shape and copied value contents;
		// a synthetic named type such as "[4]int32" is not a declaration.
		typed := r.goSourceNativeValueCell(value)
		cell.declType, cell.interfaceValue = typed.declType, typed.interfaceValue
	}
}

// bashPPBridgeTypeScope spells a named type by the identity the helper
// registered for its declaration, or reports false to keep the bare name.
// The runner's scope resolves a function-local declaration whose name is
// reused elsewhere in the program (bashpp_s243_scoped_local_types.go).
type bashPPBridgeTypeScope func(*syntax.BashPPNamedType) (string, bool)

func bashPPBridgeTypeText(typ syntax.BashPPTypeExpr) string {
	return bashPPBridgeTypeTextIn(typ, nil)
}

// bashPPBridgeTypeIdentity is bashPPBridgeTypeText under this runner's
// lexical type scope: a reference to a function-local named type whose
// name the program reuses is spelled as the helper name registered for
// exactly that declaration.
func (r *Runner) bashPPBridgeTypeIdentity(typ syntax.BashPPTypeExpr) string {
	return bashPPBridgeTypeTextIn(typ, r.bashPPScopedLocalTypeName)
}

func bashPPBridgeTypeTextIn(typ syntax.BashPPTypeExpr, scope bashPPBridgeTypeScope) string {
	switch t := typ.(type) {
	case *syntax.BashPPChanType:
		return goSourceNativeChannelTypeTextIn(t, scope)
	case *syntax.BashPPNamedType:
		base := t.Name.Value
		if scope != nil {
			if name, ok := scope(t); ok {
				base = name
			}
		}
		if len(t.TypeArgs) == 0 {
			return base
		}
		args := make([]string, len(t.TypeArgs))
		for i, arg := range t.TypeArgs {
			args[i] = bashPPBridgeTypeTextIn(arg.ArgType, scope)
		}
		return base + "[" + strings.Join(args, ",") + "]"
	case *syntax.BashPPPointerType:
		return "*" + bashPPBridgeTypeTextIn(t.Element, scope)
	case *syntax.BashPPFuncType:
		return "func(" + bashPPBridgeFieldsTextIn(t.Params, scope) + ")(" + bashPPBridgeFieldsTextIn(t.Results, scope) + ")"
	case *syntax.BashPPStructType:
		var fields []string
		for _, field := range t.Fields {
			// An embedded field spells only its element type, exactly as the
			// original wrote it; the helper materialises the same embedding.
			if field.Embedded {
				text := bashPPBridgeTypeTextIn(field.FieldTypeExpr, scope)
				if field.Tag != nil {
					text += " " + field.Tag.Value
				}
				fields = append(fields, text)
				continue
			}
			names := make([]string, len(field.Names))
			for i, name := range field.Names {
				names[i] = name.Value
			}
			text := strings.Join(names, ",") + " " + bashPPBridgeTypeTextIn(field.FieldTypeExpr, scope)
			if field.Tag != nil {
				text += " " + field.Tag.Value
			}
			fields = append(fields, text)
		}
		return "struct{" + strings.Join(fields, ";") + "}"
	case *syntax.BashPPInterfaceType:
		if len(t.Methods) == 0 && len(t.Elems) == 0 {
			return "interface{}"
		}
		elems := bashPPInterfaceElems(t)
		members := make([]string, 0, len(elems))
		for _, elem := range elems {
			if elem.Method != nil {
				method := elem.Method
				members = append(members, method.Name.Value+"("+bashPPBridgeFieldsTextIn(method.Params, scope)+")("+bashPPBridgeFieldsTextIn(method.Results, scope)+")")
			} else if elem.Embedded != nil {
				members = append(members, bashPPBridgeTypeTextIn(elem.Embedded, scope))
			}
		}
		return "interface{" + strings.Join(members, ";") + "}"
	case *syntax.BashPPCollectionType:
		if t.Kind == "map" {
			return "map[" + bashPPBridgeTypeTextIn(t.Key, scope) + "]" + bashPPBridgeTypeTextIn(t.Element, scope)
		}
		length := ""
		if t.Length != nil {
			length = t.Length.Value
		}
		return "[" + length + "]" + bashPPBridgeTypeTextIn(t.Element, scope)
	}
	return bashPPTypeText(typ)
}

func bashPPBridgeFieldsText(fields []*syntax.BashPPField) string {
	return bashPPBridgeFieldsTextIn(fields, nil)
}

func bashPPBridgeFieldsTextIn(fields []*syntax.BashPPField, scope bashPPBridgeTypeScope) string {
	var values []string
	for _, field := range fields {
		text := "<inferred>"
		if field.FieldTypeExpr != nil {
			text = bashPPBridgeTypeTextIn(field.FieldTypeExpr, scope)
		} else if field.FieldType != nil {
			text = field.FieldType.Value
		}
		if field.Variadic() {
			text = "..." + text
		}
		for range max(len(field.Names), 1) {
			values = append(values, text)
		}
	}
	return strings.Join(values, ",")
}

func (r *Runner) bashPPBridgeCell(cell *bashPPCell) (bashPPBridgeValue, error) {
	// The bridge value is chosen by testing carrier against carrier, so a
	// shared cell is read from a snapshot; see bashpp_cell_share.go.
	cell = cell.view()
	if cell != nil {
		if fn, ok := r.bashPPClosure(cell.vr.Str); ok {
			return r.bashPPBridgeFunction(fn)
		}
	}
	if cell == nil {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: missing result cell")
	}
	if cell.interfaceValue != nil {
		if cell.interfaceValue.nilIface {
			return bashPPBridgeValue{Kind: "nil"}, nil
		}
		if bashPPRuntimeErrorType(cell.interfaceValue.dynamic) {
			return bashPPBridgeString(bashPPRuntimeErrorText(cell.interfaceValue)), nil
		}
		value, err := r.bashPPBridgeCell(cell.interfaceValue.cell)
		if err == nil && r.bashPPGoSource {
			// Interface construction fixed the dynamic type before the payload
			// was copied. Preserve that producer-owned identity: aliases are
			// transparent, while local generic declarations retain the source
			// position needed to select their lexical registry entry.
			if cell.interfaceValue.dynamic != nil {
				dynamic := r.bashPPCanonicalAssignableType(cell.interfaceValue.dynamic)
				// Older scalar call binding records the dynamic declaration by
				// base name while the copied payload retains the concrete generic
				// target. Enrich only that exact base/instance pair; never infer
				// across names, packages, pointer depth, or lexical declarations.
				if base, ok := dynamic.(*syntax.BashPPNamedType); ok && base.Name != nil && len(base.TypeArgs) == 0 && cell.interfaceValue.cell != nil {
					if concrete, ok := cell.interfaceValue.cell.declType.(*syntax.BashPPNamedType); ok && concrete.Name != nil && concrete.Name.Value == base.Name.Value && len(concrete.TypeArgs) > 0 {
						dynamic = concrete
					}
				}
				value.Type = r.bashPPBridgeTypeIdentity(dynamic)
			}
			// A typed nil dynamic value is still a nonnil interface. Range
			// copies and argument/result cells must retain that static wrapper.
			value.Interface = r.bashPPBridgeTypeIdentity(cell.declType)
		}
		return value, err
	}
	if cell.pointer {
		value, err := r.bashPPBridgePointerValue(cell.pointerValue)
		if value.Type == "" {
			value.Type = r.bashPPBridgeTypeIdentity(cell.declType)
		}
		return value, err
	}
	if cell.vr.Kind == expand.Object {
		if value, ok := cell.vr.Obj.(*bashPPBridgeValue); ok {
			return *value, nil
		}
		return r.bashPPBridgeCollection(cell.vr.Obj, cell.valueMeta, cell.declType)
	}
	// A runtime error's payload cell (declared as the runtime's unexported
	// error type, see bashpp_sprint162_runtime_error.go) crosses as its
	// message string: the dependency has no such type to resolve, and the
	// message is exactly what %v / %s / Error() print.
	if bashPPRuntimeErrorType(cell.declType) {
		return bashPPBridgeString(cell.vr.String()), nil
	}
	scalar, err := bridgeScalar(r.bashPPScalarFromCell(cell))
	if err != nil {
		return scalar, err
	}
	scalar = bashPPBridgeInstantiatedScalar(scalar, cell.declType)
	return r.bashPPBridgeDefinedScalar(scalar)
}

// goSourceUnsafeConstantOperator reports whether call is one of the unsafe
// constant operators — Sizeof, Alignof or Offsetof of one operand — that
// goSourceUnsafeConstant folds; such a call is never a dependency request.
func (r *Runner) goSourceUnsafeConstantOperator(call *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || len(call.Fun) != 2 || len(call.ArgExprs) != 1 || r.bashPPImports[call.Fun[0].Value] != "unsafe" {
		return false
	}
	switch call.Fun[1].Value {
	case "Sizeof", "Alignof", "Offsetof":
		return true
	}
	return false
}

// goSourceUnsafeStringCall answers `unsafe.String(ptr, len)`: the string of
// len bytes starting at the byte ptr names. The pointer is one of the
// interpreter's own — into an array or slice element, or to a single byte
// variable — so the bytes are read from that storage; the dependency helper
// could neither see it nor return a string over it. A nil pointer with a
// zero length is the empty string; with any other length, or a length past
// the storage the pointer names, the call is the run-time fault Go raises.
// It reports claimed=false for any other call.
func (r *Runner) goSourceUnsafeStringCall(call *syntax.BashPPCall) (values []bashPPBridgeValue, claimed bool, err error) {
	defer func() { err = r.goSourceRuntimeFault(err) }()
	if !r.bashPPGoSource || call == nil || len(call.Fun) != 2 || call.Fun[1].Value != "String" || r.bashPPImports[call.Fun[0].Value] != "unsafe" {
		return nil, false, nil
	}
	if len(call.ArgExprs) != 2 || len(call.ArgExprs) != len(call.Args) || call.Ellipsis.IsValid() {
		return nil, false, nil
	}
	if r.bashPPScope != nil && r.bashPPScope.lookup(call.Fun[0].Value) != nil {
		return nil, false, nil
	}
	return r.goSourceUnsafeString(call, false)
}

// goSourceUnsafeString evaluates unsafe.String once. With discard set, a
// forged pointer that passes Go's checks yields no value instead of a
// refusal; see goSourceUnsafeDiscardAssign.
func (r *Runner) goSourceUnsafeString(call *syntax.BashPPCall, discard bool) (values []bashPPBridgeValue, claimed bool, err error) {
	defer func() { err = r.goSourceRuntimeFault(err) }()
	var ptr *bashPPPointer
	// An untyped nil operand is the nil *byte.
	if !goSourceNilLiteral(call.ArgExprs[0]) {
		ptr, err = r.bashPPPointerExprValue(call.ArgExprs[0])
		if err != nil {
			return nil, true, err
		}
	}
	// The length, nil and address-space checks are unsafe.Slice's; see
	// bashpp_s247_unsafe.go. A forged or out-of-span pointer then refuses.
	n, err := r.goSourceUnsafeLength("String", ptr, call.ArgExprs[1], 1)
	if err != nil {
		return nil, true, err
	}
	if ptr == nil {
		return []bashPPBridgeValue{{Kind: "string", Type: "string", NativeType: "string"}}, true, nil
	}
	if discard && ptr.forged() {
		return nil, true, nil
	}
	if err := goSourceUnsafeDerefCheck(ptr); err != nil {
		return nil, true, err
	}
	var bytes []any
	if last := len(ptr.path) - 1; last >= 0 && ptr.path[last].field == "" && !ptr.path[last].deref {
		// A pointer to an element: the bytes run on from that element.
		parent, _, _, err := ptr.readParent()
		if err != nil {
			return nil, true, err
		}
		seq, ok := parent.([]any)
		if !ok || ptr.path[last].index < 0 || ptr.path[last].index > len(seq) {
			return nil, true, fmt.Errorf("BASHPP-EUNSAFE-STRING: pointer no longer names byte storage")
		}
		bytes = seq[ptr.path[last].index:]
	} else {
		value, _, _, err := ptr.read()
		if err != nil {
			return nil, true, err
		}
		bytes = []any{value}
	}
	if n > int64(len(bytes)) {
		return nil, true, &bashPPRuntimeError{refusal: "BASHPP-EUNSAFE-STRING: unsafe.String: len out of range", runtime: "unsafe.String: len out of range"}
	}
	out := make([]byte, n)
	for i := range out {
		b, ok := bytes[i].(int)
		if !ok || b < 0 || b > 255 {
			return nil, true, fmt.Errorf("BASHPP-EUNSAFE-STRING: pointer does not name byte storage")
		}
		out[i] = byte(b)
	}
	return []bashPPBridgeValue{{Kind: "string", Type: "string", NativeType: "string", Text: string(out), Bytes: out}}, true, nil
}

// bashPPPlainScalarElement reports whether an interpreter collection element is
// held as a plain scalar rather than a structured or native value.
func bashPPPlainScalarElement(value any) bool {
	switch value.(type) {
	case string, bool, int, int64, uint64, float64, bashPPScalar, *bashPPScalar:
		return true
	}
	return false
}
