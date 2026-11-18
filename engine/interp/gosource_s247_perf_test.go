//go:build full

package interp_test

// Sprint: #247; Story: #673; Story-ID: f24307569417
//
// Correctness guards for the Sprint 247 deadline-root performance changes:
// the cached sorted iteration order of typed-key maps, shared immutable
// zero elements of scalar arrays, and the scopes no longer pushed for a
// nameless range iteration, an empty block, or an unembedded selector root.

import (
	"testing"
	"time"
)

func TestS247PerfMapRangeOrderCache(t *testing.T) {
	const source = `package main

import "fmt"

func main() {
	m := map[int]int{}
	for i := 0; i < 50; i++ {
		m[i] = i * 10
	}
	// Drain by range-then-break: every start sees only live keys.
	seen := map[int]bool{}
	for len(m) > 25 {
		for k, v := range m {
			if seen[k] || v != k*10 {
				panic("stale entry")
			}
			seen[k] = true
			delete(m, k)
			break
		}
	}
	// A full range after deletions yields each remaining key once.
	sum, count := 0, 0
	for k := range m {
		if seen[k] {
			panic("deleted key ranged")
		}
		sum += k
		count++
	}
	fmt.Println(count, len(m))
	// Deleting ahead of the iteration hides those keys.
	hidden := 0
	for k := range m {
		for j := range m {
			if j != k {
				delete(m, j)
				hidden++
			}
		}
		break
	}
	n := 0
	for range m {
		n++
	}
	fmt.Println(n, len(m), hidden)
	// Re-insert deleted keys and range again: the order is rebuilt.
	for i := 0; i < 5; i++ {
		m[100+i] = i
	}
	total := 0
	for k, v := range m {
		if k >= 100 && v != k-100 {
			panic("bad value")
		}
		total++
	}
	fmt.Println(total, len(m))
	clear(m)
	for range m {
		panic("cleared map ranged")
	}
	m[7] = 70
	for k, v := range m {
		fmt.Println(k, v)
	}
	// Deleting and re-adding the same key during a range never yields it twice.
	r := map[string]int{"a": 1, "b": 2, "c": 3}
	visits := map[string]int{}
	for k := range r {
		visits[k]++
		delete(r, "c")
		r["c"] = 3
	}
	for k, v := range visits {
		if v > 1 {
			panic("key visited twice: " + k)
		}
	}
	fmt.Println(len(r))
}
`
	out, errout, err := runGoSource(t, "s247_map_order", source)
	if err != nil || errout != "" {
		t.Fatalf("err=%v stderr=%q out=%q", err, errout, out)
	}
	if want := "25 25\n1 1 24\n6 6\n7 70\n3\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

// maplinear's iterdelete shape must stay linear: 2n range-then-delete
// operations may not cost the quadratic multiple of n.
func TestS247PerfMapIterDeleteLinear(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"time"
)

func run(n int) time.Duration {
	start := time.Now()
	m := map[int]int{}
	for i := 0; i < n; i++ {
		m[i] = i
	}
	for i := 0; i < n; i++ {
		for k := range m {
			delete(m, k)
			break
		}
	}
	if len(m) != 0 {
		panic("not drained")
	}
	return time.Since(start)
}

func main() {
	a, b := run(2000), run(8000)
	fmt.Println(int64(b) < 10*int64(a))
}
`
	start := time.Now()
	out, errout, err := runGoSource(t, "s247_iterdelete", source)
	if err != nil || errout != "" {
		t.Fatalf("err=%v stderr=%q out=%q", err, errout, out)
	}
	if out != "true\n" {
		t.Fatalf("iterdelete not linear: %q (elapsed %v)", out, time.Since(start))
	}
}

func TestS247PerfArrayZeroElements(t *testing.T) {
	const source = `package main

import "fmt"

type P struct{ x int }

func main() {
	var b [4]byte
	b[1] = 7
	c := b
	c[2] = 9
	fmt.Println(b, c)
	var s [3]string
	s[0] = "x"
	fmt.Printf("%q\n", s)
	var f [2]float64
	f[1] = 1.5
	fmt.Println(f)
	var ok [2]bool
	ok[0] = true
	fmt.Println(ok)
	var ps [3]P
	ps[0].x = 1
	q := &ps[1]
	q.x = 2
	fmt.Println(ps)
	var ptrs [3]*int
	v := 5
	ptrs[2] = &v
	fmt.Println(ptrs[0] == nil, ptrs[1] == nil, *ptrs[2])
	var grid [2][3]int
	grid[1][2] = 4
	fmt.Println(grid)
	var ifs [2]interface{}
	ifs[1] = 3
	fmt.Println(ifs[0] == nil, ifs[1])
}
`
	out, errout, err := runGoSource(t, "s247_array_zero", source)
	if err != nil || errout != "" {
		t.Fatalf("err=%v stderr=%q out=%q", err, errout, out)
	}
	want := "[0 7 0 0] [0 7 9 0]\n[\"x\" \"\" \"\"]\n[0 1.5]\n[true false]\n[{1} {2} {0}]\ntrue true 5\n[[0 0 0] [0 0 4]]\ntrue 3\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestS247PerfNamelessRangeAndEmptyBlockScopes(t *testing.T) {
	const source = `package main

import "fmt"

func main() {
	n := 0
	for range 3 {
	}
	for range 3 {
		x := n
		n = x + 1
	}
	var fs []func() int
	for range 2 {
		y := n
		fs = append(fs, func() int { return y })
		n++
	}
	for _ = range []int{1, 2} {
		z := 10
		n += z
	}
	for _, _ = range map[int]int{1: 1} {
		{
		}
		w := 1
		n += w
	}
	{
	}
	fmt.Println(n, fs[0](), fs[1]())
}
`
	out, errout, err := runGoSource(t, "s247_scopes", source)
	if err != nil || errout != "" {
		t.Fatalf("err=%v stderr=%q out=%q", err, errout, out)
	}
	if want := "26 3 4\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestS247PerfEmbeddedSelectionAncestors(t *testing.T) {
	const source = `package main

import "fmt"

type Node struct {
	*Node
	v int
}

type Inner struct{ a int }

func (i Inner) get() int { return i.a }

type Mid struct{ Inner }
type Outer struct {
	Mid
	b int
}

func main() {
	n := &Node{Node: &Node{v: 2}, v: 1}
	fmt.Println(n.v, n.Node.v)
	o := Outer{Mid: Mid{Inner{a: 5}}, b: 6}
	o.a = 7
	fmt.Println(o.a, o.get(), o.b, o.Mid.Inner.a)
}
`
	out, errout, err := runGoSource(t, "s247_selection", source)
	if err != nil || errout != "" {
		t.Fatalf("err=%v stderr=%q out=%q", err, errout, out)
	}
	if want := "1 2\n7 7 6 7\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}
