//go:build full

package interp_test

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// Original locally declared types crossing into a dependency. Every case runs
// the unchanged original source and compares stdout, stderr and exit status
// against a real Go build of the same file, so nothing here asserts an
// interpreter-only expectation. differGoSource lives in
// bashpp_native_structured_test.go.
import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceLocalTypeValues covers the original type shapes a Tour program
// hands to fmt: a struct value, a struct literal, a struct pointer, a defined
// scalar and array type, a slice/map of structs, an empty interface and a
// declared interface value. %T and %v observables are compared, not just the
// default verb.
func TestGoSourceLocalTypeValues(t *testing.T) {
	cases := map[string]string{
		"struct_value_and_literal": `package main

import "fmt"

type Vertex struct {
	X int
	Y int
}

func main() {
	v := Vertex{1, 2}
	fmt.Println(v)
	fmt.Println(Vertex{3, 4})
	fmt.Printf("%v %T\n", v, v)
}
`,
		"struct_pointer": `package main

import "fmt"

type Vertex struct {
	X int
	Y int
}

func main() {
	v := Vertex{1, 2}
	p := &v
	p.X = 9
	fmt.Println(*p)
	fmt.Println(p)
	fmt.Printf("%v %T\n", p, p)
}
`,
		"defined_scalar_and_array": `package main

import "fmt"

type Celsius float64
type IPAddr [4]byte

func main() {
	var c Celsius = 36.6
	fmt.Println(c)
	fmt.Printf("%v %T\n", c, c)
	ip := IPAddr{127, 0, 0, 1}
	fmt.Println(ip)
	fmt.Printf("%v %T\n", ip, ip)
}
`,
		"slice_and_map_of_structs": `package main

import "fmt"

type Vertex struct {
	X, Y float64
}

func main() {
	pts := []Vertex{{1, 2}, {3, 4}}
	fmt.Println(pts)
	m := map[string]Vertex{"a": {1, 2}}
	fmt.Println(m)
	fmt.Printf("%T %T\n", pts, m)
}
`,
		"empty_interface_and_interface_value": `package main

import "fmt"

type I interface {
	M() string
}

type T struct {
	S string
}

func (t T) M() string { return "T:" + t.S }

func main() {
	var e interface{}
	fmt.Println(e)
	e = T{"boxed"}
	fmt.Println(e)
	var i I = T{"hello"}
	fmt.Println(i.M())
	fmt.Println(i)
	fmt.Printf("%v %T\n", i, i)
}
`,
		"anonymous_struct_field_type": `package main

import "fmt"

type Wrapper struct {
	Label string
	Inner struct {
		N int
	}
}

func main() {
	var w Wrapper
	w.Label = "outer"
	w.Inner.N = 7
	fmt.Println(w)
	fmt.Printf("%T\n", w)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			differGoSource(t, source, nil, "")
		})
	}
}

// TestGoSourceLocalTypeMethodCallbacks covers the fmt interface callbacks. The
// dependency holds a materialised local type whose String or Error body is
// never compiled: each call re-enters the interpreter that owns the original
// body, including a body that itself calls back out to an imported function.
func TestGoSourceLocalTypeMethodCallbacks(t *testing.T) {
	cases := map[string]string{
		"value_receiver_stringer": `package main

import "fmt"

type Person struct {
	Name string
	Age  int
}

func (p Person) String() string {
	return fmt.Sprintf("%v (%v years)", p.Name, p.Age)
}

func main() {
	a := Person{"Arthur Dent", 42}
	z := Person{"Zaphod Beeblebrox", 9001}
	fmt.Println(a, z)
	fmt.Printf("%v|%s|%T\n", a, a, a)
}
`,
		"array_receiver_stringer": `package main

import "fmt"

type IPAddr [4]byte

func (ip IPAddr) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
}

func main() {
	fmt.Println(IPAddr{127, 0, 0, 1})
	fmt.Println(IPAddr{8, 8, 8, 8})
}
`,
		"pointer_receiver_error": `package main

import "fmt"

type MyError struct {
	What string
	Code int
}

func (e *MyError) Error() string {
	return fmt.Sprintf("%s (code %d)", e.What, e.Code)
}

func main() {
	var err error = &MyError{"it broke", 7}
	fmt.Println(err)
	fmt.Printf("%v %T\n", err, err)
}
`,
		"value_receiver_error": `package main

import "fmt"

type ErrNegativeSqrt float64

func (e ErrNegativeSqrt) Error() string {
	return fmt.Sprint("cannot Sqrt negative number: ", float64(e))
}

func main() {
	var err error = ErrNegativeSqrt(-2)
	fmt.Println(err)
	fmt.Println(err.Error())
}
`,
		// A defined interface type carries no method declaration of its own in
		// the helper; the dynamic value's mirror is what fmt consults.
		"stringer_through_declared_interface": `package main

import "fmt"

type Stringish interface {
	String() string
}

type Tag string

func (t Tag) String() string { return "<" + string(t) + ">" }

func main() {
	var s Stringish = Tag("body")
	fmt.Println(s)
	fmt.Println(s.String())
	fmt.Printf("%v %T\n", s, s)
}
`,
		// A value method on a type whose String body has no imported call at
		// all: the callback must still reach the interpreter and come back.
		"stringer_without_nested_import": `package main

import "fmt"

type Tag string

func (t Tag) String() string {
	return "<" + string(t) + ">"
}

func main() {
	fmt.Println(Tag("body"))
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			differGoSource(t, source, nil, "")
		})
	}
}

// The original private-field refusal fixture now requires native equivalence.
// Typed local codecs preserve its hidden field without unsafe reflection.
func TestGoSourceLocalTypePrivateField(t *testing.T) {
	const source = `package main

import "fmt"

type Counter struct {
	N      int
	hidden string
}

func main() {
	fmt.Println(Counter{1, "secret"})
}
`
	differGoSource(t, source, nil, "")
}

// runGoSourceRunnerError runs one original source and returns the diagnostic
// the Runner reported, so a refused shape can be pinned by its exact reason.
func runGoSourceRunnerError(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	runErr := runner.Run(ctx, program.File)
	if runErr == nil {
		t.Fatalf("want a refusal, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	return runErr.Error() + stderr.String()
}
