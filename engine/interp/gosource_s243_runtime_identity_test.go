//go:build full

package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceRuntimeLinkedFunctionIdentity(t *testing.T) {
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(`package main
import "./a"
func main(){ a.F() }
`)}}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		ImportPath: "test/main",
		Packages: []gosource.PackageSpec{{Path: "test/a", Sources: []gosource.Source{{Name: "a.go", Data: []byte(`package a
type T int
func F() {}
func (T) M() {}
`)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{bashPPGoSourceFile: program.File}
	got := map[string]bool{}
	for _, stmt := range program.File.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok {
			continue
		}
		got[r.goSourceDeclFrameName(decl)] = true
	}
	for _, want := range []string{"test/a.F", "test/a.T.M", "main.main"} {
		if !got[want] {
			t.Errorf("missing original frame identity %q in %v", want, got)
		}
	}
	for name := range got {
		if strings.Contains(name, "__gosource_pkg_") {
			t.Errorf("frame leaked linker hygiene: %q", name)
		}
	}
}

func TestGoSourceRuntimeLinkedInitializerFrame(t *testing.T) {
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte("package main\nimport _ \"./a\"\nfunc main() {}\n")}}, gosource.Options{
		RunMain: true, ImportBase: "test", ImportPath: "test/main",
		Packages: []gosource.PackageSpec{{Path: "test/a", Sources: []gosource.Source{{Name: "a.go", Data: []byte(`package a
import ("fmt"; "runtime")
var X = f()
func f() int {
 pcs := make([]uintptr, 4)
 n := runtime.Callers(1, pcs)
 frames := runtime.CallersFrames(pcs[:n])
 first, more := frames.Next()
 second, _ := frames.Next()
 fmt.Println(first.Function, first.Line, more, second.Function, second.Line)
 return 1
}
`)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), program.File); err != nil {
		t.Fatalf("run: %v; output=%s", err, out.String())
	}
	if got, want := out.String(), "test/a.f 6 true test/a.init 3\n"; got != want {
		t.Fatalf("initializer frames = %q, want %q", got, want)
	}
}

func TestGoSourceRuntimeMainHygienicLookingNameIsNotRewritten(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader("package main\nfunc __gosource_pkg_0_F() {}\nfunc main() {}\n"), "main.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{bashPPGoSourceFile: program.File}
	for _, stmt := range program.File.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if ok && decl.Name.Value == "__gosource_pkg_0_F" && r.goSourceDeclFrameName(decl) != "main.__gosource_pkg_0_F" {
			t.Fatalf("main declaration identity was rewritten: %q", r.goSourceDeclFrameName(decl))
		}
	}
}
