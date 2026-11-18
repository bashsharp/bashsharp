//go:build full

package interp_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #376; Story: #1551; Story-ID: b22ffea69b27
//
// Array value semantics and bulk-copy throughput for large arrays (testdir:fixedbugs/issue20780b.go).
// When an array has scalar, pointer, channel, or slice elements, copying by-value
// requires only a bulk payload copy; element metadata carries no value semantics and can
// be safely shared without per-element traversal. When an array has value semantics
// (structs, nested arrays, or interfaces), recursive deep-copy is preserved.

// TestS376ArrayCopyValueSemantics tests that mutating a copied array does not mutate
// the original array, for scalar arrays.
func TestS376ArrayCopyValueSemantics(t *testing.T) {
	const src = `package main
import "fmt"

func main() {
	var a [5]int
	a[0] = 10
	b := a
	b[0] = 99
	b[1] = 42
	fmt.Println(a[0], a[1], b[0], b[1])
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyStructDeepCopy verifies that an array of structs preserves Go's value
// semantics (mutating a field in a copied struct does not mutate the original).
func TestS376ArrayCopyStructDeepCopy(t *testing.T) {
	const src = `package main
import "fmt"

type Point struct {
	X, Y int
}

func main() {
	var a [3]Point
	a[0].X = 1
	a[0].Y = 2
	b := a
	b[0].X = 99
	b[1].Y = 42
	fmt.Println(a[0].X, a[0].Y, a[1].Y, b[0].X, b[0].Y, b[1].Y)
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyNestedArrayDeepCopy verifies that nested arrays preserve value semantics.
func TestS376ArrayCopyNestedArrayDeepCopy(t *testing.T) {
	const src = `package main
import "fmt"

func main() {
	var a [2][3]int
	a[0][0] = 1
	a[0][1] = 2
	b := a
	b[0][0] = 99
	fmt.Println(a[0][0], a[0][1], b[0][0], b[0][1])
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyPointerAliasing verifies that an array of pointers preserves Go aliasing
// semantics (modifying pointee through copied pointer modifies pointee in original).
func TestS376ArrayCopyPointerAliasing(t *testing.T) {
	const src = `package main
import "fmt"

func main() {
	x, y := 10, 20
	a := [2]*int{&x, &y}
	b := a
	*b[0] = 99
	fmt.Println(*a[0], *b[0], x)
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopySliceAliasing verifies that an array of slices preserves Go aliasing
// semantics (elements view the same slice backing array).
func TestS376ArrayCopySliceAliasing(t *testing.T) {
	const src = `package main
import "fmt"

func main() {
	s := []int{1, 2, 3}
	a := [2][]int{s, s}
	b := a
	b[0][1] = 99
	fmt.Println(a[0][1], a[1][1], b[0][1], s[1])
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyInterfaceValueSemantics verifies that arrays of interfaces with struct values
// preserve value semantics.
func TestS376ArrayCopyInterfaceValueSemantics(t *testing.T) {
	const src = `package main
import "fmt"

type Box struct {
	V int
}

func main() {
	var a [2]any
	a[0] = Box{V: 1}
	b := a
	b[0] = Box{V: 99}
	fmt.Println(a[0].(Box).V, b[0].(Box).V)
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyFunctionPassByValue verifies that passing an array as argument to a function
// passes it by value.
func TestS376ArrayCopyFunctionPassByValue(t *testing.T) {
	const src = `package main
import "fmt"

func modify(x [4]int) {
	x[0] = 999
}

func main() {
	var a [4]int
	a[0] = 123
	modify(a)
	fmt.Println(a[0])
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyReturnByValue verifies that returning an array returns by value.
func TestS376ArrayCopyReturnByValue(t *testing.T) {
	const src = `package main
import "fmt"

func makeArray() [3]int {
	var a [3]int
	a[0] = 42
	return a
}

func main() {
	x := makeArray()
	y := x
	y[0] = 100
	fmt.Println(x[0], y[0])
}
`
	differGoSource(t, src, nil, "")
}

// TestS376ArrayCopyRangeSemantics verifies that range over an array ranges over a copy
// of the array at loop start.
func TestS376ArrayCopyRangeSemantics(t *testing.T) {
	const src = `package main
import "fmt"

func main() {
	a := [4]int{1, 2, 3, 4}
	var seen []int
	for i, v := range a {
		a[0] = 999
		seen = append(seen, v)
		_ = i
	}
	fmt.Println(seen, a[0])
}
`
	differGoSource(t, src, nil, "")
}

// issue20780bSource returns the exact issue20780b program with a scaled N. N
// keeps the root's float spelling (2e6 there): the front end lowers its uses
// as int(N) conversions, which an integer literal would not exercise.
func issue20780bSource(n int) string {
	return fmt.Sprintf(`package main

import "fmt"

const N = %de0

type Big = [N]int

var sink interface{}

func main() {
	g(0, f(0))

	x1 := f(1)
	sink = &x1
	g(1, x1)
	g(7, f(7))
	g(1, x1)

	x3 := f(3)
	sink = &x3
	g(1, x1)
	g(3, x3)

	h(f(0), x1, f(2), x3, f(4))
}

func f(k int) (x Big) {
	for i := range x {
		x[i] = k*N + i
	}
	return
}

func g(k int, x Big) {
	for i := range x {
		if x[i] != k*N+i {
			panic(fmt.Sprintf("x%%d[%%d] = %%d", k, i, x[i]))
		}
	}
}

func h(x0, x1, x2, x3, x4 Big) {
	g(0, x0)
	g(1, x1)
	g(2, x2)
	g(3, x3)
	g(4, x4)
}
`, n)
}

func runScaledRepro(t *testing.T, n int) (time.Duration, uint64, uint64) {
	t.Helper()
	src := issue20780bSource(n)
	p, err := gosource.Parse(strings.NewReader(src), "issue20780b.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("parse N=%d: %v", n, err)
	}

	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)

	start := time.Now()
	runner, err := interp.New(interp.Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatalf("interp.New: %v", err)
	}
	if err := runner.Run(t.Context(), p.File); err != nil {
		t.Fatalf("run N=%d: %v", n, err)
	}
	elapsed := time.Since(start)

	runtime.ReadMemStats(&m2)
	allocBytes := m2.TotalAlloc - m1.TotalAlloc
	allocCount := m2.Mallocs - m1.Mallocs

	return elapsed, allocBytes, allocCount
}

// TestS376ScaledReproMeasurements runs scaled repros of issue20780b and records
// elapsed time, total memory allocated, allocation count, and per-element rates.
func TestS376ScaledReproMeasurements(t *testing.T) {
	scales := []int{100, 1000, 5000, 10000}
	for _, n := range scales {
		elapsed, allocBytes, allocCount := runScaledRepro(t, n)
		bytesPerElem := float64(allocBytes) / float64(n)
		timePerIter := float64(elapsed.Nanoseconds()) / float64(18*n) // 7 f calls write n, 11 g calls read n
		t.Logf("N=%-6d elapsed=%-12v alloc=%-10d bytes (%8.1f B/elem) mallocs=%-8d (%.2f µs/iter)",
			n, elapsed, allocBytes, bytesPerElem, allocCount, timePerIter/1000)
	}
}

// TestS376Components breaks down copy cost vs loop cost for [N]int.
func TestS376Components(t *testing.T) {
	const n = 10000

	// 1. Array copy only (20 copies)
	copySrc := fmt.Sprintf(`package main
const N = %d
type Big = [N]int
//go:noinline
func pass(x Big) {}
func main() {
	var x Big
	for i := 0; i < 20; i++ {
		pass(x)
	}
}`, n)

	// 2. Loop in f only: 14M total in root (here 14*n)
	writeLoopSrc := fmt.Sprintf(`package main
const N = %d
type Big = [N]int
func main() {
	var x Big
	for k := 0; k < 7; k++ {
		for i := range x {
			x[i] = k*N + i
		}
	}
}`, n)

	// 3. Loop in g only: 22M total in root (here 11*n)
	readLoopSrc := fmt.Sprintf(`package main
const N = %d
type Big = [N]int
func main() {
	var x Big
	for k := 0; k < 11; k++ {
		for i := range x {
			if x[i] != k*N+i {
				// don't panic
			}
		}
	}
}`, n)

	runPart := func(name, src string) {
		p, err := gosource.Parse(strings.NewReader(src), name+".go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var m1, m2 runtime.MemStats
		runtime.ReadMemStats(&m1)
		start := time.Now()
		runner, err := interp.New(interp.Lang(syntax.LangBashPP))
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(t.Context(), p.File); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		runtime.ReadMemStats(&m2)
		t.Logf("%-15s: elapsed=%-10v totalAlloc=%-10d mallocs=%d",
			name, elapsed, m2.TotalAlloc-m1.TotalAlloc, m2.Mallocs-m1.Mallocs)
	}

	runPart("copy_20x", copySrc)
	runPart("write_7x", writeLoopSrc)
	runPart("read_11x", readLoopSrc)
}

// TestS376LoopFastPathSemantics pins the Go behaviour of the loop shapes the
// typed int evaluator, the reused range key and the copy-free key-only array
// range take over, next to the shapes that must stay on the general path.
func TestS376LoopFastPathSemantics(t *testing.T) {
	const src = `package main

import "fmt"

const N = 2e3

type Big = [N]int
type Celsius int

func fill(k int) (x Big) {
	for i := range x {
		x[i] = k*N + i
	}
	return
}

func check(k int, x Big) int {
	bad := 0
	for i := range x {
		if x[i] != k*N+i {
			bad++
		}
	}
	return bad
}

func main() {
	a := fill(3)
	fmt.Println(check(3, a), check(4, a), a[0], a[N-1])

	// A key-only range never reads the array, so assigning it in the body
	// changes neither the trip count nor later keys.
	var b [4]int
	trips := 0
	for i := range b {
		b = [4]int{9, 9, 9, 9}
		b[i] = i
		trips++
	}
	fmt.Println(trips, b)

	// A value range iterates a copy.
	c := [3]int{1, 2, 3}
	sum := 0
	for _, v := range c {
		c[2] = 100
		sum += v
	}
	fmt.Println(sum, c)

	// Assigning the key lasts for one iteration.
	var keys []int
	var d [3]int
	for i := range d {
		keys = append(keys, i)
		i = 40
		d[0] += i
	}
	fmt.Println(keys, d)

	// Each iteration has its own variable for a closure or an address.
	var fns []func() int
	var ptrs []*int
	var e [3]int
	for i := range e {
		fns = append(fns, func() int { return i })
	}
	for i := range e {
		ptrs = append(ptrs, &i)
	}
	fmt.Println(fns[0](), fns[1](), fns[2](), *ptrs[0], *ptrs[1], *ptrs[2])

	// Arithmetic, division, named and sized integer types.
	big := 1 << 40
	one, zero := 1, 0
	var w [2]int
	w[0] = big/4*3 + one
	w[1] = -big/8 - big/8
	fmt.Println(w, w[0] > w[1], 7/(one+one), -7%(one+one+one), 7&^one|8^one)
	var temps [3]Celsius
	var wide [3]int64
	for i := range temps {
		temps[i] = Celsius(i) * 2
		wide[i] = int64(i) << 40
	}
	fmt.Println(temps, wide, temps[1] == 2, wide[2] > wide[1])

	// Block scopes inside a reused iteration still shadow and expire.
	s := []int{5, 6, 7}
	total := 0
	for i := range s {
		if s[i] > 5 {
			total := s[i] * 10
			_ = total
		}
		{
			i := i + 100
			total += i
		}
		total += s[i]
	}
	fmt.Println(total)

	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		fmt.Println(one / zero)
	}()
	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		idx := 3
		s[idx] = one
	}()
	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		idx := -1
		fmt.Println(s[idx+zero] == one)
	}()
}
`
	differGoSource(t, src, nil, "")
}

// TestS376LoopFastPathConcurrentReaders runs concurrent key-only ranges and
// by-value copies over one shared array under the race detector's eye.
func TestS376LoopFastPathConcurrentReaders(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"sync"
)

var shared [64]int

func sum(x [64]int) int {
	n := 0
	for i := range x {
		n += x[i]
	}
	return n
}

func main() {
	for i := range shared {
		shared[i] = i
	}
	var wg sync.WaitGroup
	out := make([]int, 4)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := 0
			for i := range shared {
				n += shared[i]
			}
			out[w] = n + sum(shared)
		}()
	}
	wg.Wait()
	fmt.Println(out)
}
`
	differGoSource(t, src, nil, "")
}

// TestS376LoopAllocationBudget is the red-to-green gate for the root's loop
// shape as the CLI sees it: `const N = 2e6` reaches the evaluator as int(N),
// and while that conversion fell outside the typed int evaluator every
// iteration cost about 24 allocations. The budget is twice the measured 6.
func TestS376LoopAllocationBudget(t *testing.T) {
	const n = 20000
	_, _, mallocs := runScaledRepro(t, n)
	perIteration := float64(mallocs) / float64(18*n)
	t.Logf("N=%d mallocs=%d (%.2f per loop iteration)", n, mallocs, perIteration)
	if perIteration > 12 {
		t.Fatalf("%.2f allocations per loop iteration, budget 12", perIteration)
	}
}

// TestS376IntConversionSemantics pins the conversions next to the one the
// typed int evaluator takes: only int(v) of an int operand is an identity.
func TestS376IntConversionSemantics(t *testing.T) {
	const src = `package main

import "fmt"

const N = 2e3
const Half = 2.5

type ID int

func main() {
	var x [N]int
	k, f, id := 3, 7.9, ID(5)
	var small int8 = -3
	for i := range x {
		x[i] = k*N + i
	}
	fmt.Println(x[0], x[N-1], x[int(Half*2)], k*int(N)+int(f), int(f)*k)
	fmt.Println(int(id)+k, ID(k)+id, int(small)*k, int64(k)*N, float64(k)*Half)
	fmt.Println(x[int(f)] == k*N+7, x[k] != int(N)*k+k, uint(k)+N)
}
`
	differGoSource(t, src, nil, "")
}
