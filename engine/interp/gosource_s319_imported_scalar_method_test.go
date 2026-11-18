//go:build full

package interp_test

import "testing"

// Sprint: #319; Story: #1089; Story-ID: 3185346bc4b5

// TestIssue74181 reaches token.Pos.IsValid through an interpreted environment
// field. The receiver storage belongs to the interpreter, but the defined
// scalar type and its method set belong to the imported package.
func TestS319ImportedDefinedScalarFieldMethod(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/token"
)

type importedPos = token.Pos
type localPos token.Pos
type localInt int

func (localPos) IsValid() string { return "local-pos" }
func (localInt) IsValid() string { return "local-int" }

type environment struct {
	direct token.Pos
	alias importedPos
	local localPos
	integer localInt
}

func main() {
	env := &environment{}
	fmt.Println(env.direct.IsValid(), env.alias.IsValid(), env.local.IsValid(), env.integer.IsValid())
	env.direct, env.alias = 1, 1
	fmt.Println(env.direct.IsValid(), env.alias.IsValid())
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	// The local defined types are wrong-type guards: sharing token.Pos' or
	// int's scalar representation must not borrow token.Pos' method body.
	if got.stdout != "false false local-pos local-int\ntrue true\n" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v", got)
	}
}
