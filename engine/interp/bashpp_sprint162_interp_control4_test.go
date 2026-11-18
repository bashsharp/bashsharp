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

// bashPPSprint162Control4Run interprets one original Go program from the
// interp-control-4 reproducer tree and returns its combined output and the
// run error.
func bashPPSprint162Control4Run(t *testing.T, mechanism, name string) (string, error) {
	t.Helper()
	root := filepath.Join("testdata", "sprint162", "interp-control-4", mechanism)
	source, err := os.ReadFile(filepath.Join(root, name+".go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), program.File)
	return out.String(), err
}

func bashPPSprint162Control4Expected(t *testing.T, mechanism, name string) string {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", "sprint162", "interp-control-4", mechanism, name+".expected"))
	if err != nil {
		t.Fatal(err)
	}
	return string(want)
}

// TestBashPPSprint162StructFieldRace: distinct fields of one shared struct
// written from two tasks is legal Go; the interpreter's struct storage must
// not turn it into a fatal map race. Run with -race to observe.
func TestBashPPSprint162StructFieldRace(t *testing.T) {
	for _, name := range []string{"fields", "select_fanin"} {
		for i := 0; i < 10; i++ {
			out, err := bashPPSprint162Control4Run(t, "struct_field_race", name)
			if err != nil {
				t.Fatalf("%s: run: %v, output=%q", name, err, out)
			}
			if want := bashPPSprint162Control4Expected(t, "struct_field_race", name); out != want {
				t.Fatalf("%s: output %q, want %q", name, out, want)
			}
		}
	}
}

// TestBashPPSprint162RuntimeErrorPanics: Go run-time errors (integer divide
// by zero, a failed one-result type assertion, panic(nil)) are recoverable
// panics whose value satisfies error and runtime.Error and renders the
// runtime's wording; an unrecovered one reports `panic: …` and exits 2.
func TestBashPPSprint162RuntimeErrorPanics(t *testing.T) {
	out, err := bashPPSprint162Control4Run(t, "runtime_error", "recoverable")
	if err == nil {
		t.Fatalf("unrecovered panic did not fail the run; output=%q", out)
	}
	if want := bashPPSprint162Control4Expected(t, "runtime_error", "recoverable"); out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

// TestBashPPSprint162RuntimeErrorClassicNegative: the classic Bash++ surface
// keeps its diagnostic and its non-committing update for the same condition.
func TestBashPPSprint162RuntimeErrorClassicNegative(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "sprint162", "interp-control-4", "runtime_error", "classic_negative.sh"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(string(source)), "classic_negative.sh")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	// Classic semantics: the diagnostic is reported, the update does not
	// commit, and the script carries on — nothing unwinds and no `panic:`
	// report appears.
	err = runner.Run(context.Background(), file)
	if err != nil || !strings.Contains(out.String(), "BASHPP-EEXPR-DIVZERO") || !strings.Contains(out.String(), "unreachable 7") || strings.Contains(out.String(), "panic") {
		t.Fatalf("classic division by zero: err=%v output=%q", err, out.String())
	}
}

// TestBashPPSprint162LocalTypeScope: a type declared inside a function is
// its own type — an assertion, a type switch and an interface comparison
// keep it apart from a same-named type declared elsewhere, and report the
// collision as Go does; the same declaration stays one type across calls
// and nested blocks.
func TestBashPPSprint162LocalTypeScope(t *testing.T) {
	out, err := bashPPSprint162Control4Run(t, "type_scope", "local_types")
	if err != nil {
		t.Fatalf("run: %v, output=%q", err, out)
	}
	if want := bashPPSprint162Control4Expected(t, "type_scope", "local_types"); out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

// TestBashPPSprint162FuncValueInterface: a function value boxed in an
// interface keeps its func type — comparing it panics as Go does for a nil
// func variable, a literal and a declared function alike, a type switch
// sees the signature — while comparable dynamic values compare as before.
func TestBashPPSprint162FuncValueInterface(t *testing.T) {
	out, err := bashPPSprint162Control4Run(t, "func_interface", "uncomparable")
	if err != nil {
		t.Fatalf("run: %v, output=%q", err, out)
	}
	if want := bashPPSprint162Control4Expected(t, "func_interface", "uncomparable"); out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

// TestBashPPSprint162AbortedPanics: recovering a panic discards the older
// panics the recovering frame raised while it was already unwinding, so the
// frame returns normally; an unrecovered replacement propagates as the
// newest value; a function called from a deferred call while an outer
// panic unwinds runs to completion; `return recover()` yields the
// interface value; a `defer recover()` is a no-op in the panicking frame
// and that function's own recover when deferred by a deferred call; and a
// deferred method that survives a nested panic in a callee still recovers
// the outer one; and a deferred call sees only the panic its deferring
// frame is unwinding, so a second recover after the nested one is nil.
// The negative program keeps a nested recover inert and an unrecovered
// deferred panic fatal for its frame.
func TestBashPPSprint162AbortedPanics(t *testing.T) {
	for _, name := range []string{"nested", "helper_during_unwind", "deferred_recover", "frame_owned", "deferred_failure_negative"} {
		out, err := bashPPSprint162Control4Run(t, "aborted_panics", name)
		if err != nil {
			t.Fatalf("%s: run: %v, output=%q", name, err, out)
		}
		if want := bashPPSprint162Control4Expected(t, "aborted_panics", name); out != want {
			t.Fatalf("%s: output:\n%s\nwant:\n%s", name, out, want)
		}
	}
}

// TestBashPPSprint162BlankDeclaration: `var _ = f()` may be spelled any
// number of times in one block and every initializer runs; a real name
// declared twice in one block is still refused.
func TestBashPPSprint162BlankDeclaration(t *testing.T) {
	out, err := bashPPSprint162Control4Run(t, "blank_decl", "blank")
	if err != nil {
		t.Fatalf("run: %v, output=%q", err, out)
	}
	if want := bashPPSprint162Control4Expected(t, "blank_decl", "blank"); out != want {
		t.Fatalf("output %q, want %q", out, want)
	}
	source, err := os.ReadFile(filepath.Join("testdata", "sprint162", "interp-control-4", "blank_decl", "redeclared_negative.sh"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(string(source)), "redeclared_negative.sh")
	if err != nil {
		t.Fatal(err)
	}
	var classic strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &classic, &classic))
	if err != nil {
		t.Fatal(err)
	}
	_ = runner.Run(context.Background(), file)
	if !strings.Contains(classic.String(), "a redeclared in this block") || !strings.Contains(classic.String(), "value 1") {
		t.Fatalf("classic redeclaration: output=%q", classic.String())
	}
}

// TestBashPPSprint162NilFuncCall: calling a nil function value — directly,
// deferred, or as a conversion of nil to a func type — is a recoverable
// run-time error; non-nil function values are called as before.
func TestBashPPSprint162NilFuncCall(t *testing.T) {
	out, err := bashPPSprint162Control4Run(t, "nil_func_call", "nil_call")
	if err != nil {
		t.Fatalf("run: %v, output=%q", err, out)
	}
	if want := bashPPSprint162Control4Expected(t, "nil_func_call", "nil_call"); out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}
