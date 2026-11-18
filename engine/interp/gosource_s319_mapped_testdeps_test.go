//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #319; Story: #1084; Story-ID: 7decf01bdd39
//
// The R45 SSA-interpreted round fails before any test body runs: the program
// is cmd/go's generated test main (it imports testing/internal/testdeps) and
// one of its mapped packages ships an assembly companion. The dependency
// bridge worker for that program is compiled from inside the companion
// package's own directory so it may import that internal package. From there
// cmd/go's directory-based internal rule also governs the worker's OTHER
// imports, and testing/internal/testdeps sits under a different tree — so the
// worker build is rejected with "use of internal package
// testing/internal/testdeps not allowed", even though the authenticated
// test-main fact admits it. This exercises that exact shape outside the
// corpus with a tiny mapped assembly companion.
func TestGoSourceS319MappedCompanionTestMainImportsTestdeps(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a mapped-package assembly companion")
	}
	dir := t.TempDir()
	pdir := filepath.Join(dir, "p")
	if err := os.MkdirAll(pdir, 0755); err != nil {
		t.Fatal(err)
	}
	pSource := `package p

func AsmAdd(a, b int) int
`
	files := map[string]string{
		"go.mod": "module example.com/asm\n\ngo 1.24\n",
		"p/add_amd64.s": `#include "textflag.h"

TEXT ·AsmAdd(SB), NOSPLIT, $0-24
	MOVQ a+0(FP), AX
	ADDQ b+8(FP), AX
	MOVQ AX, ret+16(FP)
	RET
`,
		"p/add_arm64.s": `#include "textflag.h"

TEXT ·AsmAdd(SB), NOSPLIT, $0-24
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	ADD R1, R0, R0
	MOVD R0, ret+16(FP)
	RET
`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pPath := filepath.Join(pdir, "p.go")
	if err := os.WriteFile(pPath, []byte(pSource), 0644); err != nil {
		t.Fatal(err)
	}
	companion := filepath.Join(pdir, "add_"+runtime.GOARCH+".s")

	p := gosource.PackageSpec{
		Path:           "example.com/asm/p",
		Sources:        []gosource.Source{{Name: pPath, Data: []byte(pSource)}},
		SourceDir:      pdir,
		CompanionFiles: []string{companion},
	}
	xtestSource := `package asm_test

import (
	"fmt"
	"testing"

	"example.com/asm/p"
)

func TestAsm(t *testing.T) {
	if got := p.AsmAdd(19, 23); got != 42 {
		t.Fatalf("AsmAdd = %d, want 42", got)
	}
	fmt.Println("asm ok")
}
`
	xtest := gosource.PackageSpec{
		Path:    "example.com/asm_test",
		Sources: []gosource.Source{{Name: "asm_test.go", Data: []byte(xtestSource)}},
	}

	identity := "example.com/asm.test"
	driver := gosource.Source{Name: "_testmain.go", Data: []byte(`package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_xtest "example.com/asm_test"
)

var tests = []testing.InternalTest{{"TestAsm", _xtest.TestAsm}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)}

	program, err := gosource.Load([]gosource.Source{driver}, gosource.Options{
		RunMain:    true,
		ImportPath: identity,
		TestMain:   true,
		Packages:   []gosource.PackageSpec{p, xtest},
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.Dir(dir),
		interp.GoSourceModuleDir(dir),
		interp.StdIO(nil, &stdout, &stderr),
		interp.GoSourceIdentity(identity, true),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	var status interp.ExitStatus
	if errors.As(err, &status) {
		err = nil
	}
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, stderr.String())
	}
	if want := "asm ok\nPASS\n"; stdout.String() != want {
		t.Fatalf("Runner stdout=%q stderr=%q, want %q", stdout.String(), stderr.String(), want)
	}
}

// TestGoSourceS319MappedTestingTBReceiver is the focused TestBranchElimIf
// reduction. The SSA helper testConfigArch accepts testing.TB, so its first
// call assigns the mapped test callback's *testing.T receiver to that native
// interface. In reflection T.Error is func(...interface {}), whereas the
// mapped TB method is sourced as Error(...any); the authenticated signatures
// must identify those as the same method.
func TestGoSourceS319MappedTestingTBReceiver(t *testing.T) {
	pkg := gosource.PackageSpec{
		Path: "example.com/receiver_test",
		Sources: []gosource.Source{{Name: "receiver_test.go", Data: []byte(`package receiver_test

import (
	"fmt"
	"testing"
)

func requireTB(tb testing.TB) { tb.Helper() }

func TestReceiver(t *testing.T) {
	requireTB(t)
	fmt.Println("receiver ok")
}
`)}},
	}
	driver := gosource.Source{Name: "_testmain.go", Data: []byte(`package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_receiver "example.com/receiver_test"
)

var tests = []testing.InternalTest{{"TestReceiver", _receiver.TestReceiver}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)}

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{pkg})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	want := s249GoSourceOutcome{stdout: "receiver ok\nPASS\n"}
	if got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}
