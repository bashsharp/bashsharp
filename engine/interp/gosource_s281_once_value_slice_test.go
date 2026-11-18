//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// cmd/internal/testdir (Go 1.27's std lib test harness) pins
//
//	var findExecCmd = sync.OnceValue(func() (execCmd []string) { ... })
//
// and later calls findExecCmd() repeatedly. A callback result of []string is
// a slice built from pure value-semantics elements: unlike a []reflect.Value
// result, whose elements are already dependency handles
// (bashPPNativeHandleSlice), its elements are interpreter-owned storage that
// Go itself copies out wherever the slice is read — exactly what the general
// "value-semantics parameters and supported results" rule already admits for
// a struct or array of scalars (bashPPCallbackValueType). The bridge used to
// stop at struct/array and hard-refuse any slice result it did not
// specifically special-case (a nested [][]byte, or map[string]int for the Go
// Tour helpers), well before the request-scoped copied-results gate
// (copiedResultsConsumer) ever got a chance to admit it. bashPPCallbackValueSlice
// generalizes the existing bashPPNativeHandleSlice treatment to any slice
// whose elements are value-semantics, so it is rebuilt from copied element
// values on reply, gated the same way: only a consumer that copies results
// out and never retains them (sync.OnceFunc/OnceValue/OnceValues,
// reflect.MakeFunc) may observe it. These tests run the unchanged original
// source and compare stdout/stderr/status against a real Go build, so both
// the admission and the OnceValue "compute once, cache forever" semantics
// are pinned together.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS281OnceValueValueSliceResult(t *testing.T) {
	for name, source := range map[string]string{
		"once_value_string_slice": `package main

import (
	"fmt"
	"sync"
)

var calls int

var findExecCmd = sync.OnceValue(func() (execCmd []string) {
	calls++
	return []string{"a", "b", "c"}
})

func main() {
	fmt.Println(findExecCmd())
	fmt.Println(findExecCmd())
	fmt.Println(calls)
}
`,
		"once_values_slice_and_scalar": `package main

import (
	"fmt"
	"sync"
)

var calls int

var get = sync.OnceValues(func() ([]int, error) {
	calls++
	return []int{1, 2, 3}, nil
})

func main() {
	a, errA := get()
	b, errB := get()
	fmt.Println(a, errA, b, errB, calls)
}
`,
		"once_value_struct_slice": `package main

import (
	"fmt"
	"sync"
)

type point struct{ X, Y int }

var get = sync.OnceValue(func() []point {
	return []point{{1, 2}, {3, 4}}
})

func main() {
	fmt.Println(get())
	fmt.Println(get())
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			oracle := runNativeOracle(t, dir, path, nil, "")
			got := runGoSourceRunner(t, dir, path, source, nil, "")
			if got.stdout != oracle.stdout || got.stderr != oracle.stderr || got.status != oracle.status {
				t.Fatalf("interp=%+v oracle=%+v", got, oracle)
			}
		})
	}
}

// A slice result stays admitted only for a reviewed copied-results consumer;
// a slice PARAMETER keeps the original refusal regardless of element type,
// because unlike a result it is not consumed once and copied out — an async
// or retained callback holding a copy could silently miss writes the
// original makes through its own backing array. bufio.Scanner.Split's own
// []byte parameter already pinned this before Sprint 281; this only confirms
// the new value-slice-result admission did not loosen it.
func TestS281ValueSliceParameterStillRefused(t *testing.T) {
	const source = `package main
import ("bufio";"strings")
func main(){s:=bufio.NewScanner(strings.NewReader(""));s.Split(func(b []byte,e bool)(int,[]byte,error){println("callback-ran");return 0,nil,nil});println("after")}`
	got := runGoSourceRunnerError(t, source)
	if !strings.Contains(got, "signature requires value-semantics parameters") {
		t.Fatalf("missing parameter boundary: %q", got)
	}
	if strings.Contains(got, "callback-ran") {
		t.Fatalf("unsupported body executed: %q", got)
	}
}

func TestS281TestingRunSynchronizesCopiedSliceCallback(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"
)

type row struct {
	name string
	data []int
}

func TestCopiedSliceRow(t *testing.T) {
	rows := []row{{"first", []int{1, 2}}, {"second", []int{4, 5}}}
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			tt.data[0] += len(tt.data)
			fmt.Println(tt.name, tt.data[0], rows[0].data[0], rows[1].data[0])
		})
	}
	fmt.Println("after", rows[0].data[0], rows[1].data[0])
}

var tests = []testing.InternalTest{{"TestCopiedSliceRow", TestCopiedSliceRow}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, nil)
	const want = "first 3 3 4\nsecond 6 3 6\nafter 3 6\nPASS\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
}

func TestS374TestingMainSynchronizesDescriptorSlices(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
)

var rows = []string{"before"}

func matchString(pat, str string) (bool, error) { return true, nil }

func TestDescriptorSliceCallback(t *testing.T) {
	rows[0] = "inside"
	fmt.Println(rows[0])
}

var tests = []testing.InternalTest{{"TestDescriptorSliceCallback", TestDescriptorSliceCallback}}

func main() {
	testing.Main(matchString, tests, nil, nil)
	fmt.Println("after", rows[0])
	os.Exit(0)
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, nil)
	const want = "inside\nPASS\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
}

func TestS374SortSliceSynchronizesCopiedSliceCallback(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"sort"
)

func main() {
	xs := []int{3, 1, 2}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i] == 3 {
			xs[i] = 4
		}
		return xs[i] < xs[j]
	})
	fmt.Println(xs)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	oracle := runNativeOracle(t, dir, path, nil, "")
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if got.stdout != oracle.stdout || got.stderr != oracle.stderr || got.status != oracle.status {
		t.Fatalf("interp=%+v oracle=%+v", got, oracle)
	}
}

func TestS374OriginalCallbackResultArity(t *testing.T) {
	for name, source := range map[string]string{
		"once_func_zero_results": `package main

import (
	"fmt"
	"sync"
)

var n int
var once = sync.OnceFunc(func() { n++; fmt.Println("called", n) })

func main() {
	once()
	once()
	fmt.Println("after", n)
}
`,
		"once_values_multiple_results": `package main

import (
	"fmt"
	"sync"
)

var calls int
var once = sync.OnceValues(func() (int, []string) {
	calls++
	return calls, []string{"linux", "amd64"}
})

func main() {
	a, b := once()
	c, d := once()
	fmt.Println(a, b, c, d, calls)
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			oracle := runNativeOracle(t, dir, path, nil, "")
			got := runGoSourceRunner(t, dir, path, source, nil, "")
			if got.stdout != oracle.stdout || got.stderr != oracle.stderr || got.status != oracle.status {
				t.Fatalf("interp=%+v oracle=%+v", got, oracle)
			}
		})
	}
}

func TestS281TestingMainStartSequentialCopiedSliceCallbackBarrier(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"
)

var sequentialParentContinued bool
var sequentialEvents [3]string

func TestSequentialBarrier(t *testing.T) {
	sequentialEvents[0] = "seq parent start"
	t.Run("child", func(t *testing.T) {
		sequentialEvents[1] = fmt.Sprintf("seq child parentContinued=%v", sequentialParentContinued)
	})
	sequentialEvents[2] = fmt.Sprintf("seq parent after child=%q parentContinued=%v", sequentialEvents[1], sequentialParentContinued)
	sequentialParentContinued = true
	for _, event := range sequentialEvents {
		fmt.Println(event)
	}
}

var tests = []testing.InternalTest{{"TestSequentialBarrier", TestSequentialBarrier}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, nil)
	const want = "seq parent start\nseq child parentContinued=false\nseq parent after child=\"seq child parentContinued=false\" parentContinued=false\nPASS\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
}

func TestS281TestingMainStartParallelCopiedSliceCallbackBarrier(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"
)

var parallelParentContinued bool
var parallelEvents [4]string

func TestParallelBarrier(t *testing.T) {
	parallelEvents[0] = "par parent start"
	t.Run("child", func(t *testing.T) {
		parallelEvents[1] = fmt.Sprintf("par child before parentContinued=%v", parallelParentContinued)
		t.Parallel()
		parallelEvents[3] = fmt.Sprintf("par child after parentContinued=%v parentSlot=%q", parallelParentContinued, parallelEvents[2])
		fmt.Println(parallelEvents[3])
	})
	parallelEvents[2] = fmt.Sprintf("par parent after childBefore=%q", parallelEvents[1])
	parallelParentContinued = true
	fmt.Println(parallelEvents[0])
	fmt.Println(parallelEvents[1])
	fmt.Println(parallelEvents[2])
}

var tests = []testing.InternalTest{{"TestParallelBarrier", TestParallelBarrier}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, nil)
	const want = "par parent start\npar child before parentContinued=false\npar parent after childBefore=\"par child before parentContinued=false\"\npar child after parentContinued=true parentSlot=\"par parent after childBefore=\\\"par child before parentContinued=false\\\"\"\nPASS\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
}

func TestS281TestingMainStartParallelFramesShareCaptures(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"
)

var parallelFrameEvents = []string{"", "", ""}

func TestParallelFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	alias := parallelFrameEvents[:]
	t.Cleanup(func() {
		fmt.Printf("cleanup %s %s %s\n", alias[0], alias[1], alias[2])
	})
	for i := 0; i < 2; i++ {
		i := i
		t.Run(fmt.Sprintf("child%d", i), func(t *testing.T) {
			alias[i] = fmt.Sprintf("child%d-before", i)
			t.Parallel()
			<-ctx.Done()
			func() {
				defer func() {
					if recover() != "frame-panic" {
						t.Fatal("panic did not stay in child frame")
					}
				}()
				panic("frame-panic")
			}()
			alias[i] = fmt.Sprintf("child%d-after", i)
		})
	}
	alias[2] = "parent-continued"
	cancel()
}

var tests = []testing.InternalTest{{"TestParallelFrames", TestParallelFrames}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, nil)
	const want = "cleanup child0-after child1-after parent-continued\nPASS\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
}

func TestS281TestingMainStartCallbackOwnsGoTasks(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"testing/internal/testdeps"
)

func TestCallbackTasks(t *testing.T) {
	ch := make(chan int)
	go func() { ch <- 7 }()
	fmt.Println("go", <-ch)

	var wg sync.WaitGroup
	wg.Go(func() { ch <- 11 })
	fmt.Println("waitgroup", <-ch)
	wg.Wait()
}

var tests = []testing.InternalTest{{"TestCallbackTasks", TestCallbackTasks}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, nil)
	const want = "go 7\nwaitgroup 11\nPASS\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
}

func TestS281EqualFuncSynchronizesInterfaceSliceIdentities(t *testing.T) {
	source := `package main

import (
	"fmt"
	"slices"
)

type Type interface{ ID() string }

type Basic struct{ name string }

func (b *Basic) ID() string { return b.name }

var (
	a = &Basic{"a"}
	b = &Basic{"b"}
	c = &Basic{"c"}

	xargs []Type
	yargs []Type
	calls int
)

func Identical(x, y Type) bool {
	calls++
	fmt.Println("cb", calls, x.ID(), y.ID(), x == y)
	if calls == 1 {
		xargs[1], yargs[1] = c, c
	}
	return x == y
}

func main() {
	xargs = []Type{a, b}
	yargs = []Type{a, b}
	fmt.Println(slices.EqualFunc(xargs, yargs, Identical))
	fmt.Println(xargs[1] == c, yargs[1] == c, calls, len(xargs), cap(xargs))

	var nils []Type
	empty := []Type{}
	fmt.Println(slices.EqualFunc(nils, empty, func(Type, Type) bool { panic("unused") }), calls)

	head := []Type{a, b, c}
	xs := head[:2:3]
	ys := head[:2:2]
	fmt.Println(slices.EqualFunc(xs, ys, Identical))
	fmt.Println(len(xs), cap(xs), len(ys), cap(ys), calls)
}
`
	differGoSource(t, source, nil, "")
}

func TestS281EqualFuncManagerIdentityAndMutation(t *testing.T) {
	for name, body := range map[string]string{
		"pointer identity":                `p:=&N{V:1};a:=[]T{p,p};b:=[]T{p,p};fmt.Println(slices.EqualFunc(a,b,func(x,y T)bool{return x==y}))`,
		"callback replaces later element": `p:=&N{V:1};q:=&N{V:2};a:=[]T{p,p};b:=[]T{p,q};count:=0;fmt.Println(slices.EqualFunc(a,b,func(x,y T)bool{count++;if count==1{a[1]=q};return x==y}),count,a[1]==q)`,
		"overlapping backing":             `p:=&N{V:1};q:=&N{V:2};back:=[]T{p,p,p};a,b:=back[:2],back[1:];count:=0;fmt.Println(slices.EqualFunc(a,b,func(x,y T)bool{count++;if count==1{back[2]=q};return x==y}),count,back[2]==q)`,
		"typed nil":                       `var p *N;fmt.Println(slices.EqualFunc([]T{p},[]T{nil},func(x,y T)bool{return x==y}))`,
	} {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, `package main
import("fmt";"slices")
type T interface{Value()int}
type N struct{V int}
func(n *N)Value()int{return n.V}
func main(){`+body+`}`, nil, "")
		})
	}
}

func TestS281EqualFuncManagerNativeSliceAndShortCircuit(t *testing.T) {
	for name, source := range map[string]string{
		"native slice result": `package main
import("fmt";"slices";"strings")
func main(){a:=[]string{"a","a"};b:=strings.Fields("a b");count:=0;fmt.Println(slices.EqualFunc(a,b,func(x,y string)bool{count++;if count==1{a[1]="b"};return x==y}),count)}`,
		"nil comparator empty": `package main
import("fmt";"slices")
func main(){var eq func(int,int)bool;fmt.Println(slices.EqualFunc([]int{},[]int{},eq))}`,
		"nil comparator mismatch": `package main
import("fmt";"slices")
func main(){var eq func(int,int)bool;fmt.Println(slices.EqualFunc([]int{1},[]int{},eq))}`,
	} {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, source, nil, "")
		})
	}
}
