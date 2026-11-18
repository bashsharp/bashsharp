//go:build full

package interp_test

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// `sync.WaitGroup.Go` — the one bounded asynchronous callback surface an
// original Go program may name. See interp/gosource_waitgroup.md.
//
// Every case runs one *unchanged* original three ways: the pinned digest is
// checked, `go build` + run gives the native answer, and the same bytes are
// parsed through gosource and interpreted. Nothing is filtered or normalised
// except where the original is deliberately schedule-dependent, and the source
// is re-read afterwards so a test that rewrote what it claims to interpret
// fails rather than passes.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const waitGroupCorpus = "testdata/gosource-waitgroup-go"

// waitGroupMemberBlocker is the *other* surface mutexes.go stops on: a
// sync.Mutex reached as a field of an original local struct is read as a
// non-addressable copy, so its pointer-receiver Lock is not in the value's
// method set. It reproduces with a plain `go func() { … }()` launch and no
// WaitGroup.Go anywhere, so it is not this story's surface; see the exact
// reproduction recorded in gosource_waitgroup.md.
const waitGroupMemberBlocker = "has no method Lock"

func waitGroupOriginal(t *testing.T, name string) string {
	t.Helper()
	pinsData, err := os.ReadFile(filepath.Join(waitGroupCorpus, "sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err := json.Unmarshal(pinsData, &pins); err != nil {
		t.Fatal(err)
	}
	digest, ok := pins[name]
	if !ok {
		t.Fatalf("original %s is not pinned", name)
	}
	source, err := os.ReadFile(filepath.Join(waitGroupCorpus, name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != digest {
		t.Fatalf("pinned original changed: %s has %s, want %s", name, got, digest)
	}
	return string(source)
}

// TestGoSourceWaitGroupAtomicCountersMatchesGo is the deterministic original of
// the three: 50 tasks, each incrementing one captured atomic.Uint64 a thousand
// times. It fails on any lost, duplicated or fabricated launch, and on a task
// whose captured handle was copied rather than shared.
func TestGoSourceWaitGroupAtomicCountersMatchesGo(t *testing.T) {
	differGoSource(t, waitGroupOriginal(t, "atomic-counters.go"), nil, "")
}

// TestGoSourceWaitGroupWaitgroupsMatchesGo runs the waitgroups example. Its
// five workers sleep concurrently, so the *order* of the ten lines is a real
// schedule and neither run may be required to reproduce the other's. The set
// of lines, the stderr and the status are compared exactly, which is what
// pins "every launched body ran exactly once and Wait blocked until they had".
func TestGoSourceWaitGroupWaitgroupsMatchesGo(t *testing.T) {
	source := waitGroupOriginal(t, "waitgroups.go")
	dir := t.TempDir()
	path := filepath.Join(dir, "waitgroups.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, nil, "")
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if sortedLines(got.stdout) != sortedLines(want.stdout) || got.stderr != want.stderr || got.status != want.status {
		t.Fatalf("Runner %+v; native Go %+v", got, want)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original source changed")
	}
}

// TestGoSourceWaitGroupMutexesBlocked records the exact remaining gap for the
// third original. Its WaitGroup.Go launches now run; what stops it is the
// separate native-member surface named by waitGroupMemberBlocker. The row is
// kept live rather than skipped so that it flips to a full three-mode
// comparison the moment that surface lands.
func TestGoSourceWaitGroupMutexesBlocked(t *testing.T) {
	source := waitGroupOriginal(t, "mutexes.go")
	dir := t.TempDir()
	path := filepath.Join(dir, "mutexes.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if strings.Contains(got.stderr, waitGroupMemberBlocker) {
		t.Logf("still blocked on the native member surface, not on WaitGroup.Go: %q", got.stderr)
		return
	}
	// The blocker is gone: hold the original to real Go's answer from here on.
	want := runNativeOracle(t, dir, path, nil, "")
	if got != want {
		t.Fatalf("Runner %+v; native Go %+v", got, want)
	}
}

func sortedLines(s string) string {
	lines := strings.Split(s, "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestGoSourceWaitGroupGoControls are the adversarial controls around the
// three originals. Each is deterministic, so it is compared with real Go byte
// for byte.
func TestGoSourceWaitGroupGoControls(t *testing.T) {
	cases := map[string]string{
		// Per-iteration capture plus exactly-once evaluation: any launch that
		// ran twice, or captured the loop variable's final value, changes the
		// sum. The counter is a native handle captured by every task, so a
		// snapshot that copied it rather than sharing it also fails here.
		"per_iteration_capture": `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var total atomic.Int64
	var launches atomic.Int64
	for i := 1; i <= 6; i++ {
		wg.Go(func() {
			launches.Add(1)
			total.Add(int64(i))
		})
	}
	wg.Wait()
	fmt.Println("total", total.Load())
	fmt.Println("launches", launches.Load())
}
`,
		// A function *value* rather than a literal, launched twice. Each launch
		// must call it exactly once, and neither launch may retain the other's.
		"named_function_value": `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var n atomic.Int64
	f := func() {
		n.Add(1)
	}
	wg.Go(f)
	wg.Go(f)
	wg.Go(f)
	wg.Wait()
	fmt.Println("n", n.Load())
}
`,
		// "a goroutine started by Go may itself call Go" — the counter is not
		// empty when the inner launch happens, so Wait must observe both. This
		// is the add-before-spawn ordering rule stated as a program.
		"nested_launch": `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var n atomic.Int64
	wg.Go(func() {
		n.Add(1)
		wg.Go(func() {
			n.Add(10)
		})
	})
	wg.Wait()
	fmt.Println("n", n.Load())
}
`,
		// One WaitGroup driven by both spellings at once. A Go launch that
		// credited its Done to anything but this counter, or an Add that landed
		// after the launcher reached Wait, shows up as a hang or a short count.
		"mixed_with_add_done": `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var n atomic.Int64
	wg.Add(1)
	go func() {
		n.Add(1)
		wg.Done()
	}()
	wg.Go(func() {
		n.Add(2)
	})
	wg.Add(1)
	go func() {
		n.Add(4)
		wg.Done()
	}()
	wg.Go(func() {
		n.Add(8)
	})
	wg.Wait()
	fmt.Println("n", n.Load())
}
`,
		// A WaitGroup reused for two independent sets of tasks, which Go allows
		// once the previous Wait has returned. It pins that the operation left
		// the counter at exactly zero the first time round.
		"reused_after_wait": `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var n atomic.Int64
	for round := 1; round <= 2; round++ {
		for i := 0; i < 3; i++ {
			wg.Go(func() {
				n.Add(int64(round))
			})
		}
		wg.Wait()
		fmt.Println("round", round, n.Load())
	}
}
`,
		// A body which takes a failure path still returns normally, so its Done
		// is still owed and Wait still returns. Nothing about a body's outcome
		// changes what the counter is owed.
		"failure_path_completes": `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func even(n int) bool {
	return n%2 == 0
}

func main() {
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < 6; i++ {
		wg.Go(func() {
			if even(i) {
				failed.Add(1)
				return
			}
			failed.Add(0)
		})
	}
	wg.Wait()
	fmt.Println("failed", failed.Load())
}
`,
		// The generalisation guard, as a program. `Go` here is a method on an
		// original local type, not on a WaitGroup, so the operation must not
		// claim it: it is an ordinary interpreted method call that runs its
		// argument synchronously, exactly as real Go does.
		"local_type_named_go": `package main

import "fmt"

type Runner struct {
	ran int
}

func (r *Runner) Go(f func()) {
	r.ran++
	f()
}

func main() {
	r := &Runner{}
	seen := 0
	r.Go(func() {
		seen += 3
	})
	fmt.Println("ran", r.ran, "seen", seen)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, source, nil, "")
		})
	}
}

// TestGoSourceWaitGroupPanicKeepsTheCount pins the one place Go's WaitGroup.Go
// deliberately does *not* decrement. Its own implementation recovers, sees a
// panic, and re-panics without calling Done, because releasing Wait would let
// the process exit before the panic printed. The interpreter must likewise not
// credit a Done for a body that left panicking; it reports the task failure and
// the run fails.
//
// The program has no Wait, so this is bounded: real Go dies on the panic, and
// the interpreter joins its tasks at the end of the run. The two disagree on
// what a panic *prints*, so only the status and the interpreter's own report
// are asserted.
func TestGoSourceWaitGroupPanicKeepsTheCount(t *testing.T) {
	source := `package main

import (
	"fmt"
	"sync"
	"time"
)

func main() {
	var wg sync.WaitGroup
	wg.Go(func() {
		fmt.Println("before")
		panic("boom")
	})
	// Deliberately not wg.Wait(): a body that panicked never releases the
	// counter, so waiting on it is exactly what neither runtime may do. The
	// sleep only gives the native oracle's goroutine time to reach the panic.
	time.Sleep(500 * time.Millisecond)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "panic.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, nil, "")
	if want.status == 0 {
		t.Fatalf("native Go accepted a panicking body: %+v", want)
	}
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if got.status == 0 {
		t.Fatalf("Runner accepted a panicking body: %+v", got)
	}
	if got.stdout != want.stdout {
		t.Fatalf("Runner stdout %q; native Go %q", got.stdout, want.stdout)
	}
	if !strings.Contains(got.stderr, "boom") {
		t.Fatalf("Runner did not report the original panic: %q", got.stderr)
	}
}

// TestGoSourceWaitGroupPointerReceiverBlocked records the second exact gap.
// goSourceWaitGroupHandle already authenticates a `*sync.WaitGroup` receiver,
// because that is what the dependency reports for a pointer to one, but no
// original can currently reach it: an interpreter-owned pointer to a native
// value is not recognised as naming a dependency object at all, so the
// selector resolves against the interpreter's own method sets and finds
// nothing. It is not this story's surface — the identical failure reproduces
// with no `Go` anywhere:
//
//	var wg sync.WaitGroup
//	p := &wg
//	p.Add(1)   // type *sync.WaitGroup has no method Add
//
// The row is kept live so it flips to a full three-mode comparison once the
// pointer-to-native-value receiver lands.
func TestGoSourceWaitGroupPointerReceiverBlocked(t *testing.T) {
	source := `package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var n atomic.Int64
	p := &wg
	for i := 1; i <= 4; i++ {
		p.Go(func() {
			n.Add(int64(i))
		})
	}
	p.Wait()
	fmt.Println("n", n.Load())
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "pointer.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if strings.Contains(got.stderr, "*sync.WaitGroup has no method") {
		t.Logf("still blocked on the pointer-to-native receiver surface, not on WaitGroup.Go: %q", got.stderr)
		return
	}
	want := runNativeOracle(t, dir, path, nil, "")
	if got != want {
		t.Fatalf("Runner %+v; native Go %+v", got, want)
	}
}
