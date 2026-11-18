//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Callback frames share the registering runner's Bash++ program tables so a
// loop of t.Run does not snapshot the whole program per subtest. Parallel
// subtests are concurrent Go execution frames, so every table an executing
// body WRITES must become private to the frame before the write: a
// function-local type shadows an entry in the flat type registry, and a func
// literal bound to a variable appends to the closure registry. Both used to
// land in one table shared by every frame.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

const s281CallbackCOWSource = `package cowtest

import (
	"fmt"
	"sync"
	"testing"
)

func TestParallelLocalTypesAndClosures(t *testing.T) {
	const children = 8
	var ready sync.WaitGroup
	ready.Add(children)
	// Give the parent's closure registry spare capacity before any subtest is
	// registered. Appending grows a slice past the length it needs, so this is
	// the ordinary state of a program that ever bound a func literal — and it
	// is what makes a registry shared by reference, rather than merely read,
	// observable: every frame's first append lands on the same backing slot.
	warm1 := func() int { return 1 }
	warm2 := func() int { return 2 }
	if warm1()+warm2() != 3 {
		t.Fatal("warm closures")
	}
	for i := 0; i < children; i++ {
		i := i
		t.Run(fmt.Sprintf("child%d", i), func(t *testing.T) {
			t.Parallel()
			// Rendezvous so every frame runs its body — the local type
			// declaration and the closure loop — at the same time.
			ready.Done()
			ready.Wait()
			type local struct {
				n int
			}
			total := 0
			for j := 0; j < 16; j++ {
				j := j
				// j+1, never 0: a frame that resolves a handle to a
				// SIBLING's closure then adds to the sibling's total, and
				// both totals come out wrong.
				add := func() {
					total += local{n: j + 1}.n
				}
				add()
			}
			fmt.Println("child", i, "total", total)
		})
	}
}
`

// TestS281ParallelCallbackFramesDeclareLocalTypes is the concurrency oracle for
// the shared program tables: eight parallel callback frames each declare a
// function-local type and build sixteen closures while the others do the same.
// Under -race a shared registry shows up as a data race or as a fatal
// "concurrent map writes"; without -race it shows up as a lost closure handle
// or a type resolved to another frame's declaration.
func TestS281ParallelCallbackFramesDeclareLocalTypes(t *testing.T) {
	testPackage := gosource.PackageSpec{
		Path:    "example.com/cowtest",
		Sources: []gosource.Source{s249Source("cow_test.go", s281CallbackCOWSource)},
	}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"
	cowtest "example.com/cowtest"
)

var tests = []testing.InternalTest{{"TestParallelLocalTypesAndClosures", cowtest.TestParallelLocalTypesAndClosures}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{testPackage}, "-test.v", "-test.parallel=8")
	if err != nil || got.stderr != "" || got.status != 0 || !strings.HasSuffix(got.stdout, "PASS\n") {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
	for i := range 8 {
		// Each closure adds its own loop index, so a frame whose closure
		// registry was overwritten by a sibling reports the wrong total.
		marker := fmt.Sprintf("child %d total 136\n", i)
		if strings.Count(got.stdout, marker) != 1 {
			t.Fatalf("child %d did not report its own closures exactly once:\n%s", i, got.stdout)
		}
	}
}
