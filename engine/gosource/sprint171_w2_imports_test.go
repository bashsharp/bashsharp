//go:build full

package gosource_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// sprint171RunBothModes loads path as an executing program and requires the
// interpreter's and the lowered program's stdout to be `go run`'s.
func sprint171RunBothModes(t *testing.T, path string) *gosource.Program {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	goOut, err := exec.Command("go", "run", path).Output()
	if err != nil {
		t.Fatalf("go run: %v", err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: filepath.Base(path), Data: data}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil || stderr.Len() > 0 {
		t.Fatalf("interpreter: err=%v stderr=%q", err, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), goOut) {
		t.Fatalf("stdout differs from go run\ninterpreter: %q\ngo run:      %q", stdout.Bytes(), goOut)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: filepath.Base(path)})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	generated := filepath.Join(t.TempDir(), "generated.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	loweredOut, err := exec.Command("go", "run", generated).Output()
	if err != nil {
		t.Fatalf("go run lowered program: %v\n%s", err, result.Source)
	}
	if !bytes.Equal(loweredOut, goOut) {
		t.Fatalf("lowered stdout differs from go run\nlowered: %q\ngo run:  %q", loweredOut, goOut)
	}
	return program
}

// TestSprint171TypeSwitchInit is an outside-corpus reproducer for a type
// switch with an init statement (`switch i := f(); v := i.(type)`), which
// the converter refused as an unsupported initializer. Go scopes the init to
// the switch, so it converts to a block holding the init and the switch —
// the shape lowering already gives an expression switch with an init. The
// program covers the scope (an outer name the init shadows is intact after
// the switch), a labeled break through the switch, a goto that re-enters
// the labeled switch so the init runs again, and an assignment init.
func TestSprint171TypeSwitchInit(t *testing.T) {
	program := sprint171RunBothModes(t, filepath.Join("testdata", "sprint171", "w2-imports", "typeswitch-init", "typeswitch_init.go"))

	// Tree shape: every type switch with an init is the last statement of a
	// block; the switch is labeled inside the block when only a break names
	// the label, and the block carries the label when a goto does.
	var labeled []string
	syntax.Walk(program.File, func(n syntax.Node) bool {
		l, ok := n.(*syntax.BashPPLabeled)
		if !ok || l.Stmt == nil {
			return true
		}
		switch cmd := l.Stmt.Cmd.(type) {
		case *syntax.BashPPSwitch:
			if cmd.TypeSwitch {
				labeled = append(labeled, l.Label.Value+":switch")
			}
		case *syntax.Block:
			if sw, ok := cmd.Stmts[len(cmd.Stmts)-1].Cmd.(*syntax.BashPPSwitch); ok && sw.TypeSwitch {
				labeled = append(labeled, l.Label.Value+":block")
			}
		}
		return true
	})
	if got, want := strings.Join(labeled, " "), "outer:switch again:block"; got != want {
		t.Fatalf("labeled type switches: got %q, want %q", got, want)
	}

	// Negatives: the checker's verdicts on the init are unchanged (a
	// declared-and-unused init name, a non-interface guard operand), and a
	// type switch without an init is still the bare switch statement.
	load := func(src string) (*gosource.Program, error) {
		return gosource.Load([]gosource.Source{{Name: "negative.go", Data: []byte(src)}}, gosource.Options{RunMain: true})
	}
	if _, err := load("package main\n\nfunc main() {\n\tvar x any = 1\n\tswitch y := 2; x.(type) {\n\tcase int:\n\t}\n}\n"); err == nil || !strings.Contains(err.Error(), "declared and not used: y") {
		t.Fatalf("unused init verdict changed: %v", err)
	}
	if _, err := load("package main\n\nfunc main() {\n\tswitch x := 2; x.(type) {\n\tcase int:\n\t}\n}\n"); err == nil || !strings.Contains(err.Error(), "not an interface") {
		t.Fatalf("non-interface guard verdict changed: %v", err)
	}
	bare, err := load("package main\n\nfunc main() {\n\tvar x any = 1\n\tswitch x.(type) {\n\tcase int:\n\t}\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	syntax.Walk(bare.File, func(n syntax.Node) bool {
		if block, ok := n.(*syntax.Block); ok && len(block.Stmts) == 1 {
			if sw, ok := block.Stmts[0].Cmd.(*syntax.BashPPSwitch); ok && sw.TypeSwitch {
				t.Fatalf("a type switch without an init gained a scoping block")
			}
		}
		return true
	})
}

// TestSprint171ImportedGenericFunctions is an outside-corpus reproducer for
// calls of imported generic functions (maps.Clone, unique.Make, slices.Max),
// which the dependency helper could not register by name: a generic
// function is a value only once instantiated. The front end records each
// call's type arguments, spelled or inferred; the runtime's instantiation
// closure substitutes an enclosing generic body's bindings into them, so
// every concrete instantiation the program reaches is registered under its
// instantiated spelling and the call names it beside the selector.
func TestSprint171ImportedGenericFunctions(t *testing.T) {
	program := sprint171RunBothModes(t, filepath.Join("testdata", "sprint171", "w2-imports", "imported-generics", "imported_generics.go"))

	// Every qualified call of a generic function carries its type arguments
	// in the tree, the inferred ones included; a non-generic call carries
	// none.
	calls := map[string]int{}
	syntax.Walk(program.File, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.BashPPCall); ok && len(call.Fun) == 2 {
			calls[call.Fun[0].Value+"."+call.Fun[1].Value] = len(call.TypeArgs)
		}
		return true
	})
	for name, want := range map[string]int{"maps.Clone": 3, "slices.Max": 2, "slices.Index": 2, "maps.Keys": 3, "slices.Sorted": 1, "slices.Clip": 2, "unique.Make": 1, "cmp.Or": 1, "fmt.Println": 0} {
		if got, ok := calls[name]; !ok || got != want {
			t.Errorf("%s: %d type arguments (present=%v), want %d", name, got, ok, want)
		}
	}
}

// TestSprint171ImportedMethodExpressions is an outside-corpus reproducer for
// method expressions on an imported package's named types
// (reflect.Type.Method, (*bytes.Buffer).WriteString): the program has no
// declaration to select the method from, so — like a type parameter's or a
// type literal's method expression — it lowers to the closure calling the
// method on its first argument, as a value and as a direct call.
func TestSprint171ImportedMethodExpressions(t *testing.T) {
	program := sprint171RunBothModes(t, filepath.Join("testdata", "sprint171", "w2-imports", "imported-method-expr", "imported_method_expr.go"))
	closures := 0
	syntax.Walk(program.File, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.BashPPFuncLit); ok {
			closures++
		}
		return true
	})
	if closures != 5 {
		t.Fatalf("imported method expressions lowered as %d closures, want 5", closures)
	}

	// Negatives: a declared type's method expression is spelled as written,
	// and a method VALUE on an imported value stays the selector it is.
	load := func(src string) (*gosource.Program, error) {
		return gosource.Load([]gosource.Source{{Name: "negative.go", Data: []byte(src)}}, gosource.Options{RunMain: true})
	}
	local, err := load("package main\n\nimport \"bytes\"\n\ntype T struct{}\n\nfunc (T) M() int { return 1 }\n\nfunc main() {\n\tf := T.M\n\tvar b bytes.Buffer\n\tw := b.WriteString\n\tw(\"x\")\n\tprintln(f(T{}), b.Len())\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	syntax.Walk(local.File, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BashPPFuncLit:
			t.Fatalf("a declared type's method expression or an imported method value lowered as a closure: %v", n)
		case *syntax.BashPPSelectorExpr:
			if n.Sel.Value == "WriteString" && !n.MethodValue {
				t.Fatalf("imported method value lost its selector form")
			}
		}
		return true
	})
}

// TestSprint171UnsafeOperators is an outside-corpus reproducer for
// unsafe.Sizeof, Alignof and Offsetof in statement positions that record a
// call to dispatch — a short declaration, an assignment, a return, a tuple —
// where the runtime sent the operator to the dependency helper, which has
// no callable symbol for it. The operators are constant expressions (or,
// over a type parameter, expressions the instantiated frame settles), so
// the converter records them as expressions in every position, the way a
// conversion is, and the evaluator folds them from the operand's type.
func TestSprint171UnsafeOperators(t *testing.T) {
	program := sprint171RunBothModes(t, filepath.Join("testdata", "sprint171", "w2-imports", "unsafe-operators", "unsafe_operators.go"))

	// Tree shape: no statement carries an unsafe operator as its call; each
	// position holds it as an expression. The import is still spelled, so
	// the lowered program keeps its use of unsafe.
	unsafeCall := func(call *syntax.BashPPCall) bool {
		return call != nil && len(call.Fun) == 2 && call.Fun[0].Value == "unsafe"
	}
	operators := 0
	syntax.Walk(program.File, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BashPPShortDecl:
			if unsafeCall(n.Call) {
				t.Errorf("short declaration dispatches %s as a call", n.Call.Fun[1].Value)
			}
		case *syntax.BashPPAssign:
			if unsafeCall(n.Call) {
				t.Errorf("assignment dispatches %s as a call", n.Call.Fun[1].Value)
			}
		case *syntax.BashPPReturn:
			if unsafeCall(n.Call) {
				t.Errorf("return dispatches %s as a call", n.Call.Fun[1].Value)
			}
		case *syntax.BashPPCall:
			if unsafeCall(n) {
				operators++
			}
		}
		return true
	})
	if operators == 0 {
		t.Errorf("no unsafe operator kept its spelling in the tree")
	}

	// Negatives: a call of an imported function, a declared function or a
	// conversion in the same positions keeps its form — the imported call
	// and the declared call are dispatched, the conversion is an expression.
	negative, err := gosource.Load([]gosource.Source{{Name: "negative.go", Data: []byte(`package main

import "strings"

func id(s string) string { return s }

func upper() string {
	return strings.ToUpper("a")
}

func main() {
	x := strings.ToUpper("b")
	x = id(x)
	y := int64(3)
	_, _ = x, y
	println(upper())
}
`)}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	syntax.Walk(negative.File, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BashPPShortDecl:
			if n.Call != nil {
				calls = append(calls, "decl:"+n.Call.Fun[len(n.Call.Fun)-1].Value)
			} else if _, ok := n.Expr.(*syntax.BashPPConvertExpr); ok {
				calls = append(calls, "decl:conversion")
			}
		case *syntax.BashPPAssign:
			if n.Call != nil {
				calls = append(calls, "assign:"+n.Call.Fun[len(n.Call.Fun)-1].Value)
			}
		case *syntax.BashPPReturn:
			if n.Call != nil {
				calls = append(calls, "return:"+n.Call.Fun[len(n.Call.Fun)-1].Value)
			}
		}
		return true
	})
	if got, want := strings.Join(calls, " "), "return:ToUpper decl:ToUpper assign:id decl:conversion"; got != want {
		t.Fatalf("negative statement forms: got %q, want %q", got, want)
	}
}

// TestSprint171UnsafeString is an outside-corpus reproducer for
// unsafe.String, which the runtime sent to the dependency helper as an
// imported function: the helper has no such symbol, and could not read the
// interpreter-owned byte storage the pointer names in any case. The call is
// answered by the interpreter from that storage, with the run-time faults
// Go raises for a nil pointer with a length and a negative length; a local
// type's own String method is the control.
func TestSprint171UnsafeString(t *testing.T) {
	sprint171RunBothModes(t, filepath.Join("testdata", "sprint171", "w2-imports", "unsafe-string", "unsafe_string.go"))
}

// TestSprint171ConstantInInterface is an outside-corpus reproducer for an
// untyped constant's NAME stored in an interface (`const a = 0;
// (interface{})(a)`), which the runtime refused as a value with no dynamic
// type where the literal `0` was boxed as an int: the constant cell carries
// no type, and its default type comes from its value's kind, as the
// literal's does. Typed constants keep their declared type. Salvaged from
// the Sprint 165 lane-203 tree (request R1 of the w2-imports FINDINGS).
func TestSprint171ConstantInInterface(t *testing.T) {
	sprint171RunBothModes(t, filepath.Join("testdata", "sprint171", "w2-imports", "const-interface", "const_interface.go"))
}
