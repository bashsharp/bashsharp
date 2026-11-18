//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// A package-map dependency keeps its exact host-selected assembly companion
// and binds the package's original symbol to the flattened declaration. The
// original Go package body remains interpreted: the native build sees only an
// overlaid declaration shim beside the assembly file.
func TestGoSourceS281MappedPackageAssemblyCompanion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a mapped-package assembly companion")
	}
	dir := t.TempDir()
	pdir := filepath.Join(dir, "internal", "p")
	if err := os.MkdirAll(pdir, 0755); err != nil {
		t.Fatal(err)
	}
	mainSource := `package main

import (
	"fmt"
	"example.com/mappedcompanion/internal/p"
)

func main() { fmt.Println(p.AsmAdd(19, 23)) }
`
	pSource := `package p

func AsmAdd(a, b int) int
`
	files := map[string]string{
		"go.mod":          "module example.com/mappedcompanion\n\ngo 1.24\n",
		"main.go":         mainSource,
		"internal/p/p.go": pSource,
		"internal/p/add_amd64.s": `#include "textflag.h"

TEXT ·AsmAdd(SB), NOSPLIT, $0-24
	MOVQ a+0(FP), AX
	ADDQ b+8(FP), AX
	MOVQ AX, ret+16(FP)
	RET
`,
		"internal/p/add_arm64.s": `#include "textflag.h"

TEXT ·AsmAdd(SB), NOSPLIT, $0-24
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	ADD R1, R0, R0
	MOVD R0, ret+16(FP)
	RET
`,
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Dir = dir
	want, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v: %s", err, want)
	}

	mainPath := filepath.Join(dir, "main.go")
	pPath := filepath.Join(pdir, "p.go")
	companion := filepath.Join(pdir, "add_"+runtime.GOARCH+".s")
	program, err := gosource.Load([]gosource.Source{{Name: mainPath, Data: []byte(mainSource)}}, gosource.Options{
		RunMain:    true,
		ImportPath: "example.com/mappedcompanion",
		Packages: []gosource.PackageSpec{{
			Path: "example.com/mappedcompanion/internal/p", Sources: []gosource.Source{{Name: pPath, Data: []byte(pSource)}},
			SourceDir: pdir, CompanionFiles: []string{companion},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := program.File.GoPackageCompanions
	if len(meta) != 1 || len(meta[0].Symbols) != 1 || meta[0].Symbols[0].Name != "AsmAdd" || meta[0].Symbols[0].RuntimeName != "__gosource_pkg_0_AsmAdd" {
		t.Fatalf("mapped companion metadata = %+v", meta)
	}
	var got, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &got, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v: %s", err, stderr.String())
	}
	if got.String() != string(want) || stderr.Len() != 0 {
		t.Fatalf("Runner stdout=%q stderr=%q, want stdout=%q", got.String(), stderr.String(), want)
	}
}
