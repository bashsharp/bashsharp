package lower_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runGoSourcePointerAssign parses Go source and runs it through the real
// interpreter, returning stdout, stderr and the exit status. It is the oracle
// for the expectations below: every asserted message is one the interpreter in
// this checkout itself prints.
func runGoSourcePointerAssign(t *testing.T, src string) (string, string, int) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(src), "pointer_field.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNil(err))
	var out, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.WithBashCompatErrors(true),
		interp.StdIO(nil, &out, &stderr),
		interp.Dir(t.TempDir()),
		interp.Env(expand.ListEnviron("PATH=/no-tools")),
	)
	qt.Assert(t, qt.IsNil(err))
	status := 0
	if runErr := runner.Run(context.Background(), program.File); runErr != nil {
		var exit interp.ExitStatus
		if !errors.As(runErr, &exit) {
			t.Fatalf("run: %v", runErr)
		}
		status = int(exit)
	}
	return out.String(), stderr.String(), status
}

// TestGoSourcePointerFieldAssignRejectsNilStorage is the negative gate. Writing
// a field through a pointer-typed struct field that is nil has no struct storage
// to land in, so the interpreter must reject it (a nil dereference) rather than
// silently inventing a parent. This is the shape that most resembles the fixed
// case yet is genuinely not assignable struct storage.
func TestGoSourcePointerFieldAssignRejectsNilStorage(t *testing.T) {
	const src = `package main

type element struct {
	next *element
	val  int
}

type List struct {
	head, tail *element
}

func main() {
	lst := List{}
	lst.head.next = &element{val: 11}
	_ = lst
}
`
	stdout, stderr, status := runGoSourcePointerAssign(t, src)
	qt.Assert(t, qt.Equals(stdout, ""))
	qt.Assert(t, qt.Equals(status, 2))
	// Since Sprint 162 a nil pointer on the way to a selector target is Go's
	// runtime panic (recoverable, exit 2), not a static refusal.
	qt.Assert(t, qt.StringContains(stderr, "runtime error: invalid memory address or nil pointer dereference"))
	// The pre-fix miscategorisation must not resurface.
	qt.Assert(t, qt.Not(qt.StringContains(stderr, "assignment parent is not struct storage")))
}

// TestGoSourcePointerFieldChainAssignWritesThroughStorage is the positive gate:
// a write whose parent selector names a pointer-typed struct field must land in
// the struct the pointer refers to. Before the fix this raised
// BASHPP-ESELECTOR-TYPE because the pointer value, not its storage, was treated
// as the assignment parent.
func TestGoSourcePointerFieldChainAssignWritesThroughStorage(t *testing.T) {
	const src = `package main

import "fmt"

type element struct {
	next *element
	val  int
}

type List struct {
	head, tail *element
}

func main() {
	lst := List{}
	lst.head = &element{val: 1}
	lst.head.next = &element{val: 2}
	fmt.Println(lst.head.next.val)
}
`
	stdout, stderr, status := runGoSourcePointerAssign(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(status, 0))
	qt.Assert(t, qt.Equals(stdout, "2\n"))
}

// TestGoSourcePointerFieldAliasAssignSharesStorage pins the value/addressability
// semantics: two pointer-typed fields set to the same element share storage, so
// a write through one is observable through the other. This is the exact shape
// the range-over-iterators fixture exercises in List.Push (tail aliases head,
// then tail.next is written).
func TestGoSourcePointerFieldAliasAssignSharesStorage(t *testing.T) {
	const src = `package main

import "fmt"

type element struct {
	next *element
	val  int
}

type List struct {
	head, tail *element
}

func main() {
	lst := List{}
	lst.head = &element{val: 1}
	lst.tail = lst.head
	lst.tail.next = &element{val: 2}
	fmt.Println(lst.head.next.val)
}
`
	stdout, stderr, status := runGoSourcePointerAssign(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(status, 0))
	qt.Assert(t, qt.Equals(stdout, "2\n"))
}
