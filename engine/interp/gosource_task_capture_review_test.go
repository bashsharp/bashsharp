//go:build full

package interp_test

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// Integration controls for the two sprint118 raw submissions that met here:
// the GoSource lexical task-capture analysis (story #52) and
// `sync.WaitGroup.Go` (story #54).
//
// Each submission was reviewed on its own against a task snapshot that did not
// yet have the other's. Together they make claims neither could measure alone:
// that `wg.Go` uses the SAME capture analysis and the SAME parent-side callee
// pin as `go`, that a launch pins the function value the launching goroutine
// held, that a local binding shadows a package-level `func` of the same name,
// and that a nested launch reaches the counter its grandparent declared.
//
// Everything here is an unchanged, compilable original Go program measured in
// all three modes by captureThreeModes — the Go toolchain, the interpreter and
// the lowered program must agree — so a regression shows up as a different
// number rather than as different metadata.

import "testing"

// TestGoSourceWaitGroupGoSharedLocalCounter is `wg.Go` standing exactly where
// `go func(){…}()` stands in TestGoSourceCaptureNativeMutexSharedLocal.
//
// Before this review, `wg.Go` took the deep-copy snapshot: it called
// bashPPTaskSnapshot with no capture set at all, so the launched body
// incremented a private copy of `counter` and the program printed 0 while
// every WaitGroup control still passed — the counter is not what a WaitGroup
// test looks at. It now goes through the same helper `go` does.
func TestGoSourceWaitGroupGoSharedLocalCounter(t *testing.T) {
	captureThreeModes(t, `package main

import (
	"fmt"
	"sync"
)

func main() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	counter := 0
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			mu.Lock()
			counter++
			mu.Unlock()
		})
	}
	wg.Wait()
	fmt.Println("counter", counter)
}
`)
}

// TestGoSourceLaunchPinsShadowedFuncVariable pins the two lookup rules a launch
// must follow, in one program where breaking either changes the total.
//
// SHADOWING: `work` is bound in main to a closure, and there is also a
// package-level `func work()`. Go resolves the launch to the innermost binding.
// The capture analysis used to consult the declared-func table FIRST, so it
// walked the package-level body — computing a capture set for a function that
// was never launched — while the pin ran the local one.
//
// REASSIGNMENT: `wg.Go(work)` evaluates `work` in the calling goroutine, so the
// first launch runs the closure the variable held THEN, not the one it holds
// when the task is finally scheduled. Resolving the name again inside the task
// would let the second assignment win both launches.
//
// globalRuns is 0 in the answer: it is the witness that the package-level
// `work` never ran.
func TestGoSourceLaunchPinsShadowedFuncVariable(t *testing.T) {
	captureThreeModes(t, `package main

import (
	"fmt"
	"sync"
)

var globalRuns int

func work() {
	globalRuns += 100
}

func main() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	counter := 0

	work := func() {
		mu.Lock()
		counter += 1
		mu.Unlock()
	}
	wg.Go(work)

	work = func() {
		mu.Lock()
		counter += 10
		mu.Unlock()
	}
	wg.Go(work)

	wg.Wait()
	fmt.Println("counter", counter, "globalRuns", globalRuns)
}
`)
}

// TestGoSourceNestedLaunchesShareGrandparentLocal is the nested-task control.
//
// The inner launches run inside a task, so their capture classification may not
// re-read a payload the parent is concurrently writing. The ownership record is
// cloned into each task instead (see [Runner.bashPPGoSourceSharable]), which is
// what lets the innermost body reach the `total` its grandparent declared. If a
// level ever fell back to the deep copy the total drops; if a level ran a body
// twice or not at all it changes too.
func TestGoSourceNestedLaunchesShareGrandparentLocal(t *testing.T) {
	captureThreeModes(t, `package main

import (
	"fmt"
	"sync"
)

func main() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	total := 0

	for i := 0; i < 4; i++ {
		wg.Go(func() {
			var inner sync.WaitGroup
			for j := 0; j < 3; j++ {
				inner.Go(func() {
					mu.Lock()
					total++
					mu.Unlock()
				})
			}
			inner.Wait()
			mu.Lock()
			total += 100
			mu.Unlock()
		})
	}
	wg.Wait()
	fmt.Println("total", total)
}
`)
}

// TestGoSourceMixedLaunchFormsShareOneCounter runs `go`, `wg.Go` and a
// `go`-launched named function against one synchronized local, so the two
// launch paths are measured as one rule rather than two.
func TestGoSourceMixedLaunchFormsShareOneCounter(t *testing.T) {
	captureThreeModes(t, `package main

import (
	"fmt"
	"sync"
)

func main() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var done sync.WaitGroup
	counter := 0

	bump := func() {
		mu.Lock()
		counter += 3
		mu.Unlock()
	}

	for i := 0; i < 5; i++ {
		wg.Go(bump)
	}
	for i := 0; i < 5; i++ {
		done.Add(1)
		go func() {
			mu.Lock()
			counter += 7
			mu.Unlock()
			done.Done()
		}()
	}
	wg.Wait()
	done.Wait()
	fmt.Println("counter", counter)
}
`)
}
