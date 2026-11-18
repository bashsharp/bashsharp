//go:build full

package interp_test

import "testing"

// Sprint: #248; Stories: #700, #701, #702
//
// Seven upstream roots assert gc toolchain or runtime implementation
// observations that Bash#'s interpreted mode deliberately does not mimic
// (bashsharp docs/bashpp-go-implementation-claim.md, "Language, not
// implementation"; sh docs/bashpp-compiler-artifact-contracts.md, Sprint 248
// section): a -d=maymorestack compiler hook, a fieldtrack-experiment pragma,
// GC heap statistics and process address-space placement. Those roots stay
// FAIL by ID in interpreted mode. These independent programs pin the
// language behaviour each root also touches, compared across unchanged Go,
// interpreted Bash# and the compiled Bash# artifact.
func TestS248RuntimeObservationReplacementConformance(t *testing.T) {
	for name, source := range map[string]string{
		// maymorestack.go: the hook is gc-only; deep recursion with large
		// frames (which forces stack growth in gc) must still compute exactly.
		"deep-recursion-with-large-frames": `package main
import "fmt"
var calls int
func deep(n int) int {
	var frame [1 << 10]byte
	frame[n%len(frame)] = byte(n)
	calls++
	if n > 1 {
		return deep(n-1) + int(frame[n%len(frame)])
	}
	return int(frame[n%len(frame)])
}
func main() { fmt.Println(deep(127), calls) }
`,
		// issue47928.go: //go:nointerface only exists under the fieldtrack
		// experiment; without it, promoted pointer methods satisfy interfaces.
		"promoted-pointer-method-sets": `package main
import "fmt"
type U struct{}
func (*U) Bad() {}
type T struct{ U }
func main() {
	var i interface{} = new(T)
	_, ok := i.(interface{ Bad() })
	var v interface{} = T{}
	_, okValue := v.(interface{ Bad() })
	fmt.Println(ok, okValue)
}
`,
		// typeparam/mdempsky/15.go: the same fieldtrack-only pragma, on
		// methods promoted through embedded generic types.
		"promoted-generic-method-sets": `package main
import "fmt"
type E struct{}
func (E) EGood() {}
type X[T any] struct{ E }
func (X[T]) XGood() {}
type W struct{ X[int] }
func main() {
	var e, x, w interface{} = E{}, X[int]{}, W{}
	_, a := e.(interface{ EGood() })
	_, b := x.(interface{ EGood() })
	_, c := x.(interface{ XGood() })
	_, d := w.(interface{ XGood() })
	_, f := w.(interface{ Missing() })
	fmt.Println(a, b, c, d, f)
}
`,
		// bug260.go: element addresses printed through %p are memory
		// layout; the language guarantees sizes and alignments.
		"packed-struct-sizes-and-alignments": `package main
import (
	"fmt"
	"unsafe"
)
type T1 struct{ x uint8 }
type T2 struct{ x uint16 }
type T4 struct{ x uint32 }
func main() {
	var b1 [10]T1
	var b2 [10]T2
	var b4 [10]T4
	fmt.Println(unsafe.Sizeof(b1[0]), unsafe.Alignof(b1[0]), unsafe.Sizeof(b1))
	fmt.Println(unsafe.Sizeof(b2[0]), unsafe.Alignof(b2[0]), unsafe.Sizeof(b2))
	fmt.Println(unsafe.Sizeof(b4[0]), unsafe.Alignof(b4[0]), unsafe.Sizeof(b4))
	fmt.Println(&b1[0] != &b1[1], &b1[1] == &b1[1])
}
`,
		// issue15277.go: heap deltas are a GC observation; a KeepAlive'd
		// allocation keeps its contents and releasing it is well defined.
		"keepalive-allocation-lifecycle": `package main
import (
	"fmt"
	"runtime"
)
func fill(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
func main() {
	x := fill(1 << 16)
	sum := 0
	for i := 0; i < len(x); i += 4095 {
		sum += int(x[i])
	}
	runtime.KeepAlive(x)
	x = nil
	runtime.GC()
	fmt.Println(sum, x == nil)
}
`,
		// issue9110.go: leaked sudog counts are a GC observation; goroutines
		// abandoning a select on timeout must all complete.
		"select-timeout-churn-completes": `package main
import (
	"fmt"
	"sync"
	"time"
)
func main() {
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := make(chan int)
			select {
			case <-c:
			case <-time.After(time.Millisecond):
				mu.Lock()
				done++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	fmt.Println(done)
}
`,
		// nilptr.go: where a global lands in the address space is not a
		// language property; nil pointer indirection through large arrays and
		// structs must panic recoverably.
		"nil-large-array-indirection-panics": `package main
import "fmt"
type T struct {
	pad [1 << 10]byte
	i   int
}
func shouldPanic(name string, f func()) {
	defer func() { fmt.Println(name, recover() != nil) }()
	f()
}
func main() {
	var p *[1 << 30]byte
	var t *T
	var sink int
	shouldPanic("index", func() { sink = int(p[256<<20]) })
	shouldPanic("slice", func() { _ = p[0:] })
	shouldPanic("store", func() { p[1<<20] = 1 })
	shouldPanic("field", func() { sink = t.i })
	shouldPanic("fieldstore", func() { t.i = 1 })
	shouldPanic("arrayfield", func() { sink = int(t.pad[512]) })
	fmt.Println(sink)
}
`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
