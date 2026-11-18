// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

// Sprint: #209; Story: #463; Story-ID: a6f104b906d9
//
// A non-interface operand compared with an interface holding the same value
// must box with its own dynamic type, not as a bare scalar. convT2X.go boxes
// named arrays, structs and pointers into interface{} and then compares the
// concrete value back against the boxed one; before this repair a composite or
// pointer operand was mis-boxed as a scalar, so the dynamic types never matched
// and every array/struct/pointer equality reported inequality.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPStory463CompositeInterfaceEquality(t *testing.T) {
	t.Parallel()
	const source = `package main

type Arr [2]uint64
type Pt struct {
	X int
	Y int
}

func main() {
	a := Arr{4, 5}
	var ia interface{} = a
	if a != ia {
		panic("a != ia")
	}
	if ia != a {
		panic("ia != a")
	}

	s := Pt{1, 2}
	var is interface{} = s
	if s != is {
		panic("s != is")
	}

	p := &s
	var ip interface{} = p
	if p != ip {
		panic("p != ip")
	}

	// A different dynamic type is never equal even with the same shape.
	var ib interface{} = Arr{9, 9}
	if a == ib {
		panic("a == ib")
	}

	println("ok")
}
`
	program, err := gosource.Parse(strings.NewReader(source), "story463.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var out, errOut strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()),
		interp.StdIO(strings.NewReader(""), &out, &errOut))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	if got := errOut.String(); got != "ok\n" {
		t.Fatalf("stderr = %q, want %q; stdout=%q", got, "ok\n", out.String())
	}
}
