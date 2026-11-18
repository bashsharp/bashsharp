package interp

// Sprint: #247; Story: #673; Story-ID: f24307569417
//
// Shared-storage heap: container/heap runs over the interpreter's own
// heap.Interface receiver.
//
// container/heap's Init/Push/Pop/Remove/Fix call back into the receiver's
// Len/Less/Swap/Push/Pop, and the conventional receiver is a pointer to a
// slice whose Push and Pop methods append to and reslice it. Over the ordinary
// transport the dependency would drive a decoded copy of that slice while each
// callback grows or shrinks the original, so after the first Push the two
// disagree on length, capacity and order. The copied-reference refusal in
// prepareNativeSliceBuffers guards exactly that.
//
// As with the sort orderings (bashpp_native_shared_order.go) there is one
// storage instead of two: the SDK's own package container/heap runs in this
// process over an adapter whose five methods are the ORIGINAL methods bound to
// the ORIGINAL receiver — the origin-bearing pointer resolves to the program's
// own pointer cell. Every append, reslice and element write a callback makes
// lands in interpreter storage directly, and the next callback (and the
// program after the call) observes the current length and capacity. The
// algorithm is the SDK's, so the Less/Swap/Push/Pop sequence matches native Go
// call for call.
//
// The element a heap.Push hands the receiver's Push, and the element the
// receiver's Pop returns to heap.Pop, are opaque to the algorithm: they pass
// through unchanged. Nothing is retained past the request, and every callback
// runs synchronously on the requesting goroutine; a callback panic, exit or
// failure stops the algorithm at once, leaving the partial state native Go
// leaves. A receiver that is not an origin-bearing pointer, or whose five
// methods are not all original, is not claimed and meets the ordinary
// transport and its refusal.

import (
	"container/heap"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// goSourceSharedHeapIface adapts an interpreter-side heap.Interface. A failing
// callback aborts the SDK algorithm through the same private panic the
// ordering adapter uses.
type goSourceSharedHeapIface struct {
	goSourceSharedOrder
	length func() (int, error)
	push   func(bashPPBridgeValue) error
	pop    func() (bashPPBridgeValue, error)
}

func (h *goSourceSharedHeapIface) fail(err error) {
	h.err = err
	panic(goSourceSharedOrderAbort{})
}

func (h *goSourceSharedHeapIface) Len() int {
	n, err := h.length()
	if err != nil {
		h.fail(err)
	}
	return n
}

func (h *goSourceSharedHeapIface) Push(x any) {
	if err := h.push(x.(bashPPBridgeValue)); err != nil {
		h.fail(err)
	}
}

func (h *goSourceSharedHeapIface) Pop() any {
	v, err := h.pop()
	if err != nil {
		h.fail(err)
	}
	return v
}

// runHeap drives one container/heap entry point, converting an aborted
// callback back into its error.
func (h *goSourceSharedHeapIface) runHeap(algorithm func()) (err error) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(goSourceSharedOrderAbort); !ok {
				panic(p)
			}
			err = h.err
		}
	}()
	algorithm()
	return nil
}

// goSourceSharedHeap answers the container/heap entry points over interpreter
// storage. handled is false for every other request and for a receiver shape
// the shared binding does not cover.
func (r *Runner) goSourceSharedHeap(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) ([]bashPPBridgeValue, bool, error) {
	if q.Op != "call" || q.Spread || q.Receiver != nil {
		return nil, false, nil
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || req.Imports[alias] != "container/heap" {
		return nil, false, nil
	}
	want := 0
	switch name {
	case "Init", "Pop":
		want = 1
	case "Push", "Remove", "Fix":
		want = 2
	default:
		return nil, false, nil
	}
	name = "container/heap." + name
	if len(q.Args) != want {
		return nil, false, nil
	}
	v := q.Args[0]
	if v.Kind != "pointer" || v.Origin == 0 {
		return nil, false, nil
	}
	if req.Bridge == nil || v.Session != req.Bridge.id {
		return nil, true, fmt.Errorf("gosource: heap.Interface pointer belongs to another dependency session")
	}
	ptr, _ := req.Bridge.originLookup(v.Origin)
	if ptr == nil {
		return nil, true, fmt.Errorf("gosource: heap.Interface receiver identity expired")
	}
	cell := bashPPPointerCell(ptr)
	methods := r.bashPPMethods[cell.typeName]
	for _, method := range []string{"Len", "Less", "Swap", "Push", "Pop"} {
		if methods[method] == nil {
			return nil, false, nil
		}
	}
	if name == "container/heap.Push" && !goSourceHeapElementShared(q.Args[1], req) {
		return nil, false, nil
	}
	index := -1
	if name == "container/heap.Remove" || name == "container/heap.Fix" {
		i, err := strconv.Atoi(q.Args[1].Text)
		if q.Args[1].Kind != "int" || err != nil {
			return nil, false, nil
		}
		index = i
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
	h := &goSourceSharedHeapIface{
		goSourceSharedOrder: goSourceSharedOrder{
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
		},
		length: func() (int, error) {
			value, err := result("Len")
			if err != nil {
				return 0, err
			}
			n, err := strconv.Atoi(value.Text)
			if err != nil || value.Kind != "int" || n < 0 {
				return 0, fmt.Errorf("gosource: original %s.Len returned %q", cell.typeName, value.Text)
			}
			return n, nil
		},
		push: func(x bashPPBridgeValue) error {
			bound, ok := r.bashPPBindMethod(cell, "Push", false)
			if !ok {
				return errBashPPScalarInterrupted
			}
			params := bashppParams(bound.params())
			if len(params) != 1 {
				return fmt.Errorf("gosource: original %s.Push must take one value", cell.typeName)
			}
			arg := &bashPPCell{declType: params[0].typ, typeName: bashPPTypeText(params[0].typ)}
			if _, ok := r.bashPPInterfaceType(params[0].typ); !ok {
				return fmt.Errorf("gosource: original %s.Push must take an interface value", cell.typeName)
			}
			if x.Kind == "pointer" {
				// The element IS the program's own pointer: bind that
				// identity as the interface's dynamic value.
				elem, resolved, err := r.bashPPBridgeOriginPointer(x)
				if err != nil || !resolved {
					return fmt.Errorf("gosource: heap.Push element identity expired")
				}
				payload := bashPPPointerCell(elem)
				bashPPStoreCellValue(arg, nil, &bashPPCollectionMeta{kind: "interface", typ: params[0].typ,
					interfaceValue: &bashPPInterfaceValue{cell: payload, dynamic: payload.declType}})
			} else {
				// A scalar, nil or dependency handle: the interface value
				// the native result binders build for the same static type.
				// An untyped constant argument takes its default type, as
				// Go's conversion to the interface parameter does.
				if x.Type == "" {
					if scalar, err := x.scalar(); err == nil {
						x.Type = bashPPDefaultScalarTypeName(scalar.value.Kind())
					}
				}
				arg = r.goSourceNativeTypedResultCell(x, params[0].typ)
			}
			_, err := r.goSourceInvokeCallbackCells(ctx, bound, []*bashPPCell{arg}, []string{""})
			return err
		},
		pop: func() (bashPPBridgeValue, error) {
			// The popped element never crosses: the result carries the
			// original Pop's own result cell (localCell), which the native
			// result binders hand back as the interpreter value itself.
			results, err := call("Pop")
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			if len(results) != 1 {
				return bashPPBridgeValue{}, fmt.Errorf("gosource: original %s.Pop must return one value", cell.typeName)
			}
			value, err := r.bashPPBridgeCell(results[0])
			value.localCell = results[0]
			return value, err
		},
	}
	var out []bashPPBridgeValue
	err := h.runHeap(func() {
		switch name {
		case "container/heap.Init":
			heap.Init(h)
		case "container/heap.Push":
			heap.Push(h, q.Args[1])
		case "container/heap.Pop":
			out = []bashPPBridgeValue{heap.Pop(h).(bashPPBridgeValue)}
		case "container/heap.Remove":
			out = []bashPPBridgeValue{heap.Remove(h, index).(bashPPBridgeValue)}
		case "container/heap.Fix":
			heap.Fix(h, index)
		}
	})
	if err != nil {
		return nil, true, err
	}
	return out, true, nil
}

// goSourceHeapElementShared reports an element whose transport is exact: a
// scalar or nil, a dependency handle, or an origin-bearing pointer, which
// resolves back to the program's own pointer. Any other shape (a slice, map,
// struct or callback value) would cross as a detached copy of interpreter
// storage and is not admitted.
func goSourceHeapElementShared(v bashPPBridgeValue, req bashPPEvalRequest) bool {
	if v.Kind == "pointer" {
		return v.Origin != 0 && req.Bridge != nil && v.Session == req.Bridge.id
	}
	return bridgeValueDependencyOwned(v)
}
