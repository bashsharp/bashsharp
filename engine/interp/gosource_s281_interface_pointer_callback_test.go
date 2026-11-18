//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

// A callback receiver snapshot flattens interpreter-owned pointers so the
// dependency can reconcile their pointees. When such a pointer is stored in an
// interface, its dynamic type must remain *funcShape rather than funcShape.
// This is the small shape of cmd/compile/internal/types.Type.extra: any holding
// *Func while fmt invokes the interpreted Type.Format method.
func TestGoSourceS281InterfaceDynamicPointerAcrossFmtCallback(t *testing.T) {
	const source = `package main

import (
	"bytes"
	"fmt"
)

type funcShape struct {
	count int
	next *funcShape
}

type typeShape struct {
	extra any
	alias any
	values []any
	mapping map[string]any
	typedNil any
	snapshot any
}

func (typ *typeShape) Format(state fmt.State, verb rune) {
	extra := typ.extra.(*funcShape)
	alias := typ.alias.(*funcShape)
	indexed := typ.values[0].(*funcShape)
	mapped := typ.mapping["pointer"].(*funcShape)
	typedNil := typ.typedNil.(*funcShape)
	value := typ.snapshot.(funcShape)
	fmt.Fprintf(state, "%d %t %t %t %t %t %d", extra.count, extra == alias, extra == indexed, extra == mapped, extra.next == extra, typedNil == nil, value.count)
	extra.count++
}

func main() {
	pointer := &funcShape{count: 7}
	pointer.next = pointer
	var typedNil *funcShape
	value := funcShape{count: 40}
	typ := &typeShape{}
	// Use field assignments, matching newType's t.extra = new(Func) path.
	typ.extra = pointer
	typ.alias = pointer
	typ.values = []any{pointer}
	typ.mapping = map[string]any{"pointer": pointer}
	typ.typedNil = typedNil
	typ.snapshot = value
	value.count = 99

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%v", typ)
	fmt.Println(buf.String())
	fmt.Println(pointer.count, typ.extra.(*funcShape).count, typ.snapshot.(funcShape).count, value.count, pointer.next == pointer) // typed nil is already checked inside Format.
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "7 true true true true true 40\n8 8 40 99 true\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}
