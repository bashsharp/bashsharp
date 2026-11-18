//go:build full

package interp_test

// Sprint: #194; Story: #123; Story-ID: 4e26e484961f
//
// Outside-corpus regressions for the general callback lifecycle and policy.
// None of these use a pinned Go-by-Example / Tour fixture: each is a freshly
// authored original program whose behaviour is measured against a real Go build
// of the same bytes (differGoSource) or, for a refusal, against the interpreter
// alone (runGoSourceRunnerError). They pin invariants that hold regardless of
// which callable is exercised, so a lifecycle or policy regression is caught
// without special-casing any fixture identity.

import (
	"strings"
	"testing"
)

// TestGoSourceCallbackReuseAfterRecoveredPanic pins that a callback whose panic
// fmt recovers leaves the runner clean enough to run the SAME and later
// callbacks again. The existing fmt_panic case runs one recovered panic and
// stops; this one recovers two, interleaved with successful reuses of the very
// type that panicked, so a teardown that corrupted the borrowed caller state
// (saved result/call cells, panic or exit bits) would diverge from native Go.
func TestGoSourceCallbackReuseAfterRecoveredPanic(t *testing.T) {
	const source = `package main
import "fmt"
type Tag int
func (t Tag) String() string {
	if t == 0 {
		panic("boom")
	}
	return fmt.Sprintf("tag(%d)", int(t))
}
func main() {
	fmt.Println(Tag(0))
	fmt.Println(Tag(3))
	fmt.Println(Tag(0))
	fmt.Println(Tag(7))
	fmt.Println("after")
}
`
	differGoSource(t, source, nil, "")
}

// TestGoSourceCallbackInterleavedReceiverLifetimes pins that repeatedly entering
// and leaving callbacks of different receiver kinds preserves each one's own
// lifetime: a pointer receiver keeps mutating one shared original value across
// every call, while a value receiver is a fresh copy each time. Getting the
// per-callback save/restore of the caller's in-flight argument state wrong would
// leak one call's cells into the next and drift from native Go.
func TestGoSourceCallbackInterleavedReceiverLifetimes(t *testing.T) {
	const source = `package main
import "fmt"
type Counter struct{ n int }
func (c *Counter) String() string { c.n++; return fmt.Sprintf("c%d", c.n) }
type Label int
func (l Label) String() string { return fmt.Sprintf("L%d", int(l)) }
func main() {
	c := &Counter{}
	for i := 0; i < 3; i++ {
		fmt.Println(c, Label(i))
	}
	fmt.Println(c.n)
}
`
	differGoSource(t, source, nil, "")
}

// TestGoSourceCallbackValueReceiverMutation pins the copy boundary used by the
// callback fast path: changing a scalar field of a value receiver is visible
// inside that invocation but never changes the caller's value or a later call.
func TestGoSourceCallbackValueReceiverMutation(t *testing.T) {
	const source = `package main
import "fmt"
type Counter struct{ n int }
func (c Counter) String() string { c.n++; return fmt.Sprintf("c%d", c.n) }
func main() {
	c := Counter{n: 4}
	fmt.Println(c, c, c.n)
}
`
	differGoSource(t, source, nil, "")
}

// TestGoSourceCallbackAsyncRetainedRefused pins the retained-callback policy at
// a second, unrelated door: time.AfterFunc keeps an original function and calls
// it later from a timer goroutine. That asynchronous retention is not one of the
// reviewed synchronous (e.g. filepath.WalkDir) or net/http registration shapes,
// so it must be refused before the dependency can keep the function — never run
// out of band. This is the general pin that retention outside the reviewed
// shapes is refused, not that any single API is special-cased.
func TestGoSourceCallbackAsyncRetainedRefused(t *testing.T) {
	const source = `package main
import (
	"fmt"
	"time"
)
func main() {
	time.AfterFunc(time.Millisecond, func() { fmt.Println("fired") })
	fmt.Println("main")
	time.Sleep(10 * time.Millisecond)
}
`
	got := runGoSourceRunnerError(t, source)
	if !strings.Contains(got, "callback") {
		t.Fatalf("an asynchronous retained callback was admitted: %q", got)
	}
	if strings.Contains(got, "fired") {
		t.Fatalf("the refused callback still ran: %q", got)
	}
}
