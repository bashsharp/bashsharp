//go:build full

package gosource

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestSprint281MappedPrivateScalars pins the converter side of the package
// binding used by go/types' TestContextHashCollisions. A mapped package's
// private, zero-initialized token.Pos variable and its typed constants must be
// declared under the same hygienic names that its test-like function reads.
// The native fmt call also distinguishes exact values and types; the grouped
// declarations cover implicit iota, a multi-name spec, and a forward chain.
func TestSprint281MappedPrivateScalars(t *testing.T) {
	core := PackageSpec{Path: "test/core", Sources: []Source{
		src("decl.go", `package core

import "go/token"

var privateVarZero token.Pos

const (
	privateZero token.Pos = iota
	privateOne
	privateSeven token.Pos = 7
	privatePairA, privatePairB token.Pos = 9, 10
	privateForward token.Pos = privateLater
	privateLater token.Pos = 12
	privateSigned int8 = -1
	privateUnsigned uint8 = 255
)
`),
		src("use.go", `package core

import (
	"fmt"
	"go/token"
)

func TestLike() string {
	shadow := ""
	{
		privateZero := token.Pos(21)
		shadow = fmt.Sprintf("%T:%v", privateZero, privateZero)
	}
	return fmt.Sprintf("%T:%v %T:%v %T:%v %T:%v %T:%v %T:%v %T:%v %T:%v %T:%v %T:%v %s",
		privateVarZero, privateVarZero,
		privateZero, privateZero,
		privateOne, privateOne,
		privateSeven, privateSeven,
		privatePairA, privatePairA,
		privatePairB, privatePairB,
		privateForward, privateForward,
		privateLater, privateLater,
		privateSigned, privateSigned,
		privateUnsigned, privateUnsigned,
		shadow)
}
`),
	}}
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./core"
)

func main() { fmt.Println(core.TestLike()) }
`)},
		packages: []PackageSpec{core},
	}

	program, err := Load(m.program, m.options(true))
	if err != nil {
		t.Fatal(err)
	}
	declared := make(map[string]bool)
	referenced := make(map[string]bool)
	syntax.Walk(program.File, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.BashPPDecl:
			declared[node.Name.Value] = true
		case *syntax.BashPPConstSpec:
			declared[node.Name.Value] = true
		case *syntax.BashPPIdent:
			referenced[node.Name.Value] = true
		}
		return true
	})
	for _, sourceName := range []string{
		"privateVarZero", "privateZero", "privateOne", "privateSeven",
		"privatePairA", "privatePairB", "privateForward", "privateLater",
		"privateSigned", "privateUnsigned",
	} {
		name := "__gosource_pkg_0_" + sourceName
		if !declared[name] {
			t.Errorf("mapped private declaration %q is missing", name)
		}
		if !referenced[name] {
			t.Errorf("mapped private reference %q is missing", name)
		}
	}

	const want = "token.Pos:0 token.Pos:0 token.Pos:1 token.Pos:7 token.Pos:9 token.Pos:10 token.Pos:12 token.Pos:12 int8:-1 uint8:255 token.Pos:21\n"
	if got := goRunMapped(t, m); got != want {
		t.Fatalf("go run = %q, want %q", got, want)
	}
	got := runMapped(t, m)
	if got != want {
		t.Fatalf("interpreted = %q, want %q", got, want)
	}
	if strings.Contains(got, "__gosource_pkg_0_") {
		t.Fatal("mapped implementation name leaked into native formatting")
	}
}
