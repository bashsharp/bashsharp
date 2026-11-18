//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// writeGoSourceCompanionDir lays out a one-package module whose Go root stays
// interpreted and whose assembly companions are compiled as themselves.
func writeGoSourceCompanionDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
//
// An assembly companion may reference a package-level Go function whose body
// is interpreted. The worker must link, and the call must reach the original
// body: the interpreted global it writes is observed by the interpreted root.
func TestGoSourceS249AssemblyCallsInterpretedFunc(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go assembly companions")
	}
	source := `package main

import "fmt"

var called int

func target() { called++ }

func jump()

func main() {
	jump()
	jump()
	fmt.Println("called", called)
}
`
	dir := writeGoSourceCompanionDir(t, map[string]string{
		"go.mod":  "module example.com/asmcallback\n\ngo 1.27\n",
		"main.go": source,
		"jump_amd64.s": `#include "textflag.h"
#include "funcdata.h"

DATA ·pointer(SB)/8, $·target(SB)
GLOBL ·pointer(SB), RODATA, $8

TEXT ·jump(SB), NOSPLIT, $8-0
	NO_LOCAL_POINTERS
	CALL *·pointer(SB)
	RET
`,
		"jump_arm64.s": `#include "textflag.h"
#include "funcdata.h"

DATA ·pointer(SB)/8, $·target(SB)
GLOBL ·pointer(SB), RODATA, $8

TEXT ·jump(SB), NOSPLIT, $16-0
	NO_LOCAL_POINTERS
	MOVD ·pointer(SB), R0
	BL (R0)
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
}

// An assembly companion that reads or writes a package-level Go variable is
// refused: the interpreter owns that storage and it cannot be split.
func TestGoSourceS249AssemblyDataSymbolRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go assembly companions")
	}
	source := `package main

import "fmt"

var counter int

func bump()

func main() {
	bump()
	fmt.Println(counter)
}
`
	dir := writeGoSourceCompanionDir(t, map[string]string{
		"go.mod":  "module example.com/asmdata\n\ngo 1.27\n",
		"main.go": source,
		"bump_amd64.s": `#include "textflag.h"

TEXT ·bump(SB), NOSPLIT, $0-0
	MOVQ ·counter(SB), AX
	INCQ AX
	MOVQ AX, ·counter(SB)
	RET
`,
		"bump_arm64.s": `#include "textflag.h"

TEXT ·bump(SB), NOSPLIT, $0-0
	MOVD ·counter(SB), R0
	ADD $1, R0, R0
	MOVD R0, ·counter(SB)
	RET
`,
	})

	path := filepath.Join(dir, "main.go")
	program, err := gosource.Load([]gosource.Source{{Name: path, Data: []byte(source)}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, nil, &gotErr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	text := gotErr.String()
	if err != nil {
		text += err.Error()
	}
	if !strings.Contains(text, "assembly companion references original variable counter") {
		t.Fatalf("Runner err=%v stderr=%q, want a package data companion refusal", err, gotErr.String())
	}
}
