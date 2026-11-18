//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// This is deliberately the ordinary compiler parser entry point, not a
// catalogue entry. Parse stores ErrorHandler only in its stack-local parser,
// scanner, and source values; the general dependency-source lifetime proof
// admits it while preserving the callback's concrete syntax.Error identity.
//
// The bad token sits inside a function body, not at the top level, so Parse
// recovers and returns a File as well as the first error: all four facts the
// program prints are then non-trivial. Real go1.27.1 prints exactly the same
// four for this input; the earlier top-level "package p\n@" makes Parse return
// a nil *File natively too, which no interpreter change can turn into true.

import "testing"

func TestS281DependencySourceCallbackProof(t *testing.T) {
	const source = `package main
import (
	"fmt"
	"strings"
	"cmd/compile/internal/syntax"
)
func main() {
	count := 0
	originalType := true
	file, first := syntax.Parse(syntax.NewFileBase("bad.go"), strings.NewReader("package p\nfunc f() { @ }"), func(err error) {
		count++
		_, ok := err.(syntax.Error)
		originalType = originalType && ok
	}, nil, 0)
	fmt.Println(file != nil, first != nil, count > 0, originalType)
}
`
	got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/types2")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "true true true true\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}
