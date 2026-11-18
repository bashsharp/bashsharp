//go:build full

package interp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// sprint162NilPtr2Run runs one Go source reproducer of the interp-nilptr-2
// lane through the interpreter and returns its combined output and run error.
func sprint162NilPtr2Run(t *testing.T, mechanism, name string) (string, error) {
	t.Helper()
	root := filepath.Join("testdata", "sprint162", "interp-nilptr-2", mechanism)
	source, err := os.ReadFile(filepath.Join(root, name+".go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("%s: parse: %v", name, err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), program.File)
	return out.String(), err
}

// sprint162NilPtr2Expect runs a reproducer and compares its output with the
// sibling .expected file byte for byte.
func sprint162NilPtr2Expect(t *testing.T, mechanism, name string, wantErr bool) {
	t.Helper()
	got, err := sprint162NilPtr2Run(t, mechanism, name)
	if (err != nil) != wantErr {
		t.Fatalf("%s/%s: run error %v, want error %v; output=%q", mechanism, name, err, wantErr, got)
	}
	want, readErr := os.ReadFile(filepath.Join("testdata", "sprint162", "interp-nilptr-2", mechanism, name+".expected"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got != string(want) {
		t.Fatalf("%s/%s: output\n%s\nwant\n%s", mechanism, name, got, string(want))
	}
}

// sprint162NilPtr2Refused asserts the checker still refuses a .go.src source
// with a diagnostic containing want.
func sprint162NilPtr2Refused(t *testing.T, mechanism, name, want string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "sprint162", "interp-nilptr-2", mechanism, name+".go.src"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gosource.Parse(strings.NewReader(string(source)), name+".go", gosource.Options{RunMain: true}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s/%s: accepted or wrongly refused: %v", mechanism, name, err)
	}
}

// A typed float variable reads back as a float whatever text its cell stores.
// REVERTED in Sprint 162: the mechanism rendered the exact rational constants
// of Bash++ SCRIPTS as decimals (`3/10` → `0.3`) and broke the
// compiled/interpreted parity gates in lower (TestLexicalValuesCompiledArtifact,
// TestProjectedNativeBindings). The GoSource-only form is Sprint 164 work;
// literal.go stays a recorded residue row until then.
func TestBashPPSprint162TypedFloatStorage(t *testing.T) {
	t.Skip("typed float read-back reverted: it changed Classic Bash++ exact-rational rendering; see Sprint 162 closure")
}

// A nil function value is an argument to a func-typed parameter; a call
// through it faults. A closure of the wrong signature stays refused.
func TestBashPPSprint162NilFuncArgument(t *testing.T) {
	sprint162NilPtr2Expect(t, "nilfunc", "nil_func_argument", false)
	sprint162NilPtr2Refused(t, "nilfunc", "nil_func_argument_negative", "cannot use")
}

// A channel compared across its direction types compares by identity; two
// opposite directional types stay refused as mismatched.
func TestBashPPSprint162ChannelDirectionCompare(t *testing.T) {
	sprint162NilPtr2Expect(t, "chandir", "chan_direction_compare", false)
	sprint162NilPtr2Refused(t, "chandir", "chan_direction_compare_negative", "mismatched types")
}

// A type parameter inside a computed callee is bound to the frame's type
// argument; an undeclared type there stays refused.
func TestBashPPSprint162TypeParamInCallee(t *testing.T) {
	sprint162NilPtr2Expect(t, "generic", "new_typeparam_callee", false)
	sprint162NilPtr2Refused(t, "generic", "new_typeparam_callee_negative", "undefined: U")
}

// A promoted method selected through a nil pointer in expression position
// raises the nil-dereference panic rather than an undefined-callable
// diagnostic.
func TestBashPPSprint162PromotedMethodNilReceiver(t *testing.T) {
	sprint162NilPtr2Expect(t, "promoted", "promoted_nil_receiver", false)
}

// `r = recover()` and `_ = recover()` assign the recovered value.
func TestBashPPSprint162RecoverAssign(t *testing.T) {
	sprint162NilPtr2Expect(t, "recoverassign", "recover_assign", false)
}

// runtime.Caller / FuncForPC / Stack and debug.Stack see the interpreted
// frames, including the panicking frames while a deferred call runs for the
// panic, and not after the recovering call has returned.
func TestBashPPSprint162RuntimeStackIntrospection(t *testing.T) {
	sprint162NilPtr2Expect(t, "stack", "runtime_caller", false)
}

// A goroutine started by a deferred call running for a panic runs; without
// a recover the program still dies of the panic afterwards.
func TestBashPPSprint162GoroutineDuringUnwind(t *testing.T) {
	sprint162NilPtr2Expect(t, "unwindgo", "goroutine_during_unwind", false)
	got, err := sprint162NilPtr2Run(t, "unwindgo", "goroutine_during_unwind_negative")
	if err == nil {
		t.Fatalf("unrecovered panic did not terminate: output=%q", got)
	}
	if status, ok := IsExitStatus(err); !ok || status != 2 {
		t.Fatalf("status %v, want 2; output=%q", err, got)
	}
	if !strings.HasPrefix(got, "literal ran\npanic: boom\n") {
		t.Fatalf("output %q", got)
	}
}

// A parameter or result of a named pointer type binds the pointer; a pointer
// to another type stays refused.
func TestBashPPSprint162NamedPointerBinding(t *testing.T) {
	sprint162NilPtr2Expect(t, "namedptr", "named_pointer_binding", false)
	sprint162NilPtr2Refused(t, "namedptr", "named_pointer_binding_negative", "cannot use")
}

// A closure made in an instantiated generic frame carries the instantiated
// signature as its dynamic type, and the init clause and condition of an
// if statement in that frame are bound to the type arguments too.
func TestBashPPSprint162ClosureInstantiatedType(t *testing.T) {
	sprint162NilPtr2Expect(t, "generic", "closure_instantiated_type", false)
}
