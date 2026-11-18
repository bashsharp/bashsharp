package interp

import (
	"sort"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func sprint165Closure(t *testing.T, source string) []string {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), "closure.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var out []string
	named, _ := bashPPInstantiationClosure(program.File)
	for wire := range named {
		out = append(out, wire)
	}
	sort.Strings(out)
	return out
}

// The closure registers exactly the concrete instantiations the program
// reaches: through a generic function's result, a generic method body and a
// nested instantiation. A spelling whose arguments are still an enclosing
// declaration's parameters is not an identity and is never registered.
func TestSprint165InstantiationClosure(t *testing.T) {
	got := sprint165Closure(t, `package main

type Box[T any] struct{ V T }
type Wrap[T any] struct{ Inner T }
type Node[T any] struct {
	Val  T
	Next *Node[T]
}

func Make[T any](v T) Box[T]          { return Box[T]{V: v} }
func Wrapped[T any](v T) Wrap[Box[T]] { return Wrap[Box[T]]{Inner: Make(v)} }
func (n Node[T]) Chain(v T) Node[T]   { return Node[T]{Val: v, Next: &n} }

// Unreached: no concrete binding ever names these.
func Unused[T any]() Wrap[Wrap[T]] { return Wrap[Wrap[T]]{} }

func main() {
	_ = Make(1)
	_ = Wrapped("s")
	_ = Node[bool]{}.Chain(true)
}
`)
	want := []string{"Box[int]", "Box[string]", "Node[bool]", "Wrap[Box[string]]"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("closure = %v, want %v", got, want)
	}
	for _, wire := range got {
		if strings.Contains(wire, "[T]") || strings.Contains(wire, "Unused") {
			t.Fatalf("unbound spelling registered: %s", wire)
		}
	}
}

// A program without generic declarations has no closure to compute.
func TestSprint165InstantiationClosureNone(t *testing.T) {
	if got := sprint165Closure(t, "package main\n\ntype T struct{}\n\nfunc main() { _ = T{} }\n"); len(got) != 0 {
		t.Fatalf("closure = %v, want none", got)
	}
}

// The closure also records the concrete instantiations of imported generic
// functions the program reaches — spelled, inferred, or bound through a
// local generic body — keyed by bashPPImportedInstanceKey; a non-generic
// imported call, a local generic call and an instantiation still naming an
// enclosing parameter are not among them.
func TestSprint165ImportedInstantiationClosure(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main

import (
	"maps"
	"slices"
	"strings"
)

type key string

func dedupe[T comparable](in []T) []T {
	return slices.Clip(slices.Compact(in))
}

// Unreached: no concrete binding ever names T here.
func unused[T any](in []T) []T { return slices.Clone(in) }

func main() {
	_ = maps.Clone(map[key]int{})
	_ = slices.Max[[]int]([]int{1})
	_ = dedupe([]string{"a"})
	_ = strings.ToUpper("x")
}
`), "closure.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	_, imported := bashPPInstantiationClosure(program.File)
	var got []string
	for key := range imported {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"maps.Clone[map[key]int, key, int]", "slices.Clip[[]string, string]", "slices.Compact[[]string, string]", "slices.Max[[]int, int]"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("imported closure = %v, want %v", got, want)
	}
}
