// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #279; Story: #757; Story-ID: 609c89bfa598

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPS279NativePointerReadOnlyFastPath tests that nativePointerReadOnlyRequest
// correctly identifies read-only operations without redundant string allocations.
func TestBashPPS279NativePointerReadOnlyFastPath(t *testing.T) {
	req := bashPPEvalRequest{
		Imports: map[string]string{
			"r":   "reflect",
			"f":   "fmt",
			"s":   "strings",
			"dep": "example.com/dep",
		},
	}
	value := bashPPBridgeValue{Kind: "handle", NativeType: "reflect.Value", Type: "reflect.Value"}
	typ := bashPPBridgeValue{Kind: "handle", NativeType: "*reflect.rtype", Type: "reflect.Type"}

	for _, tc := range []struct {
		name string
		q    bashPPBridgeRequest
		want bool
	}{
		{"non-call", bashPPBridgeRequest{Op: "get", Selector: "r.ValueOf"}, false},
		{"value-of", bashPPBridgeRequest{Op: "call", Selector: "r.ValueOf"}, true},
		{"type-of", bashPPBridgeRequest{Op: "call", Selector: "r.TypeOf"}, true},
		{"deep-equal", bashPPBridgeRequest{Op: "call", Selector: "r.DeepEqual"}, true},
		{"fmt-sprintf", bashPPBridgeRequest{Op: "call", Selector: "f.Sprintf"}, true},
		{"fmt-fprintf", bashPPBridgeRequest{Op: "call", Selector: "f.Fprintf"}, true},
		{"strings-has-prefix", bashPPBridgeRequest{Op: "call", Selector: "s.HasPrefix"}, false},
		{"dep-mutate", bashPPBridgeRequest{Op: "call", Selector: "dep.Mutate"}, false},
		{"value-field", bashPPBridgeRequest{Op: "call", Selector: "Field", Receiver: &value}, true},
		{"value-len", bashPPBridgeRequest{Op: "call", Selector: "Len", Receiver: &value}, true},
		{"type-field", bashPPBridgeRequest{Op: "call", Selector: "Field", Receiver: &typ}, true},
		{"value-set", bashPPBridgeRequest{Op: "call", Selector: "Set", Receiver: &value}, false},
		{"value-call", bashPPBridgeRequest{Op: "call", Selector: "Call", Receiver: &value}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativePointerReadOnlyRequest(req, tc.q); got != tc.want {
				t.Fatalf("nativePointerReadOnlyRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBashPPS279SharedOrderingAndHeap verifies that sort and container/heap
// algorithms operate correctly over interpreter storage through their fast paths.
func TestBashPPS279SharedOrderingAndHeap(t *testing.T) {
	const source = `package main
import (
	"container/heap"
	"fmt"
	"sort"
)

type IntHeap []int

func (h IntHeap) Len() int           { return len(h) }
func (h IntHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h IntHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *IntHeap) Push(x any) {
	*h = append(*h, x.(int))
}

func (h *IntHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func main() {
	nums := []int{5, 2, 6, 3, 1, 4}
	sort.Slice(nums, func(i, j int) bool {
		return nums[i] < nums[j]
	})
	fmt.Printf("sorted:%v\n", nums)

	h := &IntHeap{2, 1, 5}
	heap.Init(h)
	heap.Push(h, 3)
	p1 := heap.Pop(h)
	p2 := heap.Pop(h)
	fmt.Printf("heap:%v,%v\n", p1, p2)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "s279_order_heap.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("Run: %v; stderr=%q", err, stderr.String())
	}
	const want = "sorted:[1 2 3 4 5 6]\nheap:1,2"
	got := strings.TrimSpace(stdout.String())
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestBashPPS279CallMethodNameExtraction verifies goSourceCallMethodName extraction
// across both direct selector expressions and dotted multi-part call paths.
func TestBashPPS279CallMethodNameExtraction(t *testing.T) {
	selCall := &syntax.BashPPCall{
		CalleeExpr: &syntax.BashPPSelectorExpr{
			X:   &syntax.BashPPIdent{Name: &syntax.Lit{Value: "frames"}},
			Sel: &syntax.Lit{Value: "Next"},
		},
	}
	if method, ok := goSourceCallMethodName(selCall); !ok || method != "Next" {
		t.Fatalf("goSourceCallMethodName(selCall) = %q, %v; want Next, true", method, ok)
	}

	funCall := &syntax.BashPPCall{
		Fun: []*syntax.Lit{
			{Value: "strings"},
			{Value: "HasPrefix"},
		},
	}
	if method, ok := goSourceCallMethodName(funCall); !ok || method != "HasPrefix" {
		t.Fatalf("goSourceCallMethodName(funCall) = %q, %v; want HasPrefix, true", method, ok)
	}

	emptyCall := &syntax.BashPPCall{}
	if _, ok := goSourceCallMethodName(emptyCall); ok {
		t.Fatal("goSourceCallMethodName(emptyCall) expected false")
	}
}

// TestBashPPS279StackFramesIteration verifies that runtime.CallersFrames and
// frames.Next() operate correctly with goSourceCallMethodName fast paths.
func TestBashPPS279StackFramesIteration(t *testing.T) {
	const source = `package main
import (
	"fmt"
	"runtime"
)

func trace() string {
	pcs := make([]uintptr, 10)
	n := runtime.Callers(1, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	frame, more := frames.Next()
	return fmt.Sprintf("%s,%t", frame.Function, more)
}

func main() {
	result := trace()
	fmt.Println(result)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "s279_stack_frames.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("Run: %v; stderr=%q", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "main.trace") {
		t.Fatalf("output = %q, expected to contain main.trace", stdout.String())
	}
}
