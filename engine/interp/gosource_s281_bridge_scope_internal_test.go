package interp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// The dependency bridge used to register a reflect entry for every exported
// symbol of every imported package. A program that imports a package with a
// large exported surface (the compiler internals) reached only a fraction of
// them, yet the generated helper still forced the compiler to build reflect
// data for thousands of unreached symbols in one file, which OOMed the build.
// bashPPReferencedSelectors reports the selectors the program actually reaches
// so bashPPNativeSource can register only those.

func TestGoSourceS281BridgeScopeFiltersUnreferencedSymbols(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go:        goBin,
		Dir:       t.TempDir(),
		Env:       os.Environ(),
		Imports:   map[string]string{"strings": "strings"},
		Selectors: []string{"strings.ToUpper"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, `"strings.ToUpper": reflect.ValueOf(`) {
		t.Fatalf("filtered bridge dropped a referenced symbol:\n%s", source)
	}
	for _, dropped := range []string{`"strings.ToLower":`, `"strings.Split":`, `"strings.NewReader":`} {
		if strings.Contains(source, dropped) {
			t.Fatalf("filtered bridge kept unreferenced symbol %s", dropped)
		}
	}
	buildS270DependencyWorker(t, source, t.TempDir())
}

func TestGoSourceS281BridgeScopeUnfilteredByDefault(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	// No Selectors: the caller could not prove the program's reach, so every
	// export is registered exactly as before this optimization.
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go:      goBin,
		Dir:     t.TempDir(),
		Env:     os.Environ(),
		Imports: map[string]string{"strings": "strings"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{`"strings.ToUpper": reflect.ValueOf(`, `"strings.ToLower": reflect.ValueOf(`, `"strings.Split": reflect.ValueOf(`} {
		if !strings.Contains(source, kept) {
			t.Fatalf("unfiltered bridge dropped %s:\n%s", kept, source)
		}
	}
}

// The collected set must cover both shapes the runner turns into a
// symbols-table key: a call target (BashPPCall, e.g. strings.ToUpper(x)) and a
// package-qualified expression value (BashPPSelectorExpr, e.g. the const
// bytes.MinRead or the function value fmt.Println passed as an argument).
func TestGoSourceS281ReferencedSelectorsCoversCallAndExpressionForms(t *testing.T) {
	src := `package main
import ("bytes";"fmt";"strings")
func apply(f func(...any)(int,error)){ f("x") }
func main(){
	fmt.Println(strings.ToUpper("hi"))
	apply(fmt.Println)
	_ = bytes.MinRead
}
`
	program, err := gosource.Parse(strings.NewReader(src), "prog.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		bashPPGoSource:     true,
		bashPPGoSourceFile: program.File,
		bashPPImports:      map[string]string{"bytes": "bytes", "fmt": "fmt", "strings": "strings"},
	}
	got := map[string]bool{}
	for _, sel := range runner.bashPPReferencedSelectors() {
		got[sel] = true
	}
	for _, want := range []string{"strings.ToUpper", "fmt.Println", "bytes.MinRead"} {
		if !got[want] {
			t.Errorf("referenced selectors missing %q; got %v", want, runner.bashPPReferencedSelectors())
		}
	}
	// A local identifier that is not an imported alias must not be collected.
	if got["apply.x"] || got["f.x"] {
		t.Errorf("collected a non-import selector: %v", runner.bashPPReferencedSelectors())
	}
}

// A nil source file (a caller that cannot prove the program's reach) disables
// filtering so the register-everything default is preserved.
func TestGoSourceS281ReferencedSelectorsNilWithoutProgram(t *testing.T) {
	runner := &Runner{bashPPGoSource: true, bashPPImports: map[string]string{"strings": "strings"}}
	if sel := runner.bashPPReferencedSelectors(); sel != nil {
		t.Fatalf("expected no selectors without a program file, got %v", sel)
	}
}
