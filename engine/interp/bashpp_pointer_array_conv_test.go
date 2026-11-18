// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

// Sprint: #209; Story: #462; Story-ID: e348d2c13248

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func runGoSourcePointerConv(t *testing.T, source string) (string, string, error) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), "conv.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource rejected the source: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

// Go's (*[N]T)(s) converts a slice to a pointer to its underlying array. The
// pointer aliases the slice's storage, a nil slice converts to a nil pointer,
// and an empty non-nil slice converts to a non-nil *[0]T. The evaluator used
// to refuse the operand as BASHPP-EPOINTER-TARGET because a conversion to a
// pointer type only knew how to retype a pointer operand.
func TestGoSourceSliceToArrayPointerConversion(t *testing.T) {
	out, stderr, err := runGoSourcePointerConv(t, `package main

import "fmt"

var (
	ss  = make([]string, 10)
	s5  = (*[5]string)(ss)
	s10 = (*[10]string)(ss)
)

func main() {
	s := make([]byte, 8, 10)
	for i := range s {
		s[i] = byte(i)
	}
	p := (*[8]byte)(s)
	same := &p[0] == &s[0]
	p[3] = 42
	fmt.Println(same, s[3], len(p))

	s5[1] = "x"
	fmt.Println(ss[1], &ss[0] == &s5[0], &ss[0] == &s10[0])

	var n []byte
	fmt.Println((*[0]byte)(n) == nil)
	z := make([]byte, 0)
	fmt.Println((*[0]byte)(z) == nil)
}
`)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true 42 8\nx true true\ntrue\nfalse\n"))
}

// A slice shorter than the array raises Go's recoverable runtime error with
// Go's exact wording, not a refusal.
func TestGoSourceSliceToArrayPointerConversionTooShort(t *testing.T) {
	out, stderr, err := runGoSourcePointerConv(t, `package main

import "fmt"

func main() {
	defer func() {
		err := recover()
		fmt.Println(err.(error).Error())
	}()
	s := make([]byte, 8)
	_ = (*[9]byte)(s)
	fmt.Println("unreachable")
}
`)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "runtime error: cannot convert slice with length 8 to array or pointer to array with length 9\n"))
}

// Converting a pointer operand to a pointer-to-array type is still the
// retyping conversion; only a slice operand takes the new path.
func TestGoSourceSliceToArrayPointerConversionKeepsPointerRetype(t *testing.T) {
	out, stderr, err := runGoSourcePointerConv(t, `package main

import "fmt"

type A [2]int

func main() {
	a := [2]int{1, 2}
	p := (*A)(&a)
	p[0] = 9
	fmt.Println(a[0], len(p))
}
`)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "9 2\n"))
}

func TestGoSourceSliceToArrayValueConversion(t *testing.T) {
	out, stderr, err := runGoSourcePointerConv(t, `package main

import "fmt"

func main() {
	s := make([]byte, 8)
	for i := range s {
		s[i] = byte(i)
	}
	a := [8]byte(s)
	a[3] = 42
	fmt.Println(a == *(*[8]byte)(s), s[3])
	type Slice []int
	type Int4 [4]int
	ii := make(Slice, 4)
	ii[1] = 7
	b := Int4(ii)
	ii[1] = 8
	var n []byte
	z := make([]byte, 0)
	n0 := [0]byte(n)
	z0 := [0]byte(z)
	fmt.Println(b[1], n0 == z0)
	defer func() { fmt.Println(recover().(error).Error()) }()
	_ = [9]byte(s)
}
`)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "false 3\n7 true\nruntime error: cannot convert slice with length 8 to array or pointer to array with length 9\n"))
}

func TestGoSourceZeroSizePointerIdentity(t *testing.T) {
	out, stderr, err := runGoSourcePointerConv(t, `package main

import "fmt"

func main() {
	x := [10][0]byte{}
	y := make([]struct{}, 10)
	fmt.Println(&x[1] == &x[2], &y[1] == &y[2])
}
`)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true true\n"))
}
