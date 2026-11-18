// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceStructuredValuesMatchGo runs unchanged Go programs twice — once
// through the Go toolchain, once through the interpreter — and requires the
// same output from both. The programs exercise the local structured-value
// surface: grouped struct field declarations, value and pointer receivers,
// addresses of variables and composite literals, defined scalar types and
// their method sets, and interface values holding each of those.
//
// They deliberately print only scalars: printing a struct or an interface
// through fmt goes to the native dependency bridge, which is a separate
// runtime surface with its own gaps, and would make this test report on
// something it is not about.
//
// The oracle builds run one at a time: five concurrent `go build` invocations
// are enough of a load spike to make the timing-sensitive concurrency tests in
// this package flake.
func TestGoSourceStructuredValuesMatchGo(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"grouped_fields_and_value_receiver", `package main

import (
	"fmt"
	"math"
)

type Vertex struct {
	X, Y float64
}

func (v Vertex) Abs() float64 {
	return math.Sqrt(v.X*v.X + v.Y*v.Y)
}

func Abs(v Vertex) float64 {
	return math.Sqrt(v.X*v.X + v.Y*v.Y)
}

func main() {
	v := Vertex{3, 4}
	fmt.Println(v.X, v.Y)
	fmt.Println(v.Abs())
	fmt.Println(Abs(v))
}
`},
		{"pointer_receivers_and_addresses", `package main

import "fmt"

type Vertex struct {
	X, Y int
}

func (v *Vertex) Scale(f int) {
	v.X = v.X * f
	v.Y = v.Y * f
}

func ScaleFunc(v *Vertex, f int) {
	v.X = v.X * f
}

func main() {
	v := Vertex{3, 4}
	v.Scale(2)
	ScaleFunc(&v, 10)
	fmt.Println(v.X, v.Y)

	p := &Vertex{4, 3}
	p.Scale(3)
	ScaleFunc(p, 8)
	fmt.Println(p.X, p.Y)

	q := &v
	q.X = 1e9
	fmt.Println(v.X)

	fmt.Println(Sum(*p))
}

func Sum(v Vertex) int { return v.X + v.Y }
`},
		{"defined_scalar_type_methods", `package main

import "fmt"

type MyFloat float64

func (f MyFloat) Abs() float64 {
	if f < 0 {
		return float64(-f)
	}
	return float64(f)
}

func main() {
	f := MyFloat(-7)
	fmt.Println(f.Abs())
	var g MyFloat = 4
	fmt.Println(g.Abs())
}
`},
		{"interface_values_and_type_switch", `package main

import "fmt"

type I interface {
	Name() string
}

type T struct {
	S string
}

func (t T) Name() string { return t.S }

type P struct {
	S string
}

func (p *P) Name() string { return p.S }

func describe(i interface{}) string {
	switch v := i.(type) {
	case int:
		return fmt.Sprintf("int %d", v*2)
	case string:
		return fmt.Sprintf("string %q", v)
	}
	return "other"
}

func main() {
	var i I = T{"hello"}
	fmt.Println(i.Name())

	i = &P{"pointer"}
	fmt.Println(i.Name())

	var e interface{}
	e = 21
	fmt.Println(describe(e))
	e = "text"
	fmt.Println(describe(e))
	fmt.Println(describe(21), describe("text"), describe(true))

	var s interface{} = "assert"
	fmt.Println(s.(string))
	n, ok := s.(int)
	fmt.Println(n, ok)
}
`},
		{"blank_results_and_returned_storage", `package main

import "fmt"

type Vertex struct {
	X, Y int
}

func newVertex(n int) *Vertex { return &Vertex{n, n * 2} }

func describe(arg int) (string, string) {
	if arg == 42 {
		return "answer", "known"
	}
	return "other", "unknown"
}

func main() {
	p := newVertex(3)
	fmt.Println(p.X, p.Y)
	_, kind := describe(42)
	fmt.Println(kind)
	name, _ := describe(1)
	fmt.Println(name)
}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
			oracle := filepath.Join(dir, "oracle")
			build := exec.Command(goBinary, "build", "-p", "2", "-o", oracle, path)
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("oracle build: %v %s", err, output)
			}
			var wantOut, wantErr bytes.Buffer
			native := exec.Command(oracle)
			native.Dir = dir
			native.Stdout, native.Stderr = &wantOut, &wantErr
			if err := native.Run(); err != nil {
				t.Fatalf("oracle: %v %s", err, wantErr.String())
			}

			program, err := gosource.Parse(strings.NewReader(tc.source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var gotOut, gotErr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &gotOut, &gotErr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := runner.Run(ctx, program.File); err != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, gotOut.String(), gotErr.String())
			}
			if gotOut.String() != wantOut.String() || gotErr.String() != wantErr.String() {
				t.Fatalf("Runner stdout=%q stderr=%q; Go stdout=%q stderr=%q",
					gotOut.String(), gotErr.String(), wantOut.String(), wantErr.String())
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != tc.source {
				t.Fatal("original source changed")
			}
		})
	}
}
