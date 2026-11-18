package interp

// Sprint: #118; Story: #64; Story-ID: 45be321bddfb
//
// The general callback/signature/copied-slice bridge. Three limits used to be
// entangled here: a callback signature had to be scalar, a request carrying an
// original callback could not also carry original slice storage, and the
// generic ordering helpers had no reflectable dependency symbol at all.
//
// The rules implemented below are general, not per-callable:
//
//   - A callback parameter or result whose type has pure value semantics — a
//     scalar, or a struct/array built only from such types — is copied by Go
//     itself at the call boundary, so a copied transport observes exactly what
//     native Go observes. Reference-bearing shapes (slice, map, pointer, chan,
//     func, interface) stay refused, because a copy of those would silently
//     drop aliasing.
//
//   - The copied-slice refusal exists because a dependency holding a decoded
//     copy of interpreter storage may observe or lose writes an original
//     callback makes while the call is in flight. A callable computed entirely
//     interpreter-side never hands that storage to the dependency, so the
//     refusal does not apply to it; its callback and its slice are the same
//     Runner's own state, in one goroutine.
//
//   - slices.SortFunc/SortStableFunc/EqualFunc/IndexFunc/ContainsFunc and
//     cmp.Compare/Less are uninstantiated generic functions, which are not
//     reflectable imported symbols. They are answered interpreter-side over the
//     original slice storage when identity can matter, exactly as slices.Sort/
//     Equal are.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceInterpretedCallable reports a callable this Runner answers itself,
// without ever sending interpreter-owned storage to the dependency process.
// Only these are exempt from the copied-slice refusal.
func goSourceInterpretedCallable(name string) bool {
	switch name {
	case "slices.Sort", "slices.Equal", "slices.Collect",
		"slices.SortFunc", "slices.SortStableFunc", "slices.EqualFunc", "slices.IndexFunc", "slices.ContainsFunc",
		"slices.Index", "slices.Contains", "slices.Reverse",
		"cmp.Compare", "cmp.Less":
		return true
	}
	return false
}

// bashPPCallbackValueType reports whether a callback parameter or result type
// has pure Go value semantics, so a transported copy is indistinguishable from
// the copy Go makes at the call itself.
func (r *Runner) bashPPCallbackValueType(typ syntax.BashPPTypeExpr) bool {
	return r.callbackValueType(typ, 0)
}

func (r *Runner) callbackValueType(typ syntax.BashPPTypeExpr, depth int) bool {
	if typ == nil || depth > 8 {
		return false
	}
	if r.bashPPCallbackScalarType(typ) {
		return true
	}
	switch shape := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPStructType:
		for _, field := range bashPPFlatFields(shape.Fields) {
			if !r.callbackValueType(field.typ, depth+1) {
				return false
			}
		}
		return len(shape.Fields) > 0
	case *syntax.BashPPCollectionType:
		if shape.Kind == "array" {
			return r.callbackValueType(shape.Element, depth+1)
		}
	}
	return false
}

// callbackLocalSliceType admits reference-bearing slice elements only for a
// callback which an interpreter-side collection helper invokes over live
// storage. Go copies structs and slice headers at that boundary while the
// referenced backing arrays remain shared.
func (r *Runner) callbackLocalSliceType(typ syntax.BashPPTypeExpr, depth int) bool {
	if typ == nil || depth > 8 {
		return false
	}
	if r.bashPPCallbackValueType(typ) {
		return true
	}
	switch shape := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPCollectionType:
		if shape.Kind == "slice" || shape.Kind == "array" {
			return r.callbackLocalSliceType(shape.Element, depth+1)
		}
	case *syntax.BashPPStructType:
		for _, field := range bashPPFlatFields(shape.Fields) {
			if !r.callbackLocalSliceType(field.typ, depth+1) {
				return false
			}
		}
		return len(shape.Fields) > 0
	}
	return false
}

// bashPPCallbackNativeType reports a callback parameter or result type that
// names an imported dependency type. Such a value never leaves the dependency:
// it arrives as the session handle it already is, so nothing is copied and no
// aliasing can be lost. This is what lets an original http.HandlerFunc declare
// its real (http.ResponseWriter, *http.Request) signature.
func (r *Runner) bashPPCallbackNativeType(typ syntax.BashPPTypeExpr) bool {
	if typ == nil {
		return false
	}
	text := strings.TrimPrefix(bashPPTypeText(typ), "*")
	alias, name, ok := strings.Cut(text, ".")
	if !ok || name == "" || strings.Contains(name, ".") {
		return false
	}
	return r.bashPPImports[alias] != ""
}

// goSourceCallbackCell binds one transported value to a callback parameter at
// its declared type. A scalar keeps the direct text form the positional call
// protocol uses; a value-semantics aggregate is rebuilt through the ordinary
// bridge contents path, so the body reads the fields it declared.
func (r *Runner) goSourceCallbackCell(v bashPPBridgeValue, param bashPPParam) (*bashPPCell, string, error) {
	if scalar, err := v.scalar(); err == nil {
		// A constant.Value keeps a float exact as a rational, so 3.5 renders
		// as "7/2" — text no float parameter accepts. The transported decimal
		// is what the parameter's own type reads.
		text := bashPPScalarString(scalar.value)
		if v.Kind == "float" && v.Text != "" {
			text = v.Text
		}
		cell := goSourceNativeValueCell(v)
		cell.vr.Str = text
		cell.typeName, cell.declType = param.declared, param.typ
		return cell, text, nil
	}
	if r.bashPPCallbackNativeType(param.typ) {
		cell := goSourceNativeValueCell(v)
		cell.typeName, cell.declType = bashPPTypeText(param.typ), param.typ
		return cell, "", nil
	}
	if !r.bashPPCallbackValueType(param.typ) {
		return nil, "", fmt.Errorf("gosource: callback parameter of type %s requires shared-reference transport", bashPPTypeText(param.typ))
	}
	value, meta, err := r.bashPPBridgeContents(v, param.typ)
	if err != nil {
		return nil, "", err
	}
	cell := &bashPPCell{declType: param.typ, typeName: bashPPTypeText(param.typ)}
	bashPPStoreCellValue(cell, value, meta)
	return cell, "", nil
}

// goSourceCallInterpretedCallback invokes an original callback from an
// interpreter-computed helper. Unlike the dependency callback path it does not
// absorb a panic or a failed exit into a transported reply: there is no
// dependency frame between the caller and the callback, so an interrupted
// callback unwinds the original program the way native Go would.
func (r *Runner) goSourceCallInterpretedCallback(ctx context.Context, fn *bashPPFunc, args []bashPPBridgeValue) ([]*bashPPCell, error) {
	params := bashppParams(fn.params())
	if len(args) != len(params) {
		return nil, fmt.Errorf("gosource: original callback wants %d argument(s), got %d", len(params), len(args))
	}
	texts := make([]string, len(args))
	cells := make([]*bashPPCell, len(args))
	for i, arg := range args {
		cell, text, err := r.goSourceCallbackCell(arg, params[i])
		if err != nil {
			return nil, err
		}
		cells[i], texts[i] = cell, text
	}
	return r.goSourceInvokeCallbackCells(ctx, fn, cells, texts)
}

// goSourceInvokeCallbackCells runs an original callback over argument cells
// already bound at the parameters' declared types.
func (r *Runner) goSourceInvokeCallbackCells(ctx context.Context, fn *bashPPFunc, cells []*bashPPCell, texts []string) ([]*bashPPCell, error) {
	savedResults, savedCalls := r.bashPPResultCells, r.bashPPCallCells
	savedChannels, savedInterfaces := r.bashPPCallChannels, r.bashPPCallInterfaces
	defer func() {
		r.bashPPResultCells, r.bashPPCallCells = savedResults, savedCalls
		r.bashPPCallChannels, r.bashPPCallInterfaces = savedChannels, savedInterfaces
	}()
	r.bashPPCallChannels, r.bashPPCallInterfaces = nil, nil
	r.bashPPCallCells = cells
	r.bashPPResultCells = nil
	failure := r.bashPPShortFailureSeq
	r.bashPPInvoke(ctx, fn, texts)
	if r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPShortFailureSeq != failure {
		return nil, errBashPPScalarInterrupted
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results := append([]*bashPPCell(nil), r.bashPPResultCells...)
	if len(results) != bashppResultCount(fn.results()) {
		return nil, fmt.Errorf("gosource: original callback result count mismatch")
	}
	return results, nil
}

// goSourceCallbackFunc resolves one transported callback argument back to the
// interpreted function it names, failing closed on a foreign session.
func goSourceCallbackFunc(req bashPPEvalRequest, v bashPPBridgeValue) (*bashPPFunc, error) {
	if v.Kind != "callback" || req.Bridge == nil || v.Session != req.Bridge.id {
		return nil, fmt.Errorf("gosource: a current original function callback is required")
	}
	req.Bridge.mu.Lock()
	fn := req.Bridge.functions[v.Handle]
	req.Bridge.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("gosource: original callback handle expired")
	}
	return fn, nil
}

// goSourceCallbackInt runs a comparison callback and reads its single int
// result, the shape cmp-style ordering functions require.
func (r *Runner) goSourceCallbackInt(ctx context.Context, fn *bashPPFunc, args []bashPPBridgeValue) (int, error) {
	results, err := r.goSourceCallInterpretedCallback(ctx, fn, args)
	if err != nil {
		return 0, err
	}
	return r.goSourceCallbackIntResult(results)
}

// goSourceCallbackIntResult reads the single int result of a comparison.
func (r *Runner) goSourceCallbackIntResult(results []*bashPPCell) (int, error) {
	if len(results) != 1 {
		return 0, fmt.Errorf("gosource: comparison callback must return one value")
	}
	value, err := r.bashPPBridgeCell(results[0])
	if err != nil {
		return 0, err
	}
	scalar, err := value.scalar()
	if err != nil {
		return 0, fmt.Errorf("gosource: comparison callback result: %w", err)
	}
	n, err := strconv.ParseInt(bashPPScalarString(scalar.value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("gosource: comparison callback returned %q, want an integer", bashPPScalarString(scalar.value))
	}
	return int(n), nil
}

// goSourceCallbackBool runs a predicate callback and reads its single bool
// result.
func (r *Runner) goSourceCallbackBool(ctx context.Context, fn *bashPPFunc, args []bashPPBridgeValue) (bool, error) {
	results, err := r.goSourceCallInterpretedCallback(ctx, fn, args)
	if err != nil {
		return false, err
	}
	return r.goSourceCallbackBoolResult(results)
}

func (r *Runner) goSourceCallbackBoolResult(results []*bashPPCell) (bool, error) {
	if len(results) != 1 {
		return false, fmt.Errorf("gosource: predicate callback must return one value")
	}
	value, err := r.bashPPBridgeCell(results[0])
	if err != nil {
		return false, err
	}
	if value.Kind != "bool" {
		return false, fmt.Errorf("gosource: predicate callback must return bool, got %s", value.Kind)
	}
	return value.Text == "true", nil
}

// goSourceGenericCallbackHelper answers the generic callback-taking helpers
// interpreter-side. handled is false for every other callable, leaving ordinary
// dependency dispatch untouched.
func (r *Runner) goSourceGenericCallbackHelper(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) (values []bashPPBridgeValue, handled bool, err error) {
	name := nativeSliceCallable(req, *q)
	switch name {
	case "cmp.Compare", "cmp.Less":
		if len(q.Args) != 2 {
			return nil, true, fmt.Errorf("gosource: %s requires two operands", name)
		}
		n, err := goSourceOrderedCompare(q.Args[0], q.Args[1])
		if err != nil {
			return nil, true, err
		}
		if name == "cmp.Less" {
			return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(n < 0)}}, true, nil
		}
		return []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(n)}}, true, nil
	case "slices.SortFunc", "slices.SortStableFunc":
		return nil, true, r.goSourceSlicesSortFunc(ctx, req, q)
	case "slices.EqualFunc":
		return r.goSourceSlicesEqualFunc(ctx, req, q)
	case "slices.IndexFunc", "slices.ContainsFunc":
		return r.goSourceSlicesSearchFunc(ctx, req, q, name == "slices.ContainsFunc")
	case "slices.Index", "slices.Contains":
		return goSourceSlicesSearch(q, name == "slices.Contains")
	}
	return nil, false, nil
}

// goSourceSlicesSearch answers slices.Index and slices.Contains over the
// transported elements. Both only read the slice and compare with ==, so no
// method of the element type can run and nothing is retained: the search is
// the same whether or not the element type carries callbacks. A pointer is
// compared by the identity of the storage it names (its transport origin),
// never by its pointee. An element kind whose == this cannot decide leaves the
// call to the dependency, as before.
func goSourceSlicesSearch(q *bashPPBridgeRequest, contains bool) ([]bashPPBridgeValue, bool, error) {
	if len(q.Args) != 2 {
		return nil, false, nil
	}
	elements, err := goSourceSequenceElements(q.Args[0])
	if err != nil {
		return nil, false, nil
	}
	index := -1
	for i, element := range elements {
		equal, decided := goSourceTransportedEqual(element, q.Args[1])
		if !decided {
			return nil, false, nil
		}
		if equal {
			index = i
			break
		}
	}
	if contains {
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(index >= 0)}}, true, nil
	}
	return []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(index)}}, true, nil
}

// goSourceTransportedEqual decides a == b for two transported values of one
// static type: nil pointers, interpreter pointers of one session (by origin)
// and primitive scalars. decided is false for everything else.
func goSourceTransportedEqual(a, b bashPPBridgeValue) (equal, decided bool) {
	if a.Interface != "" || b.Interface != "" {
		return false, false
	}
	if a.Kind == "nil" || b.Kind == "nil" {
		other := a
		if a.Kind == "nil" {
			other = b
		}
		if other.Kind == "nil" {
			return true, true
		}
		// A non-nil interpreter pointer never equals nil.
		return false, other.Kind == "pointer" && other.Origin != 0
	}
	if a.Kind == "pointer" && b.Kind == "pointer" {
		if a.Origin == 0 || b.Origin == 0 || a.Session != b.Session {
			return false, false
		}
		return a.Origin == b.Origin, true
	}
	switch a.Kind {
	case "string", "bool", "int", "uint", "float":
		if a.Kind != b.Kind {
			return false, false
		}
		equal, err := nativeSliceScalarEqual(a, b)
		return equal, err == nil
	}
	return false, false
}

// goSourceSlicesEqualFunc answers slices.EqualFunc over the live original
// backing arrays when they are representable. That preserves interface dynamic
// values and pointer identity, and a callback mutation of a later element is
// visible to the next comparison just as it is in native Go.
func (r *Runner) goSourceSlicesEqualFunc(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) ([]bashPPBridgeValue, bool, error) {
	name := nativeSliceCallable(req, *q)
	if len(q.Args) != 3 {
		return nil, true, fmt.Errorf("gosource: %s requires two slices and an equality function", name)
	}
	left, err := r.goSourceEqualFuncOperand(ctx, req, q.Args[0])
	if err != nil {
		return nil, true, err
	}
	right, err := r.goSourceEqualFuncOperand(ctx, req, q.Args[1])
	if err != nil {
		return nil, true, err
	}
	if left.len != right.len {
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: "false"}}, true, nil
	}
	if left.len == 0 {
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: "true"}}, true, nil
	}
	fn, err := goSourceCallbackFunc(req, q.Args[2])
	if err != nil {
		return nil, true, err
	}
	params := bashppParams(fn.params())
	if len(params) != 2 {
		return nil, true, fmt.Errorf("gosource: %s equality function wants two parameters", name)
	}
	for i := range left.len {
		leftCell, leftText, err := left.callbackCell(ctx, req, r, i, params[0])
		if err != nil {
			return nil, true, err
		}
		rightCell, rightText, err := right.callbackCell(ctx, req, r, i, params[1])
		if err != nil {
			return nil, true, err
		}
		results, err := r.goSourceInvokeCallbackCells(ctx, fn, []*bashPPCell{leftCell, rightCell}, []string{leftText, rightText})
		if err != nil {
			return nil, true, err
		}
		equal, err := r.goSourceCallbackBoolResult(results)
		if err != nil {
			return nil, true, err
		}
		if !equal {
			return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: "false"}}, true, nil
		}
	}
	return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: "true"}}, true, nil
}

type goSourceEqualFuncOperand struct {
	len      int
	shared   *goSourceSharedSlice
	elements []bashPPBridgeValue
	handle   *bashPPBridgeValue
}

func (r *Runner) goSourceEqualFuncOperand(ctx context.Context, req bashPPEvalRequest, v bashPPBridgeValue) (goSourceEqualFuncOperand, error) {
	if shared, ok := r.goSourceSharedReadSliceOf(v); ok {
		return goSourceEqualFuncOperand{len: len(shared.view), shared: &shared}, nil
	}
	if v.Kind == "handle" {
		n, err := r.goSourceNativeSliceLen(ctx, req, v)
		if err != nil {
			return goSourceEqualFuncOperand{}, err
		}
		return goSourceEqualFuncOperand{len: n, handle: &v}, nil
	}
	elements, err := goSourceSequenceElements(v)
	if err != nil {
		return goSourceEqualFuncOperand{}, err
	}
	return goSourceEqualFuncOperand{len: len(elements), elements: elements}, nil
}

func (o goSourceEqualFuncOperand) callbackCell(ctx context.Context, req bashPPEvalRequest, r *Runner, i int, param bashPPParam) (*bashPPCell, string, error) {
	if o.shared != nil {
		cell, text := o.shared.callbackArg(i, param)
		return cell, text, nil
	}
	var value bashPPBridgeValue
	if o.handle != nil {
		var err error
		value, err = r.goSourceNativeSliceIndex(ctx, req, *o.handle, i)
		if err != nil {
			return nil, "", err
		}
	} else {
		value = o.elements[i]
	}
	return r.goSourceCallbackCell(value, param)
}

func (r *Runner) goSourceNativeSliceLen(ctx context.Context, req bashPPEvalRequest, v bashPPBridgeValue) (int, error) {
	if req.Bridge == nil {
		return 0, fmt.Errorf("gosource: native slice handle is not bound to a dependency session")
	}
	values, err := req.Bridge.request(ctx, req, bashPPBridgeRequest{Op: "len", Receiver: &v})
	if err != nil {
		return 0, err
	}
	if len(values) != 1 || values[0].Kind != "int" {
		return 0, fmt.Errorf("gosource: native slice length returned invalid shape")
	}
	n, err := strconv.Atoi(values[0].Text)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("gosource: native slice length returned %q", values[0].Text)
	}
	return n, nil
}

func (r *Runner) goSourceNativeSliceIndex(ctx context.Context, req bashPPEvalRequest, v bashPPBridgeValue, i int) (bashPPBridgeValue, error) {
	if req.Bridge == nil {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: native slice handle is not bound to a dependency session")
	}
	values, err := req.Bridge.request(ctx, req, bashPPBridgeRequest{
		Op:       "index",
		Receiver: &v,
		Args:     []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(i)}},
	})
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	if len(values) != 1 {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: native slice index returned invalid shape")
	}
	return values[0], nil
}

func (r *Runner) goSourceSharedReadSliceOf(v bashPPBridgeValue) (goSourceSharedSlice, bool) {
	capture := v.sliceView
	if capture == nil || v.Kind != "slice" {
		return goSourceSharedSlice{}, false
	}
	collection, ok := r.bashPPUnderlyingType(capture.typ).(*syntax.BashPPCollectionType)
	if !ok || collection.Kind != "slice" {
		return goSourceSharedSlice{}, false
	}
	if capture.meta != nil && len(capture.meta.sequence) < len(capture.view) {
		return goSourceSharedSlice{}, false
	}
	return goSourceSharedSlice{view: capture.view, meta: capture.meta}, true
}

// goSourceSlicesSortFunc reorders the original backing array in place with
// the original comparison callback (bashpp_native_shared_order.go): each
// comparison reads its two elements from live storage, and each swap moves
// the elements and their metadata, so every aliasing header — and the
// callback itself, should it index the slice — observes the order native Go's
// pdqsortCmpFunc/stableCmpFunc produce, one step at a time.
func (r *Runner) goSourceSlicesSortFunc(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) error {
	name := nativeSliceCallable(req, *q)
	if len(q.SliceBuffers) != 1 || len(q.sliceTargets) != 1 {
		return fmt.Errorf("gosource: %s requires a direct original slice", name)
	}
	if len(q.Args) != 2 {
		return fmt.Errorf("gosource: %s requires a slice and a comparison function", name)
	}
	fn, err := goSourceCallbackFunc(req, q.Args[1])
	if err != nil {
		return err
	}
	shared, ok := r.goSourceSharedSliceOf(bashPPBridgeValue{Kind: "slice", sliceView: q.sliceTargets[0]})
	if !ok {
		return fmt.Errorf("gosource: %s cannot share the original slice storage", name)
	}
	order := &goSourceSharedOrder{
		n: len(shared.view),
		less: func(i, j int) (bool, error) {
			// Bind every element from live interpreter storage. Function-call
			// binding then applies Go's own value/reference copy rules: scalars
			// and aggregates copy, while pointers and interface dynamic values
			// retain identity.
			params := bashppParams(fn.params())
			if len(params) != 2 {
				return false, fmt.Errorf("gosource: %s comparison wants two parameters", name)
			}
			left, leftText := shared.callbackArg(i, params[0])
			right, rightText := shared.callbackArg(j, params[1])
			cells := []*bashPPCell{left, right}
			results, err := r.goSourceInvokeCallbackCells(ctx, fn, cells, []string{leftText, rightText})
			if err != nil {
				return false, err
			}
			n, err := r.goSourceCallbackIntResult(results)
			return n < 0, err
		},
		swap: shared.swap,
	}
	if name == "slices.SortStableFunc" {
		return order.run(sort.Stable)
	}
	return order.run(sort.Sort)
}

// goSourceSlicesSearchFunc answers slices.IndexFunc/ContainsFunc. Both only
// read the transported elements, so no writeback is involved.
func (r *Runner) goSourceSlicesSearchFunc(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest, contains bool) ([]bashPPBridgeValue, bool, error) {
	if len(q.Args) != 2 {
		return nil, true, fmt.Errorf("gosource: %s requires a slice and a predicate", nativeSliceCallable(req, *q))
	}
	fn, err := goSourceCallbackFunc(req, q.Args[1])
	if err != nil {
		return nil, true, err
	}
	params := bashppParams(fn.params())
	if len(params) != 1 {
		return nil, true, fmt.Errorf("gosource: %s predicate wants one parameter", nativeSliceCallable(req, *q))
	}
	if shared, ok := r.goSourceSharedReadSliceOf(q.Args[0]); ok {
		index := -1
		for i := range shared.view {
			cell, text := shared.callbackArg(i, params[0])
			results, err := r.goSourceInvokeCallbackCells(ctx, fn, []*bashPPCell{cell}, []string{text})
			if err != nil {
				return nil, true, err
			}
			match, err := r.goSourceCallbackBoolResult(results)
			if err != nil {
				return nil, true, err
			}
			if match {
				index = i
				break
			}
		}
		if contains {
			return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(index >= 0)}}, true, nil
		}
		return []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(index)}}, true, nil
	}
	elements, err := goSourceSequenceElements(q.Args[0])
	if err != nil {
		return nil, true, err
	}
	index := -1
	for i, element := range elements {
		match, err := r.goSourceCallbackBool(ctx, fn, []bashPPBridgeValue{element})
		if err != nil {
			return nil, true, err
		}
		if match {
			index = i
			break
		}
	}
	if contains {
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(index >= 0)}}, true, nil
	}
	return []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(index)}}, true, nil
}

// goSourceSequenceElements reads the transported elements of a slice or array,
// treating a nil slice as empty exactly as ranging over one does.
func goSourceSequenceElements(v bashPPBridgeValue) ([]bashPPBridgeValue, error) {
	switch v.Kind {
	case "slice", "array":
		return v.Elements, nil
	case "nil":
		return nil, nil
	}
	return nil, fmt.Errorf("gosource: a slice operand is required, got %s", v.Kind)
}

// goSourceOrderedCompare orders two transported operands as cmp.Compare does,
// including its NaN rule: a NaN sorts below every non-NaN value and equals
// itself. bool is not ordered and is rejected, as cmp.Ordered requires.
func goSourceOrderedCompare(a, b bashPPBridgeValue) (int, error) {
	if a.Kind != b.Kind && !(goSourceNumericKind(a.Kind) && goSourceNumericKind(b.Kind)) {
		return 0, fmt.Errorf("gosource: cmp requires operands of one ordered type, got %s and %s", a.Kind, b.Kind)
	}
	if a.Kind == "string" {
		return strings.Compare(a.Text, b.Text), nil
	}
	if !goSourceNumericKind(a.Kind) {
		return 0, fmt.Errorf("gosource: cmp requires ordered operands, got %s", a.Kind)
	}
	left, err := strconv.ParseFloat(a.Text, 64)
	if err != nil {
		return 0, fmt.Errorf("gosource: invalid ordered operand %q", a.Text)
	}
	right, err := strconv.ParseFloat(b.Text, 64)
	if err != nil {
		return 0, fmt.Errorf("gosource: invalid ordered operand %q", b.Text)
	}
	leftNaN, rightNaN := math.IsNaN(left), math.IsNaN(right)
	switch {
	case leftNaN && rightNaN:
		return 0, nil
	case leftNaN:
		return -1, nil
	case rightNaN:
		return 1, nil
	case left < right:
		return -1, nil
	case left > right:
		return 1, nil
	}
	return 0, nil
}

func goSourceNumericKind(kind string) bool {
	switch kind {
	case "int", "uint", "float":
		return true
	}
	return false
}

// goSourceReferenceDigest renders the storage a copied receiver shares with its
// caller: the contents of every slice and map reachable from it. A Go value
// receiver may freely reassign its own fields, including replacing a slice
// header, because the caller never sees that copy. What the caller does see is
// a write THROUGH one of those references, and that is what this digest
// changes. Comparing it before and after an original value-receiver callback
// turns a silently dropped effect into a reported one. Replacing a header is
// reported too: it is indistinguishable here, and refusing loudly is the side
// to err on.
func goSourceReferenceDigest(v bashPPBridgeValue) string {
	var out strings.Builder
	var walk func(bashPPBridgeValue)
	walk = func(v bashPPBridgeValue) {
		switch v.Kind {
		case "slice", "map":
			data, err := json.Marshal(v)
			if err != nil {
				out.WriteString("?")
				return
			}
			out.WriteString(v.Kind)
			out.Write(data)
			return
		}
		for _, element := range v.Elements {
			walk(element)
		}
		names := make([]string, 0, len(v.Fields))
		for name := range v.Fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out.WriteString(name)
			walk(v.Fields[name])
		}
		for _, entry := range v.Entries {
			walk(entry.Value)
		}
	}
	walk(v)
	return out.String()
}

// goSourceCopiedReceiverDigest reads the shared-storage digest of a receiver
// the interpreter rebuilt from a transported copy. An unreadable receiver
// yields no digest and is simply not compared, which cannot mask a mutation:
// the same read fails identically before and after the body.
func (r *Runner) goSourceCopiedReceiverDigest(cell *bashPPCell) string {
	if cell == nil {
		return ""
	}
	// Only slices and maps have shared mutable storage in a copied value
	// receiver. Most high-frequency callbacks (for example image.Image.At)
	// carry scalar structs; avoid rebuilding and walking their entire bridge
	// representation merely to discover that their reference digest is empty.
	if !bashPPCopiedReceiverHasReferences(bashPPCellMeta(cell), make(map[*bashPPCollectionMeta]bool)) {
		return ""
	}
	value, err := r.bashPPBridgeCell(cell)
	if err != nil {
		return ""
	}
	return goSourceReferenceDigest(value)
}

func bashPPCopiedReceiverHasReferences(meta *bashPPCollectionMeta, seen map[*bashPPCollectionMeta]bool) bool {
	if meta == nil || seen[meta] {
		return false
	}
	seen[meta] = true
	if meta.kind == "slice" || meta.kind == "map" {
		return true
	}
	if iface := meta.interfaceValue; iface != nil && iface.cell != nil &&
		bashPPCopiedReceiverHasReferences(bashPPCellMeta(iface.cell), seen) {
		return true
	}
	for _, child := range meta.sequence {
		if bashPPCopiedReceiverHasReferences(child, seen) {
			return true
		}
	}
	for _, child := range meta.mapping {
		if bashPPCopiedReceiverHasReferences(child, seen) {
			return true
		}
	}
	return false
}

// elementCell binds element i of the original backing array to a callback
// parameter as the value Go copies into it: for a pointer element, the pointer
// itself, sharing its pointee with the slice.
func (s goSourceSharedSlice) elementCell(i int, param bashPPParam) *bashPPCell {
	var child *bashPPCollectionMeta
	if s.meta != nil && i < len(s.meta.sequence) {
		child = s.meta.sequence[i]
	}
	cell := &bashPPCell{declType: param.typ, typeName: param.declared}
	bashPPStoreCellValue(cell, s.view[i], child)
	return cell
}

// callbackArg carries both representations used by ordinary function binding:
// the typed cell preserves reference identity and aggregate metadata, while
// the text keeps scalar positional arguments usable by the existing evaluator.
func (s goSourceSharedSlice) callbackArg(i int, param bashPPParam) (*bashPPCell, string) {
	cell := s.elementCell(i, param)
	return cell, cell.vr.String()
}
