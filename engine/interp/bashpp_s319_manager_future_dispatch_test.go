//go:build full

package interp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestS319ManagerFutureInterfaceImplementationRefuses(t *testing.T) {
	source := `package dep
import "example.com/foreign"
type Expr interface { Eval(func() bool) bool }
type Root struct { Next Expr }
func (r *Root) Eval(cb func() bool) bool {
    r.Next = foreign.Make()
    return r.Next.Eval(cb)
}
type Leaf struct{}
func (*Leaf) Eval(cb func() bool) bool { return cb() }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	enumeration := &dependencyCallbackEnumeration{sites: make(map[string]*dependencyCallbackEnumerationSite)}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.fset = fset
	proof.enumerate = enumeration.record
	proof.proveMethod("Root", "Eval", []string{"Root", "Leaf"}, []int{0})
	report := enumeration.report("dep.Root.Eval", proof.steps, proof.reason)
	t.Log("\n" + report)
	if len(enumeration.sites) != 1 || enumeration.total != 1 {
		t.Fatalf("future implementation refusal scope changed: %s", report)
	}

	ok, reason := dependencyCallbackProveMethodSource(t, source, "Root", "Eval", []string{"Root", "Leaf"}, []int{0})
	if ok || !strings.Contains(reason, "target is not source-visible") {
		t.Fatalf("new foreign implementation admitted; ok=%v reason=%q", ok, reason)
	}
}

func TestS319ManagerInterfaceReplacementProvenance(t *testing.T) {
	t.Run("unknown_parameter_refuses", func(t *testing.T) {
		source := `package dep
type Expr interface { Eval(func() bool) bool }
type Root struct { Next Expr }
func (r *Root) Eval(cb func() bool, next Expr) bool {
	r.Next = next
	return r.Next.Eval(cb)
}
type Leaf struct{}
func (*Leaf) Eval(cb func() bool) bool { return cb() }
`
		ok, reason := dependencyCallbackProveMethodSource(t, source, "Root", "Eval", []string{"Root", "Leaf"}, []int{0})
		if ok || !strings.Contains(reason, "target is not source-visible") {
			t.Fatalf("unknown replacement admitted; ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("helper_may_replace_with_future_implementation", func(t *testing.T) {
		source := `package dep
import "example.com/foreign"
type Expr interface { Eval(func() bool) bool }
type Root struct { Next Expr }
func replace(r *Root) { r.Next = foreign.Make() }
func (r *Root) Eval(cb func() bool) bool {
	replace(r)
	return r.Next.Eval(cb)
}
type Leaf struct{}
func (*Leaf) Eval(cb func() bool) bool { return cb() }
`
		ok, reason := dependencyCallbackProveMethodSource(t, source, "Root", "Eval", []string{"Root", "Leaf"}, []int{0})
		if ok || !strings.Contains(reason, "target is not source-visible") {
			t.Fatalf("helper replacement admitted; ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("source_visible_concrete_remains_provable", func(t *testing.T) {
		source := `package dep
type Expr interface { Eval(func() bool) bool }
type Root struct { Next Expr }
func (r *Root) Eval(cb func() bool) bool {
	r.Next = &Leaf{}
	return r.Next.Eval(cb)
}
type Leaf struct{}
func (*Leaf) Eval(cb func() bool) bool { return cb() }
`
		ok, reason := dependencyCallbackProveMethodSource(t, source, "Root", "Eval", []string{"Root", "Leaf"}, []int{0})
		if !ok {
			t.Fatalf("source-visible concrete replacement refused: %s", reason)
		}
	})
}
