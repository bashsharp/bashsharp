//go:build full

package interp_test

// Sprint: #319; Story: #1089; Story-ID: 3185346bc4b5

import "testing"

// TestIssue74181 reaches inNode with the zero token.Pos copied through the
// lbrack field of an interpreter-owned indexedExpr. The field must remain a
// typed token.Pos when it becomes a call argument; its shell spelling alone is
// not enough to distinguish that zero from a string.
func TestS319ImportedScalarZeroStructFieldArgument(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/token"
)

type indexedExpr struct {
	lbrack token.Pos
}

func unpack(e *ast.IndexExpr) *indexedExpr {
	return &indexedExpr{lbrack: e.Lbrack}
}

func inNode(pos token.Pos) token.Pos { return pos }

func main() {
	ix := unpack(&ast.IndexExpr{})
	zero := &indexedExpr{}
	fmt.Println(inNode(ix.lbrack), inNode(zero.lbrack))
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil || got.stdout != "0 0\n" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
