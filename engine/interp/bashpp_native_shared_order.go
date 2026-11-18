package interp

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0
//
// Shared-storage ordering: the sort algorithms run over the interpreter's own
// backing array.
//
// The bridge transports a slice by copying its elements. When the callee also
// calls back into the interpreter — sort.Sort's Less/Swap, a sort.Slice
// comparison closure that indexes the captured slice — the dependency would
// permute its decoded copy while every callback reads and writes the
// original, and the two diverge after the first Swap. The copied-reference
// refusal in prepareNativeSliceBuffers guards exactly that.
//
// For the ordering entry points of package sort (and the generic
// slices.SortFunc/SortStableFunc, which the dependency cannot reflect at all)
// there is one storage instead of two: the algorithm runs in this process,
// driven by the SDK's own package sort, and its Len/Less/Swap act directly on
// interpreter storage.
//
//   - sort.Sort/Stable/IsSorted bind the original Len/Less/Swap methods to the
//     ORIGINAL receiver: a slice-kind value shares the transported view's
//     backing array (Go copies the header, never the elements), and an
//     origin-bearing pointer resolves to the program's own pointer.
//   - sort.Slice/SliceStable/SliceIsSorted permute the original backing array
//     and its element metadata in place, exactly as reflect.Swapper does, and
//     call the original comparison closure with the two indexes.
//   - slices.SortFunc/SortStableFunc permute the same way and hand the
//     comparison the two elements read from live storage at the moment of the
//     comparison.
//
// The SDK generates sort.Sort's pdqsort, sort.Slice's pdqsort_func and
// slices.SortFunc's pdqsortCmpFunc (and the three stable variants) from one
// template, so driving sort.Sort/sort.Stable over this adapter issues the very
// Less/Swap sequence native Go issues for each entry point: call counts, the
// order among equal elements and the in-flight order a callback observes all
// match. Every alias of the backing array — a sub-slice, another header —
// observes each Swap as it happens.
//
// Nothing is retained: the callee is this process's own code and returns
// before the request does, and every callback runs synchronously on the
// requesting goroutine. A callback panic, exit or failure stops the algorithm
// at once, so the storage keeps precisely the partial order native Go leaves
// behind. A shape this file cannot bind — a struct value holding the slice, a
// receiver whose methods are not all original, a slice with a logical
// length/capacity carrier — is not claimed and meets the ordinary transport,
// whose refusal still applies.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceSharedOrder adapts an interpreter-side ordering to sort.Interface.
// A failing callback aborts the SDK algorithm through a private panic that
// run recovers; no further Less or Swap reaches the program.
type goSourceSharedOrder struct {
	n    int
	less func(i, j int) (bool, error)
	swap func(i, j int) error
	err  error
}

type goSourceSharedOrderAbort struct{}

func (o *goSourceSharedOrder) Len() int { return o.n }

func (o *goSourceSharedOrder) Less(i, j int) bool {
	less, err := o.less(i, j)
	if err != nil {
		o.err = err
		panic(goSourceSharedOrderAbort{})
	}
	return less
}

func (o *goSourceSharedOrder) Swap(i, j int) {
	if err := o.swap(i, j); err != nil {
		o.err = err
		panic(goSourceSharedOrderAbort{})
	}
}

// run drives algorithm over the adapter, converting an aborted callback back
// into its error. Any other panic is not ours and keeps unwinding.
func (o *goSourceSharedOrder) run(algorithm func(sort.Interface)) (err error) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(goSourceSharedOrderAbort); !ok {
				panic(p)
			}
			err = o.err
		}
	}()
	algorithm(o)
	return nil
}

// isSorted is the SDK's IsSorted/SliceIsSorted loop over the adapter.
func (o *goSourceSharedOrder) isSorted() (sorted bool, err error) {
	err = o.run(func(data sort.Interface) {
		sorted = true
		for i := data.Len() - 1; i > 0; i-- {
			if data.Less(i, i-1) {
				sorted = false
				return
			}
		}
	})
	return sorted, err
}

// goSourceSharedSlice is the original backing array of a transported slice,
// permuted in place: the dense element carrier and its per-element metadata
// move together, as an interpreted s[i], s[j] = s[j], s[i] moves them.
type goSourceSharedSlice struct {
	view []any
	meta *bashPPCollectionMeta
}

// goSourceSharedSliceOf binds the original storage behind a transported slice
// argument, or reports false for a shape whose storage it cannot reach.
func (r *Runner) goSourceSharedSliceOf(v bashPPBridgeValue) (goSourceSharedSlice, bool) {
	capture := v.sliceView
	if capture == nil || v.Kind != "slice" {
		return goSourceSharedSlice{}, false
	}
	collection, ok := r.bashPPUnderlyingType(capture.typ).(*syntax.BashPPCollectionType)
	if !ok || collection.Kind != "slice" {
		return goSourceSharedSlice{}, false
	}
	// A logical length/capacity carrier is a different representation of the
	// same storage; it is not permuted here.
	if bashPPLogicalSequence(capture.meta) {
		return goSourceSharedSlice{}, false
	}
	if capture.meta != nil && len(capture.meta.sequence) < len(capture.view) {
		return goSourceSharedSlice{}, false
	}
	return goSourceSharedSlice{view: capture.view, meta: capture.meta}, true
}

func (s goSourceSharedSlice) swap(i, j int) error {
	if i < 0 || j < 0 || i >= len(s.view) || j >= len(s.view) {
		return fmt.Errorf("gosource: shared slice swap index out of range [%d, %d] with length %d", i, j, len(s.view))
	}
	s.view[i], s.view[j] = s.view[j], s.view[i]
	if s.meta != nil {
		s.meta.sequence[i], s.meta.sequence[j] = s.meta.sequence[j], s.meta.sequence[i]
	}
	return nil
}

// goSourceSharedOrdering answers the package sort ordering entry points over
// interpreter storage when the request carries original callbacks. handled is
// false for every other request, and for a shape the shared binding does not
// cover, leaving the ordinary transport (and its refusal) in charge.
func (r *Runner) goSourceSharedOrdering(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) ([]bashPPBridgeValue, bool, error) {
	if q.Op != "call" || q.Spread {
		return nil, false, nil
	}
	if q.Receiver != nil && (q.Selector != "" || q.Receiver.Kind != "handle") {
		return nil, false, nil
	}
	if q.Receiver == nil {
		alias, _, ok := strings.Cut(q.Selector, ".")
		if !ok || req.Imports[alias] != "sort" {
			return nil, false, nil
		}
	}
	name := nativeSliceCallable(req, *q)
	switch name {
	case "sort.Sort", "sort.Stable", "sort.IsSorted":
		if len(q.Args) != 1 || !requestHasCallbacks(req, *q) {
			return nil, false, nil
		}
		order, ok, err := r.goSourceSharedInterface(ctx, req, q.Args[0])
		if !ok || err != nil {
			return nil, ok, err
		}
		return goSourceSharedOrderResult(name, order)
	case "sort.Slice", "sort.SliceStable", "sort.SliceIsSorted":
		if len(q.Args) != 2 {
			return nil, false, nil
		}
		shared, ok := r.goSourceSharedSliceOf(q.Args[0])
		if !ok {
			return nil, false, nil
		}
		var less func(i, j int) (bool, error)
		switch comparator := q.Args[1]; {
		case comparator.Kind == "callback":
			fn, err := goSourceCallbackFunc(req, comparator)
			if err != nil {
				return nil, true, err
			}
			less = func(i, j int) (bool, error) {
				return r.goSourceCallbackBool(ctx, fn, []bashPPBridgeValue{goSourceSharedIndex(i), goSourceSharedIndex(j)})
			}
		case req.Bridge != nil && req.Bridge.madeFuncOwnedBy(comparator, req.CallbackOwner):
			// reflect.MakeFunc's Interface result is a dependency-owned callable
			// whose body still re-enters an original comparison closure. Invoke it
			// synchronously while sort works over the original backing array.
			less = func(i, j int) (bool, error) {
				values, err := req.Bridge.request(ctx, req, bashPPBridgeRequest{
					Op: "call", Receiver: &comparator,
					Args: []bashPPBridgeValue{goSourceSharedIndex(i), goSourceSharedIndex(j)},
				})
				if err != nil {
					return false, err
				}
				if len(values) != 1 || values[0].Kind != "bool" {
					return false, fmt.Errorf("gosource: reflected sort comparison must return bool")
				}
				return values[0].Text == "true", nil
			}
		default:
			return nil, false, nil
		}
		order := &goSourceSharedOrder{
			n:    len(shared.view),
			less: less,
			swap: shared.swap,
		}
		switch name {
		case "sort.Slice":
			name = "sort.Sort"
		case "sort.SliceStable":
			name = "sort.Stable"
		default:
			name = "sort.IsSorted"
		}
		return goSourceSharedOrderResult(name, order)
	}
	return nil, false, nil
}

func goSourceSharedIndex(i int) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "int", Type: "int", Text: strconv.Itoa(i)}
}

// goSourceSharedOrderResult runs the SDK algorithm the entry point names. The
// adapter's Len is already resolved, which is where sort.Sort and sort.Stable
// read it; an ordering over at most one element makes no further call in
// either the interface or the func variant.
func goSourceSharedOrderResult(name string, order *goSourceSharedOrder) ([]bashPPBridgeValue, bool, error) {
	switch name {
	case "sort.Sort":
		return nil, true, order.run(sort.Sort)
	case "sort.Stable":
		return nil, true, order.run(sort.Stable)
	}
	sorted, err := order.isSorted()
	if err != nil {
		return nil, true, err
	}
	return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(sorted)}}, true, nil
}

// goSourceSharedInterface binds the original Len/Less/Swap of a sort.Interface
// argument to the program's own storage. Len is called once here, where the
// SDK entry points call it.
func (r *Runner) goSourceSharedInterface(ctx context.Context, req bashPPEvalRequest, v bashPPBridgeValue) (*goSourceSharedOrder, bool, error) {
	var cell *bashPPCell
	switch {
	case v.Kind == "pointer" && v.Origin != 0:
		if req.Bridge == nil || v.Session != req.Bridge.id {
			return nil, true, fmt.Errorf("gosource: sort.Interface pointer belongs to another dependency session")
		}
		ptr, _ := req.Bridge.originLookup(v.Origin)
		if ptr == nil {
			return nil, true, fmt.Errorf("gosource: sort.Interface receiver identity expired")
		}
		cell = bashPPPointerCell(ptr)
	case v.sliceView != nil:
		if _, ok := r.goSourceSharedSliceOf(v); !ok {
			return nil, false, nil
		}
		named, ok := v.sliceView.typ.(*syntax.BashPPNamedType)
		if !ok || named.Name == nil {
			return nil, false, nil
		}
		cell = &bashPPCell{declType: v.sliceView.typ, typeName: named.Name.Value}
		bashPPStoreCellValue(cell, v.sliceView.view, v.sliceView.meta)
	default:
		return nil, false, nil
	}
	methods := r.bashPPMethods[cell.typeName]
	for _, method := range []string{"Len", "Less", "Swap"} {
		fn := methods[method]
		if fn == nil {
			return nil, false, nil
		}
		// A pointer method is not in a value's method set; only an original
		// pointer can supply it.
		if fn.decl.Receiver.Pointer && !cell.pointer {
			return nil, false, nil
		}
	}
	call := func(method string, args ...bashPPBridgeValue) ([]*bashPPCell, error) {
		bound, ok := r.bashPPBindMethod(cell, method, false)
		if !ok {
			return nil, errBashPPScalarInterrupted
		}
		return r.goSourceCallInterpretedCallback(ctx, bound, args)
	}
	result := func(method string, args ...bashPPBridgeValue) (bashPPBridgeValue, error) {
		results, err := call(method, args...)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		if len(results) != 1 {
			return bashPPBridgeValue{}, fmt.Errorf("gosource: original %s.%s must return one value", cell.typeName, method)
		}
		return r.bashPPBridgeCell(results[0])
	}
	length, err := result("Len")
	if err != nil {
		return nil, true, err
	}
	n, err := strconv.Atoi(length.Text)
	if err != nil || length.Kind != "int" || n < 0 {
		return nil, true, fmt.Errorf("gosource: original %s.Len returned %q", cell.typeName, length.Text)
	}
	return &goSourceSharedOrder{
		n: n,
		less: func(i, j int) (bool, error) {
			value, err := result("Less", goSourceSharedIndex(i), goSourceSharedIndex(j))
			if err != nil {
				return false, err
			}
			if value.Kind != "bool" {
				return false, fmt.Errorf("gosource: original %s.Less returned %s", cell.typeName, value.Kind)
			}
			return value.Text == "true", nil
		},
		swap: func(i, j int) error {
			_, err := call("Swap", goSourceSharedIndex(i), goSourceSharedIndex(j))
			return err
		},
	}, true, nil
}
