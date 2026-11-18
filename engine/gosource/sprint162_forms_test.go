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

// TestSprint162AnonymousTypeForms is an outside-corpus reproducer for
// anonymous types in expression position: new with a struct or array literal
// operand wherever a call is lowered (a returned result, an assignment, a
// declaration), and method expressions whose receiver is an interface
// literal, a struct literal or an instantiated generic type. The program's
// output must be `go run`'s, interpreted and lowered.
func TestSprint162AnonymousTypeForms(t *testing.T) {
	path := filepath.Join("testdata", "sprint162", "forms", "anonymous-types", "anonymous_types.go")
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

	// Negatives. The type operand is lowered as a type, never evaluated: a
	// shadowed new is an ordinary call and its operand a value; a method
	// expression on a declared name is spelled as written, not as a closure;
	// the checker's verdict on a mismatched allocation is unchanged.
	load := func(src string) (*gosource.Program, error) {
		return gosource.Load([]gosource.Source{{Name: "negative.go", Data: []byte(src)}}, gosource.Options{RunMain: true})
	}
	shadowed, err := load("package main\n\nfunc new(x int) *int { return &x }\n\nfunc f() *int { return new(3) }\n\nfunc main() { println(*f()) }\n")
	if err != nil {
		t.Fatalf("shadowed new: %v", err)
	}
	syntax.Walk(shadowed.File, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BashPPNewExpr:
			t.Fatalf("shadowed new lowered as an allocation: %v", n)
		case *syntax.BashPPCall:
			if len(n.Fun) == 1 && n.Fun[0].Value == "new" && n.ArgType != nil {
				t.Fatalf("shadowed new's value operand lowered as a type: %v", n.ArgType)
			}
		}
		return true
	})
	named, err := load("package main\n\ntype T struct{}\n\nfunc (T) M() int { return 1 }\n\nfunc main() { f := T.M; g := (*T).M; println(f(T{}) + g(&T{})) }\n")
	if err != nil {
		t.Fatalf("named method expression: %v", err)
	}
	syntax.Walk(named.File, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.BashPPFuncLit); ok {
			t.Fatalf("declared receiver's method expression lowered as a closure: %v", lit)
		}
		return true
	})
	if _, err := load("package main\n\nfunc f() *struct{ a int } { return new(struct{ b int }) }\n\nfunc main() { _ = f() }\n"); err == nil || !strings.Contains(err.Error(), "cannot use new(struct{b int})") {
		t.Fatalf("mismatched allocation verdict changed: %v", err)
	}
}
