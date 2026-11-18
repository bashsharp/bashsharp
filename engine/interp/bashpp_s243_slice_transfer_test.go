//go:build full

package interp

// Sprint: #243; Story: #675; Story-ID: 6ee00d8029b3
//
// Classification of an exclusive slice transfer (bashpp_native_transfer.go):
// the proof is the backend-asserted generated test main at a program-package
// call site with bare-binding slice arguments of dependency-owned elements.
// Nothing keys on the callee, and every weaker shape keeps the refusal.

import (
	"fmt"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
)

func transferDescriptor(typ string) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "handle", Type: typ, NativeType: typ, Callbacks: true, Session: "session", Handle: 1}
}

// transferRequest is the generated driver's MainStart call as the transport
// sees it: a TestDeps handle, then four descriptor slices each read from its
// own package-level binding.
func transferRequest(t *testing.T) (bashPPEvalRequest, bashPPBridgeRequest) {
	t.Helper()
	req := bashPPEvalRequest{TestMain: true, ImportPath: "example.com/app.test", Imports: map[string]string{"testing": "testing"}}
	q := bashPPBridgeRequest{Op: "call", Selector: "testing.MainStart", sourceProgram: true, Args: []bashPPBridgeValue{
		{Kind: "handle", Type: "testing/internal/testdeps.TestDeps", NativeType: "testing/internal/testdeps.TestDeps", Session: "session", Handle: 5},
	}}
	q.argCells = make([]*bashPPCell, 5)
	q.transferProof = []bool{false, true, true, true, true}
	for i, typ := range []string{"[]testing.InternalTest", "[]testing.InternalBenchmark", "[]testing.InternalFuzzTarget", "[]testing.InternalExample"} {
		var view []any
		var elements []bashPPBridgeValue
		if i == 0 {
			first, second := transferDescriptor("testing.InternalTest"), transferDescriptor("testing.InternalTest")
			second.Handle = 2
			view = []any{&first, &second}
			elements = []bashPPBridgeValue{first, second}
		} else {
			view = []any{}
		}
		q.argCells[i+1] = &bashPPCell{vr: expand.NewObject(view)}
		q.Args = append(q.Args, bashPPBridgeValue{Kind: "slice", Type: typ, Elements: elements, sliceView: &bashPPNativeSlice{view: view}})
	}
	return req, q
}

func TestS243SliceTransferAdmitsGeneratedDriver(t *testing.T) {
	req, q := transferRequest(t)
	if !nativeSliceTransferable(req, &q) {
		t.Fatal("the generated driver's descriptor slices were not transferable")
	}
	if got := fmt.Sprint(q.Transfers); got != "[1 2 3 4]" {
		t.Fatalf("transfers = %s, want [1 2 3 4]", got)
	}
	if !nativeSliceTransferOnly(q) {
		t.Fatal("a transfer beside a dependency handle is not transfer-only")
	}
	if !requestHasCallbacks(req, q) {
		t.Fatal("the descriptor elements should carry callbacks")
	}
}

func TestS243SliceTransferRefusesWeakerShapes(t *testing.T) {
	cases := map[string]func(*bashPPEvalRequest, *bashPPBridgeRequest){
		"no generated test main fact":   func(req *bashPPEvalRequest, q *bashPPBridgeRequest) { req.TestMain = false },
		"call site in a linked package": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) { q.sourceProgram = false },
		"slice is not the bare binding": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) { q.argCells[1] = nil },
		"frontend ownership is absent":  func(req *bashPPEvalRequest, q *bashPPBridgeRequest) { q.transferProof[1] = false },
		"same binding is repeated": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) {
			q.argCells[2] = q.argCells[1]
		},
		"element is an original pointer": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) {
			q.Args[1].Elements[0] = bashPPBridgeValue{Kind: "pointer", Type: "*main.T", Origin: 7, Elements: []bashPPBridgeValue{{Kind: "struct", Type: "main.T"}}}
		},
		"element is a local struct": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) {
			q.Args[1].Elements[0] = bashPPBridgeValue{Kind: "struct", Type: "main.T", Fields: map[string]bashPPBridgeValue{"N": {Kind: "int", Type: "int", Text: "1"}}}
		},
		"element is a bare callback": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) {
			q.Args[1].Elements[0] = bashPPBridgeValue{Kind: "callback", Type: "func()", Session: "session", Handle: 9}
		},
		"element carries an origin": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) { q.Args[1].Elements[0].Origin = 3 },
		"nested original slice": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) {
			inner := []any{}
			q.Args[1].Elements[0] = bashPPBridgeValue{Kind: "slice", Type: "[]int", sliceView: &bashPPNativeSlice{view: inner}}
		},
		"receiver holds an original slice": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) {
			q.Receiver = &bashPPBridgeValue{Kind: "slice", Type: "[]int", sliceView: &bashPPNativeSlice{view: []any{}}}
		},
		"not a call": func(req *bashPPEvalRequest, q *bashPPBridgeRequest) { q.Op = "construct" },
	}
	for name, weaken := range cases {
		t.Run(name, func(t *testing.T) {
			req, q := transferRequest(t)
			weaken(&req, &q)
			if nativeSliceTransferable(req, &q) {
				t.Fatalf("transferable: %v", q.Transfers)
			}
			if len(q.Transfers) != 0 {
				t.Fatalf("a refused request kept transfers %v", q.Transfers)
			}
		})
	}
}

// A transfer admits nothing else on the request: a copied local struct or a
// bare callback beside the transferred slices is still interpreter storage
// the dependency could mutate or retain.
func TestS243SliceTransferOnlyRequiresDependencyOwnedCompanions(t *testing.T) {
	for name, companion := range map[string]bashPPBridgeValue{
		"local struct":  {Kind: "struct", Type: "main.T", Fields: map[string]bashPPBridgeValue{"N": {Kind: "int", Type: "int", Text: "1"}}},
		"bare callback": {Kind: "callback", Type: "func()", Session: "session", Handle: 9},
		"local map":     {Kind: "map", Type: "map[string]int"},
		"origin handle": {Kind: "handle", Type: "*bytes.Buffer", Origin: 4, Session: "session", Handle: 8},
	} {
		t.Run(name, func(t *testing.T) {
			req, q := transferRequest(t)
			q.Args[0] = companion
			if !nativeSliceTransferable(req, &q) {
				t.Fatal("the descriptor slices themselves are still transferable")
			}
			if nativeSliceTransferOnly(q) {
				t.Fatalf("transfer-only with a %s companion", name)
			}
		})
	}
	req, q := transferRequest(t)
	q.Transfers = nil
	if nativeSliceTransferOnly(q) {
		t.Fatal("a request without transfers is never transfer-only")
	}
	_ = req
}

// The transfer reply rebinds the supplying binding to the worker's handle on
// the decoded slice, keeping the callback-bearing mark; a reply that does not
// answer every transfer, or answers with something other than a handle, is
// refused rather than applied.
func TestS243SliceTransferRebindsSupplyingBinding(t *testing.T) {
	_, q := transferRequest(t)
	q.Transfers = []int{1, 2, 3, 4}
	s := &bashPPNativeSession{id: "session"}
	var reply bashPPBridgeResponse
	for i, index := range q.Transfers {
		reply.Transferred = append(reply.Transferred, bashPPNativeSliceBuffer{Index: index, Length: len(q.Args[index].Elements), Value: bashPPBridgeValue{Kind: "handle", Type: q.Args[index].Type, NativeType: q.Args[index].Type, Handle: uint64(100 + i)}})
	}
	if err := s.applyNativeSliceTransfers(q, reply); err != nil {
		t.Fatal(err)
	}
	for i, index := range q.Transfers {
		cell := q.argCells[index]
		handle, ok := cell.vr.Obj.(*bashPPBridgeValue)
		if !ok || handle.Kind != "handle" || handle.Handle != uint64(100+i) || handle.Session != "session" || handle.Type != q.Args[index].Type {
			t.Fatalf("binding %d = %+v, want the worker's handle", index, cell.vr.Obj)
		}
		if cell.valueMeta != nil || cell.declType != nil || cell.pointer || cell.interfaceValue != nil {
			t.Fatalf("binding %d kept interpreter collection state", index)
		}
		if handle.Callbacks != (index == 1) {
			t.Fatalf("binding %d callbacks = %v", index, handle.Callbacks)
		}
	}

	for name, mutate := range map[string]func(*bashPPBridgeResponse){
		"missing answer": func(r *bashPPBridgeResponse) { r.Transferred = r.Transferred[:3] },
		"wrong index":    func(r *bashPPBridgeResponse) { r.Transferred[0].Index = 2 },
		"not a handle":   func(r *bashPPBridgeResponse) { r.Transferred[0].Value.Kind = "slice" },
		"wrong length":   func(r *bashPPBridgeResponse) { r.Transferred[0].Length = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			_, q := transferRequest(t)
			q.Transfers = []int{1, 2, 3, 4}
			var reply bashPPBridgeResponse
			for i, index := range q.Transfers {
				reply.Transferred = append(reply.Transferred, bashPPNativeSliceBuffer{Index: index, Length: len(q.Args[index].Elements), Value: bashPPBridgeValue{Kind: "handle", Type: q.Args[index].Type, Handle: uint64(100 + i)}})
			}
			mutate(&reply)
			if err := s.applyNativeSliceTransfers(q, reply); err == nil {
				t.Fatal("an invalid transfer reply was applied")
			}
			if _, rebound := q.argCells[1].vr.Obj.(*bashPPBridgeValue); rebound {
				t.Fatal("a refused reply rebound the binding")
			}
		})
	}
}

func TestS243TestingMRunOwnsCallbacksSynchronously(t *testing.T) {
	request := bashPPBridgeRequest{
		Op:       "call",
		Selector: "Run",
		Receiver: &bashPPBridgeValue{Kind: "handle", NativeType: "*testing.M", Callbacks: true, Session: "session", Handle: 1},
	}
	if !synchronousFunctionCallback(bashPPEvalRequest{}, request) {
		t.Fatal("(*testing.M).Run was not classified as a synchronous callback owner")
	}
	request.Receiver.NativeType = "*example.M"
	if synchronousFunctionCallback(bashPPEvalRequest{}, request) {
		t.Fatal("an unrelated M.Run was classified as a synchronous callback owner")
	}
	request.Receiver.NativeType = "*testing.M"
	request.Selector = "Retain"
	if synchronousFunctionCallback(bashPPEvalRequest{}, request) {
		t.Fatal("another method of testing.M was classified as a synchronous callback owner")
	}
}
