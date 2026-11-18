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

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
func TestGoSourceS249SamePackageAssemblyCompanion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go assembly companions")
	}
	dir := t.TempDir()
	source := `package main
import "fmt"

func AsmAdd(a, b int) int

func main() { fmt.Println(AsmAdd(20, 22)) }
`
	writeFile := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module example.com/asmcompanion\n\ngo 1.27\n")
	writeFile("main.go", source)
	writeFile("asm_amd64.s", `#include "textflag.h"

TEXT ·AsmAdd(SB), NOSPLIT, $0-24
	MOVQ a+0(FP), AX
	ADDQ b+8(FP), AX
	MOVQ AX, ret+16(FP)
	RET
`)
	writeFile("asm_arm64.s", `#include "textflag.h"

TEXT ·AsmAdd(SB), NOSPLIT, $0-24
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	ADD R1, R0, R0
	MOVD R0, ret+16(FP)
	RET
`)

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

func TestGoSourceS249NativeCompanionRefusesWithoutObject(t *testing.T) {
	dir := t.TempDir()
	source := `package main
func MissingObject() int
func main() { _ = MissingObject() }
`
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
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
	if err == nil || !strings.Contains(err.Error()+gotErr.String(), "relocation target main.MissingObject not defined") {
		t.Fatalf("Runner err=%v stderr=%q, want unresolved native companion refusal", err, gotErr.String())
	}
}
