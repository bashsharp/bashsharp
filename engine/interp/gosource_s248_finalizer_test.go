//go:build full

package interp_test

// Sprint: #248; Story: #424; Story-ID: ffabc6c1c44a

import (
	"strings"
	"testing"
)

// TestS248FinalizerAfterLastUse is the source-level lifetime control. The
// object stays reachable through a slice held in an interface, so collections
// while that interface is still used must not finalize it (early control);
// once neither the array nor the interface is named again, a collection must
// deliver the original finalizer with the original pointer (late control).
func TestS248FinalizerAfterLastUse(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"runtime"
)

type T [4]int

func alias(x []*T) (any, []*T) { return x, x }

func collect(done <-chan *T) *T {
	for i := 0; i < 5; i++ {
		runtime.GC()
		select {
		case p := <-done:
			return p
		default:
		}
	}
	return nil
}

func main() {
	s := [3]*T{{42}}
	s[0][3] = 99
	done := make(chan *T, 1)
	runtime.SetFinalizer(s[0], func(p *T) { done <- p })
	h, _ := alias(s[:])
	if collect(done) != nil {
		panic("finalized while reachable")
	}
	fmt.Println(h.([]*T)[0][0])
	p := collect(done)
	if p == nil {
		panic("never finalized")
	}
	fmt.Println(p[0], p[3])
}
`
	out, stderr, err := runGoSource(t, "s248-finalizer-last-use", source)
	if err != nil || stderr != "" || out != "42\n42 99\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// TestS248FinalizerResurrectionOnce: the finalizer resurrects the object into
// a global; it stays intact and usable, and a second loss of reachability does
// not run the finalizer again. SetFinalizer(p, nil) disarms another object.
func TestS248FinalizerResurrectionOnce(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"runtime"
)

type node struct {
	name string
	next *node
}

var saved *node
var runs int

func (n *node) finalize() {
	runs++
	saved = n
}

func arm() {
	n := &node{name: "a", next: &node{name: "b"}}
	runtime.SetFinalizer(n, (*node).finalize)
	m := &node{name: "cleared"}
	runtime.SetFinalizer(m, func(*node) { panic("cleared finalizer ran") })
	runtime.SetFinalizer(m, nil)
}

func main() {
	arm()
	for i := 0; i < 3 && saved == nil; i++ {
		runtime.GC()
	}
	if saved == nil {
		panic("never finalized")
	}
	fmt.Println(saved.name, saved.next.name, runs)
	saved = nil
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	fmt.Println(runs)
}
`
	out, stderr, err := runGoSource(t, "s248-finalizer-resurrect", source)
	if err != nil || stderr != "" || out != "a b 1\n1\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// TestS248FinalizerReachableNeverRuns: an object reachable from a package
// variable, and one captured by its own finalizer, are never finalized.
func TestS248FinalizerReachableNeverRuns(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"runtime"
)

type T struct{ n int }

var keep *T

func main() {
	keep = &T{1}
	runtime.SetFinalizer(keep, func(*T) { panic("reachable object finalized") })
	self := &T{2}
	runtime.SetFinalizer(self, func(p *T) {
		if p != self {
			panic("wrong object")
		}
		panic("self-referencing finalizer ran")
	})
	for i := 0; i < 5; i++ {
		runtime.GC()
	}
	fmt.Println(keep.n)
}
`
	out, stderr, err := runGoSource(t, "s248-finalizer-reachable", source)
	if err != nil || stderr != "" || out != "1\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// Shapes the interpreter's allocation model cannot represent keep the prompt
// refusal: an interior pointer and a finalizer that cannot take the object.
func TestS248FinalizerRefusals(t *testing.T) {
	for name, body := range map[string]string{
		"interior":  `x := new([2]int); runtime.SetFinalizer(&x[1], func(*int) {})`,
		"field":     `type pair struct{ a, b int }; x := &pair{}; runtime.SetFinalizer(&x.b, func(*int) {})`,
		"mismatch":  `x := new(int); runtime.SetFinalizer(x, func(*string) {})`,
		"interface": `var x any = new(int); runtime.SetFinalizer(x, func(*int) {})`,
	} {
		t.Run(name, func(t *testing.T) {
			source := "package main\n\nimport (\n\t\"fmt\"\n\t\"runtime\"\n)\n\nfunc main() {\n\t" + body + "\n\tfmt.Println(\"unreached\")\n}\n"
			got := runGoSourceRunnerError(t, source)
			if !strings.Contains(got, "original callback signature requires value-semantics parameters and supported results") || strings.Contains(got, "unreached") {
				t.Fatalf("unsupported finalizer shape was not refused: %q", got)
			}
		})
	}
}

// Arming a second finalizer on one object is Go's fatal error.
func TestS248FinalizerAlreadySet(t *testing.T) {
	const source = `package main

import "runtime"

func main() {
	x := new(int)
	runtime.SetFinalizer(x, func(*int) {})
	runtime.SetFinalizer(x, func(*int) {})
	println("unreached")
}
`
	out, stderr, err := runGoSource(t, "s248-finalizer-twice", source)
	if err == nil || out != "" || !strings.Contains(stderr, "fatal error: runtime.SetFinalizer: finalizer already set") || strings.Contains(stderr, "unreached") {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
