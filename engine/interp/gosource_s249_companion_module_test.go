//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
//
// A companion build is decided by the original package directory, and two of
// the things it decides are not the interpreted program's to arrange. The
// generated helper's language version must be its own: a go.mod carrying no go
// directive means go1.16 semantics for every file in that module, which the
// helper does not compile under. And the build must compile none of the
// package's Go files, not merely the one input the runner was handed: a second
// file redeclares the very symbols the helper exists to declare.
func TestGoSourceS249CompanionModuleWithoutGoDirective(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go assembly companions")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("no assembly companion for GOARCH=%s", runtime.GOARCH)
	}
	declSource := `package main

func AsmTriple(v int) int
`
	mainSource := `package main

import "fmt"

func scale(v int) int { return v * 3 }

func main() {
	fmt.Println(AsmTriple(14), scale(2))
}
`
	dir := writeGoSourceCompanionDir(t, map[string]string{
		// No go directive at all, exactly as an upstream run-in-directory
		// module writes it.
		"go.mod":  "module example.com/nogodirective\n",
		"decl.go": declSource,
		"main.go": mainSource,
		"triple_amd64.s": `#include "textflag.h"

TEXT ·AsmTriple(SB), NOSPLIT, $0-16
	MOVQ v+0(FP), AX
	IMULQ $3, AX
	MOVQ AX, ret+8(FP)
	RET
`,
		"triple_arm64.s": `#include "textflag.h"

TEXT ·AsmTriple(SB), NOSPLIT, $0-16
	MOVD v+0(FP), R0
	MOVD $3, R1
	MUL R1, R0, R0
	MOVD R0, ret+8(FP)
	RET
`,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Dir = dir
	var wantOut, wantErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &wantOut, &wantErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run: %v: %s", err, wantErr.String())
	}

	program, err := gosource.Load([]gosource.Source{
		{Name: filepath.Join(dir, "decl.go"), Data: []byte(declSource)},
		{Name: filepath.Join(dir, "main.go"), Data: []byte(mainSource)},
	}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotOut, gotErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &gotOut, &gotErr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v: %s", err, gotErr.String())
	}
	if gotOut.String() != wantOut.String() || gotErr.String() != "" {
		t.Fatalf("Runner stdout=%q stderr=%q; want stdout=%q", gotOut.String(), gotErr.String(), wantOut.String())
	}
	if strings.TrimSpace(wantOut.String()) != "42 6" {
		t.Fatalf("reference stdout=%q, want %q", wantOut.String(), "42 6\n")
	}
}

// framedCompanionFiles lays out a one-package module whose assembly companion
// declares a frame but no locals stack map -- `TEXT ·jump(SB),NOSPLIT,$8` with
// no NO_LOCAL_POINTERS -- and calls a package function through a data symbol,
// which is the shape upstream's own fixedbugs/issue15609.dir has. Natively the
// callee is a leaf, so the runtime never asks that frame for a map. Here the
// callee is interpreted, reached through callback protocol.
func framedCompanionFiles(module, source string) map[string]string {
	return map[string]string{
		"go.mod":  "module " + module + "\n\ngo 1.27\n",
		"main.go": source,
		"jump_amd64.s": `#include "textflag.h"

DATA ·pointer(SB)/8, $·target(SB)
GLOBL ·pointer(SB), RODATA, $8

TEXT ·jump(SB), NOSPLIT, $8-0
	CALL *·pointer(SB)
	RET
`,
		"jump_arm64.s": `#include "textflag.h"

DATA ·pointer(SB)/8, $·target(SB)
GLOBL ·pointer(SB), RODATA, $8

TEXT ·jump(SB), NOSPLIT, $16-0
	MOVD ·pointer(SB), R0
	BL (R0)
	RET
`,
	}
}

// runFramedCompanion runs one such program twice: once by the host toolchain,
// for the reference output, and once with the Go root interpreted and only the
// companion compiled. Both must say the same thing.
func runFramedCompanion(t *testing.T, module, source string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds Go assembly companions")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("no assembly companion for GOARCH=%s", runtime.GOARCH)
	}
	dir := writeGoSourceCompanionDir(t, framedCompanionFiles(module, source))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Dir = dir
	var wantOut, wantErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &wantOut, &wantErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run: %v: %s", err, wantErr.String())
	}

	program, err := gosource.Load([]gosource.Source{{Name: filepath.Join(dir, "main.go"), Data: []byte(source)}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotOut, gotErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &gotOut, &gotErr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v: %s", err, gotErr.String())
	}
	if gotOut.String() != wantOut.String() || gotErr.String() != "" {
		t.Fatalf("Runner stdout=%q stderr=%q; want stdout=%q", gotOut.String(), gotErr.String(), wantOut.String())
	}
	if strings.TrimSpace(wantOut.String()) == "" {
		t.Fatalf("reference produced no output")
	}
}

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
//
// A framed companion with no locals stack map may call an interpreted package
// function. The frame cannot be moved or scanned while it is live, and the
// trampoline that carries the call back is protocol that would do both.
func TestGoSourceS249CompanionFramedAssemblyCallsInterpreted(t *testing.T) {
	runFramedCompanion(t, "example.com/framedcompanion", `package main

import "fmt"

var called int

func target() { called++ }

func jump()

func main() {
	jump()
	jump()
	fmt.Println("called", called)
}
`)
}

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
//
// The same shape under collector pressure: the interpreted callee allocates,
// and allocates again through the bridge, so the helper is building megabytes
// while the frame with no stack map is live and while the call repeats.
func TestGoSourceS249CompanionFramedAssemblyUnderGCPressure(t *testing.T) {
	runFramedCompanion(t, "example.com/framedcompanionpressure", `package main

import (
	"fmt"
	"strings"
)

var total int

func target() {
	local := make([]byte, 1<<16)
	for i := range local {
		local[i] = byte(i)
	}
	total += len(local) + len(strings.Repeat("companion", 4096))
}

func jump()

func main() {
	for i := 0; i < 48; i++ {
		jump()
	}
	fmt.Println("total", total)
}
`)
}
