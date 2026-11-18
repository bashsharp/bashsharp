//go:build full

package interp_test

// Sprint: #247; Story: #673; Story-ID: f24307569417

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestS247SieveOriginalSieve2 runs the assigned upstream root byte-for-byte:
// container/heap drives a pointer-receiver heap whose Push/Pop append to and
// reslice interpreter storage, and ring.Ring values are asserted back to int.
func TestS247SieveOriginalSieve2(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "chan", "sieve2.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != "29ff3b13ec95947d31f97d9a2fc378165c97581b7054b011017b61366bd7a3a3" {
		t.Fatalf("sieve2.go source digest = %s; update only after review", got)
	}
	out, stderr, err := runGoSource(t, "sieve2", string(source))
	if err != nil || out != "" || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// The heap algorithm runs over the original receiver: every Push/Pop callback
// grows or shrinks the program's own slice, aliases observe the swaps, Fix and
// Remove see indexes the Swap callbacks wrote, popped pointers keep identity,
// untyped constants take their default type, and the Less call count matches
// native Go.
func TestS247SieveSharedHeap(t *testing.T) {
	const source = `package main

import (
	"container/heap"
	"container/ring"
	"fmt"
)

type IntHeap []int

func (h IntHeap) Len() int           { return len(h) }
func (h IntHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h IntHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *IntHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *IntHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type Item struct {
	name  string
	prio  int
	index int
}

type PQ []*Item

var calls int

func (pq PQ) Len() int { return len(pq) }
func (pq PQ) Less(i, j int) bool {
	calls++
	return pq[i].prio > pq[j].prio
}
func (pq PQ) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
func (pq *PQ) Push(x any) {
	item := x.(*Item)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *PQ) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*pq = old[0 : n-1]
	return item
}

func main() {
	h := &IntHeap{5, 2, 8}
	heap.Init(h)
	heap.Push(h, 3)
	alias := *h
	fmt.Println(len(*h), cap(*h) >= 4, (*h)[0], []int(alias))
	for h.Len() > 0 {
		fmt.Print(heap.Pop(h), " ")
	}
	fmt.Println(len(*h))

	items := map[string]int{"banana": 3, "apple": 2, "pear": 4}
	pq := PQ{}
	var kept *Item
	for _, name := range []string{"banana", "apple", "pear"} {
		it := &Item{name: name, prio: items[name]}
		if name == "apple" {
			kept = it
		}
		heap.Push(&pq, it)
	}
	kept.prio = 5
	heap.Fix(&pq, kept.index)
	top := heap.Pop(&pq).(*Item)
	fmt.Println(top == kept, top.name, top.index, pq.Len())
	removed := heap.Remove(&pq, 1).(*Item)
	fmt.Println(removed.name, removed.index, pq[0].name, pq[0].index, calls)

	r := ring.New(3)
	r.Value = 7
	var e int
	e = r.Value.(int)
	_, ok := r.Next().Value.(int)
	fmt.Println(e, ok)
}
`
	out, stderr, err := runGoSource(t, "shared-heap", source)
	if err != nil || stderr != "" || out != "4 true 2 [2 3 8 5]\n2 3 5 8 0\ntrue apple -1 2\nbanana -1 pear 0 4\n7 false\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A heap element that would cross as a detached copy of interpreter storage
// (here a []int whose later write native Go makes visible through the heap)
// is not claimed by the shared heap and keeps the copied-reference refusal.
func TestS247SieveHeapCopiedElementRefused(t *testing.T) {
	const source = `package main

import (
	"container/heap"
	"fmt"
)

type SH [][]int

func (h SH) Len() int           { return len(h) }
func (h SH) Less(i, j int) bool { return h[i][0] < h[j][0] }
func (h SH) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *SH) Push(x any)        { *h = append(*h, x.([]int)) }
func (h *SH) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func main() {
	var h SH
	e := []int{4}
	heap.Push(&h, e)
	e[0] = 9
	fmt.Println(h[0][0])
}
`
	_, stderr, err := runGoSource(t, "heap-copied-element", source)
	if err == nil || !strings.Contains(err.Error()+stderr, "original callback with copied slice references is unsupported") {
		t.Fatalf("run=%v stderr=%q", err, stderr)
	}
}
