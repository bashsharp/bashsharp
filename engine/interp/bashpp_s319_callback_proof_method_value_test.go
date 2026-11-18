//go:build full

package interp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Sprint 319 story 00446a7bd51a (#1088). The callback proof refused
// interpreted cmd/internal/testdir at expr.Eval(ctxt.match): a callback handed
// to an interface method (go/build/constraint Expr.Eval) whose concrete
// implementations recurse, plus a method value formed from a callback-bearing
// receiver. Two gaps are closed here, both fail-closed:
//
//   - interface dispatch: the bridge observes the authenticated native
//     receiver graph under strict bounds and proves exactly the same-package
//     concrete method bodies found there. Unknown, foreign, or source-invisible
//     implementations refuse; method names alone prove nothing.
//   - method values: a selector that names a method rather than a field builds a
//     method value that captures its whole receiver, so it can reach every
//     callback the receiver reaches. Storing such a value where a callback may
//     not go stays refused instead of being read as a clean function.

// dependencyCallbackConstraintSource is a faithful reduction of
// go/build/constraint: an Expr interface whose And/Or/Not implementations
// recurse over child Expr values and whose Tag leaf finally invokes the
// callback. Match hands the callback to the interface method, exactly the shape
// of testdir's expr.Eval(ctxt.match).
const dependencyCallbackConstraintSource = `package dep

type Expr interface {
	Eval(ok func(tag string) bool) bool
	isExpr()
}

type AndExpr struct{ X, Y Expr }

func (*AndExpr) isExpr() {}

func (e *AndExpr) Eval(ok func(tag string) bool) bool {
	return e.X.Eval(ok) && e.Y.Eval(ok)
}

type OrExpr struct{ X, Y Expr }

func (*OrExpr) isExpr() {}

func (e *OrExpr) Eval(ok func(tag string) bool) bool {
	return e.X.Eval(ok) || e.Y.Eval(ok)
}

type NotExpr struct{ X Expr }

func (*NotExpr) isExpr() {}

func (e *NotExpr) Eval(ok func(tag string) bool) bool {
	return !e.X.Eval(ok)
}

type TagExpr struct{ Tag string }

func (*TagExpr) isExpr() {}

func (e *TagExpr) Eval(ok func(tag string) bool) bool {
	return ok(e.Tag)
}

func Match(x Expr, ok func(tag string) bool) bool {
	return x.Eval(ok)
}
`

// TestS319CallbackProofRefusalEnumeration measures the refusal scope before
// interface dispatch is admitted. The enumerate hook must expose the single
// unresolved dispatch class in this reduction; production remains fail-closed.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofRefusalEnumeration(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "constraint.go", dependencyCallbackConstraintSource, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	enumeration := &dependencyCallbackEnumeration{sites: make(map[string]*dependencyCallbackEnumerationSite)}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.fset = fset
	proof.enumerate = enumeration.record
	proof.prove("Match", []int{1})
	report := enumeration.report("constraint.Match", proof.steps, "")
	t.Log("\n" + report)
	if len(enumeration.sites) != 1 || enumeration.total != 1 {
		t.Fatalf("refusal scope changed: %s", report)
	}
	for _, site := range enumeration.sites {
		if !strings.Contains(site.class, "tainted method") || !strings.Contains(site.class, "target is not source-visible") {
			t.Fatalf("unexpected refusal class %q", site.class)
		}
	}
}

func dependencyCallbackProveMethodSource(t *testing.T, source, receiver, name string, implementations []string, args []int) (bool, string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.fset = fset
	ok := proof.proveMethod(receiver, name, implementations, args)
	return ok, proof.reason
}

// TestS319CallbackProofInterfaceDispatch proves the exact observed concrete
// implementations in the constraint receiver graph. Name-only dispatch is not
// enough: the bridge supplies this bounded set after inspecting the receiver.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofInterfaceDispatch(t *testing.T) {
	ok, reason := dependencyCallbackProveMethodSource(t, dependencyCallbackConstraintSource, "AndExpr", "Eval", []string{"AndExpr", "NotExpr", "OrExpr", "TagExpr"}, []int{0})
	if !ok {
		t.Fatalf("interface-dispatched synchronous callback refused: %s", reason)
	}
}

// TestS319CallbackProofInterfaceDispatchNegatives keeps the dispatch fail-closed:
// each source adds one Expr implementation that leaks the callback, and every
// route must refuse even though the other implementations are clean.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofInterfaceDispatchNegatives(t *testing.T) {
	for _, test := range []struct {
		name   string
		extra  string
		reason string
	}{
		{
			// The implementation stores the callback in a field of the receiver,
			// which is the interface's dynamic value and outlives the call.
			name: "field_retention",
			extra: `type BadExpr struct{ saved func(tag string) bool }

func (e *BadExpr) Eval(ok func(tag string) bool) bool {
	e.saved = ok
	return false
}
`,
			reason: "escaped or unowned",
		},
		{
			// The implementation stores the callback in a package global.
			name: "global_retention",
			extra: `var stash func(tag string) bool

type LeakExpr struct{}

func (e *LeakExpr) Eval(ok func(tag string) bool) bool {
	stash = ok
	return false
}
`,
			reason: "package global",
		},
		{
			// The implementation launches the callback asynchronously.
			name: "async_invocation",
			extra: `type AsyncExpr struct{}

func (e *AsyncExpr) Eval(ok func(tag string) bool) bool {
	go ok("x")
	return false
}
`,
			reason: "callback",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := dependencyCallbackConstraintSource + "\n" + test.extra
			implementations := []string{"AndExpr", "NotExpr", "OrExpr", "TagExpr"}
			switch test.name {
			case "field_retention":
				implementations = append(implementations, "BadExpr")
			case "global_retention":
				implementations = append(implementations, "LeakExpr")
			case "async_invocation":
				implementations = append(implementations, "AsyncExpr")
			}
			ok, reason := dependencyCallbackProveMethodSource(t, source, "AndExpr", "Eval", implementations, []int{0})
			if ok {
				t.Fatal("callback-retaining interface implementation incorrectly admitted")
			}
			if !strings.Contains(reason, test.reason) {
				t.Fatalf("refused for the wrong reason: got %q, want it to mention %q", reason, test.reason)
			}
		})
	}
}

// TestS319CallbackProofMethodValueRetention proves the method-value soundness
// fix: a method value formed from a callback-bearing receiver retains the
// receiver, so storing it in a package global must refuse rather than read the
// method value as a clean function that carries nothing.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofMethodValueRetention(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		reason string
	}{
		{
			// The receiver holds the callback in a field; the method value that
			// captures it is stashed in a package global.
			name: "field_receiver_to_global",
			source: `package dep
type T struct{ f func() }
func (t *T) Run() {}
var saved func()
func Retain(cb func()) {
	t := &T{f: cb}
	saved = t.Run
}`,
			reason: "package global",
		},
		{
			// The method value is returned out of the region, exposing the
			// callback the receiver carries.
			name: "field_receiver_returned",
			source: `package dep
type T struct{ f func() }
func (t *T) Run() {}
func Retain(cb func()) func() {
	t := &T{f: cb}
	return t.Run
}`,
			reason: "return exposes",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []int{0}
			ok, reason := dependencyCallbackProveSource(t, test.source, "Retain", args)
			if ok {
				t.Fatal("method value over callback-bearing receiver incorrectly admitted")
			}
			if !strings.Contains(reason, test.reason) {
				t.Fatalf("refused for the wrong reason: got %q, want it to mention %q", reason, test.reason)
			}
		})
	}
}

// TestS319CallbackProofMethodValueClean keeps the fix from over-refusing: a
// method value whose receiver carries no callback is a clean function, and
// forming one while the callback is only ever passed to a synchronous helper
// must still be admitted.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofMethodValueClean(t *testing.T) {
	source := `package dep
type T struct{ n int }
func (t *T) Run() {}
func sink(f func()) { f() }
func Clean(cb func()) {
	t := &T{n: 1}
	m := t.Run
	m()
	sink(cb)
}`
	ok, reason := dependencyCallbackProveSource(t, source, "Clean", []int{0})
	if !ok {
		t.Fatalf("clean method value refused: %s", reason)
	}
}

// TestS319CallbackProofInterfaceDispatchNativeCollision keeps interface
// dispatch from being fooled by a name collision with a native target. The
// candidate set is built from method names alone, so without further gating a
// callback handed to native code would be admitted whenever the dependency
// source happens to declare a clean method of the same name. Two gates hold:
// a selector whose base names an imported package is a package-level call, not
// interface dispatch; and dispatch only fires for method names declared by a
// source-visible interface type, because only such a name can be an interface
// method call on a source-visible dynamic type.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofInterfaceDispatchNativeCollision(t *testing.T) {
	t.Run("imported_package_qualifier", func(t *testing.T) {
		// sync.OnceValue retains the callback in the closure it returns; the
		// source's own clean OnceValue interface method and implementation must
		// not stand in for it.
		source := `package dep
import "sync"
type Oncer interface{ OnceValue(cb func() string) string }
type T struct{}
func (t *T) OnceValue(cb func() string) string { return cb() }
func Retain(cb func() string) string {
	once := sync.OnceValue(cb)
	return once()
}`
		ok, reason := dependencyCallbackProveSource(t, source, "Retain", []int{0})
		if ok {
			t.Fatal("package-qualified native call incorrectly admitted via same-named source method")
		}
		if !strings.Contains(reason, "tainted method call target is not source-visible") {
			t.Fatalf("refused for the wrong reason: got %q", reason)
		}
	})

	t.Run("no_interface_declares_method", func(t *testing.T) {
		// The receiver is an opaque value whose dynamic type could be native; a
		// concrete source method named Emit is not evidence that the call
		// dispatches to it, because no source interface declares Emit.
		source := `package dep
type T struct{}
func (t *T) Emit(cb func()) { cb() }
func Retain(x any, cb func()) {
	x.Emit(cb)
}`
		ok, reason := dependencyCallbackProveSource(t, source, "Retain", []int{1})
		if ok {
			t.Fatal("non-interface method call incorrectly admitted via same-named source method")
		}
		if !strings.Contains(reason, "tainted method call target is not source-visible") {
			t.Fatalf("refused for the wrong reason: got %q", reason)
		}
	})
}

// TestS319CallbackProofMethodValueCallRetention closes the calling half of the
// method-value gap: invoking a stored method value runs the bound method's
// body with the captured receiver, so a body that leaks the receiver's
// callback must refuse even though the call site itself has clean arguments.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofMethodValueCallRetention(t *testing.T) {
	source := `package dep
type T struct{ f func() }
var saved func()
func (t *T) Run() { saved = t.f }
func Retain(cb func()) {
	t := &T{f: cb}
	m := t.Run
	m()
}`
	ok, reason := dependencyCallbackProveSource(t, source, "Retain", []int{0})
	if ok {
		t.Fatal("calling a method value whose method leaks the receiver's callback incorrectly admitted")
	}
	if !strings.Contains(reason, "package global") {
		t.Fatalf("refused for the wrong reason: got %q", reason)
	}
}

// TestS319CallbackProofMethodValueCallClean keeps the call-time proof precise:
// a method value over a callback-bearing receiver whose method only invokes
// the callback synchronously is admitted, both when called directly and when
// handed through interface dispatch as testdir's expr.Eval(ctxt.match) does.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofMethodValueCallClean(t *testing.T) {
	t.Run("direct_call", func(t *testing.T) {
		source := `package dep
type T struct{ f func() }
func (t *T) Run() { t.f() }
func Retain(cb func()) {
	t := &T{f: cb}
	m := t.Run
	m()
}`
		ok, reason := dependencyCallbackProveSource(t, source, "Retain", []int{0})
		if !ok {
			t.Fatalf("clean method value call refused: %s", reason)
		}
	})

	t.Run("through_interface_dispatch", func(t *testing.T) {
		// The bridge-level constraint test covers the full method-value through
		// observed interface dispatch path. An arbitrary interface parameter has
		// no such provenance and must stay refused here.
		source := dependencyCallbackConstraintSource + `
func MatchCtx(x Expr, cb func(tag string) bool) bool { return x.Eval(cb) }
`
		ok, _ := dependencyCallbackProveSource(t, source, "MatchCtx", []int{1})
		if ok {
			t.Fatal("arbitrary interface receiver incorrectly admitted without observed provenance")
		}
	})
}

// TestS319CallbackProofOnceValuePath covers the other cmd/internal/testdir
// coordinate named by the story: stdlibImportcfg is initialized with
// sync.OnceValue(func() string { ... }). A clean OnceValue thunk must not taint
// the callback proof for the surrounding dependency function, but handing an
// original callback argument to unresolved native OnceValue must stay refused
// because the returned closure can retain it.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319CallbackProofOnceValuePath(t *testing.T) {
	t.Run("clean_unrelated_once_value", func(t *testing.T) {
		source := `package dep
var stdlibImportcfg = sync.OnceValue(func() string {
	return "packagefile runtime=runtime.a"
})
func Match(ok func(tag string) bool) bool {
	_ = stdlibImportcfg()
	return ok("go1.27")
}`
		ok, reason := dependencyCallbackProveSource(t, source, "Match", []int{0})
		if !ok {
			t.Fatalf("clean sync.OnceValue path refused: %s", reason)
		}
	})

	t.Run("callback_to_once_value_refuses", func(t *testing.T) {
		source := `package dep
func Retain(cb func() string) string {
	once := sync.OnceValue(cb)
	return once()
}`
		ok, reason := dependencyCallbackProveSource(t, source, "Retain", []int{0})
		if ok {
			t.Fatal("callback handed to unresolved sync.OnceValue incorrectly admitted")
		}
		if !strings.Contains(reason, "tainted method call target is not source-visible") {
			t.Fatalf("refused for the wrong reason: got %q", reason)
		}
	})
}
