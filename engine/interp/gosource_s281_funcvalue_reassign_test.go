package interp_test

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceS281FuncValueReassignFromCollection models cmd/compile's main:
// a package-level map[string]func(*ssagen.ArchInfo) whose selected value is
// reassigned to an already-declared func-typed variable before being handed to
// gc.Main. A func value read from a collection travels as a bare closure handle
// with no declared type of its own, so reassigning it to a func-typed variable
// must recover the func identity instead of reading the handle as an untyped
// scalar. Otherwise the assignment is refused as
//
//	BASHPP-EASSIGN-TYPE: untyped result is not assignable to func(*ssagen.ArchInfo)
//
// which crashes every interpreted `go tool compile` startup.
func TestGoSourceS281FuncValueReassignFromCollection(t *testing.T) {
	packages := []gosource.PackageSpec{
		{Path: "test/ssagen", Sources: []gosource.Source{{Name: "s.go", Data: []byte(`package ssagen

type ArchInfo struct {
	Name string
	Gen  func(int) int
}

var Arch ArchInfo
`)}}},
		{Path: "test/amd64", Sources: []gosource.Source{{Name: "a.go", Data: []byte(`package amd64

import "test/ssagen"

func Init(a *ssagen.ArchInfo) {
	a.Name = "amd64"
	a.Gen = func(x int) int { return x * 2 }
}
`)}}},
		{Path: "test/gc", Sources: []gosource.Source{{Name: "g.go", Data: []byte(`package gc

import (
	"fmt"
	"test/ssagen"
)

func Main(archInit func(*ssagen.ArchInfo)) {
	archInit(&ssagen.Arch)
	fmt.Println("compiled for", ssagen.Arch.Name, ssagen.Arch.Gen(21))
}
`)}}},
	}

	// The failing shape is the pre-declared func variable reassigned from a map
	// read; the direct-argument and comma-ok forms already worked, so keep them
	// alongside as guards against a future narrowing of the fix.
	for name, body := range map[string]string{
		"reassign_declared_variable": `	var archInit func(*ssagen.ArchInfo)
	archInit = archInits["amd64"]
	gc.Main(archInit)`,
		"comma_ok_short_declaration": `	archInit, ok := archInits["amd64"]
	if !ok {
		return
	}
	gc.Main(archInit)`,
		"direct_argument": `	gc.Main(archInits["amd64"])`,
	} {
		t.Run(name, func(t *testing.T) {
			main := `package main

import (
	"test/amd64"
	"test/gc"
	"test/ssagen"
)

var archInits = map[string]func(*ssagen.ArchInfo){
	"amd64": amd64.Init,
}

func main() {
` + body + `
}
`
			program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(main)}}, gosource.Options{
				RunMain:    true,
				ImportBase: "test",
				ImportPath: "test/main",
				Packages:   packages,
			})
			if err != nil {
				t.Fatalf("gosource.Load: %v", err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := runner.Run(ctx, program.File); err != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if got, want := stdout.String(), "compiled for amd64 42\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if strings.Contains(stderr.String(), "func(") {
				t.Fatalf("function type leaked to stderr: %q", stderr.String())
			}
		})
	}
}
