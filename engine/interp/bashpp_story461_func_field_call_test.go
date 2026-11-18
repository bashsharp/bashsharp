//go:build full

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// Calls of func-typed struct fields. The remaining corpus roots
// fixedbugs/issue43619.go (`t.f(t.a, t.b, t.x)` on a named func type field),
// gcgort.go (`f.t()` on an anonymous func type field of a named struct) and
// nilptr2.go (`tt.fn()` on an anonymous struct's func field) all invoke a
// field as a call; selector binding used to consider methods only and
// diagnosed `type T has no method f`. These are outside-corpus reductions of
// the three shapes plus the negative space: a nil field value must keep Go's
// nil-function run-time panic, and an unknown or non-func selector must keep
// the method diagnostic rather than being treated as a field.
package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// issue43619.go shape: a named func type field, filled from declared
// functions, called with sibling field arguments inside a range loop.
func TestStory461NamedFuncTypeFieldCall(t *testing.T) {
	src := `package main
import "fmt"
type fn func(a, b float64, x uint64) uint64
type testCase struct {
	f       fn
	a, b    float64
	x, want uint64
}
func lt(a, b float64, x uint64) uint64 {
	if a < b {
		x = 0
	}
	return x
}
func ge(a, b float64, x uint64) uint64 {
	if a >= b {
		x = 0
	}
	return x
}
func main() {
	for _, t := range []testCase{
		{lt, 1.0, 2.0, 123, 0},
		{ge, 1.0, 2.0, 123, 123},
		{lt, 2.0, 1.0, 123, 123},
	} {
		got := t.f(t.a, t.b, t.x)
		fmt.Println(got == t.want)
	}
}`
	out, stderr, err := runGoSource(t, "story461namedfnfield", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\ntrue\ntrue\n"))
}

// gcgort.go shape: anonymous func type fields on a named struct, the value
// passed as a parameter and its fields invoked as calls.
func TestStory461AnonFuncTypeFieldCall(t *testing.T) {
	src := `package main
import "fmt"
type modifier struct {
	name string
	t    func()
	pointerT func()
}
func run(f modifier) {
	f.t()
	f.pointerT()
}
func main() {
	n := 0
	m := modifier{
		name:     "int",
		t:        func() { n++ },
		pointerT: func() { n += 10 },
	}
	run(m)
	fmt.Println(m.name, n)
}`
	out, stderr, err := runGoSource(t, "story461anonfnfield", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "int 11\n"))
}

// nilptr2.go shape: a range over a slice of an anonymous struct type whose
// func field is invoked per element.
func TestStory461AnonStructFuncFieldCall(t *testing.T) {
	src := `package main
import "fmt"
func main() {
	tests := []struct {
		name string
		fn   func()
	}{
		{"one", func() { fmt.Println("ran one") }},
		{"two", func() { fmt.Println("ran two") }},
	}
	for _, tt := range tests {
		fmt.Println(tt.name)
		tt.fn()
	}
}`
	out, stderr, err := runGoSource(t, "story461anonstructfnfield", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "one\nran one\ntwo\nran two\n"))
}

// Calling a func field whose value is nil is Go's nil-function call: a
// run-time panic, not a selector diagnostic.
func TestStory461NilFuncFieldCallPanics(t *testing.T) {
	src := `package main
type modifier struct {
	name string
	t    func()
}
func main() {
	m := modifier{name: "zero"}
	m.t()
}`
	out, stderr, err := runGoSource(t, "story461nilfnfield", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.StringContains(stderr, "panic: runtime error: invalid memory address or nil pointer dereference"))
}

// Go source never reaches the evaluator with these shapes: the go/types
// front end rejects an unknown selector and a call of a non-func field at
// parse time. The rejections are asserted so a front-end regression cannot
// silently hand the evaluator's field dispatch arbitrary names.
func TestStory461GoSourceRejectsBadSelectorCalls(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"unknown name": {
			src: `package main
type testCase struct{ f func() }
func main() {
	t := testCase{f: func() {}}
	t.g()
}`,
			want: "type testCase has no field or method g",
		},
		"non-func field": {
			src: `package main
type testCase struct{ name string }
func main() {
	t := testCase{name: "x"}
	t.name()
}`,
			want: "string is not a function",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gosource.Parse(strings.NewReader(tc.src), "story461selneg.go", gosource.Options{RunMain: true})
			qt.Assert(t, qt.IsNotNil(err))
			qt.Assert(t, qt.StringContains(err.Error(), tc.want))
		})
	}
}

// The evaluator itself — reached directly by the classic Bash++ dialect,
// which has no go/types front end — must keep the method diagnostic for an
// unknown name and for a field that is not func-typed, rather than treating
// arbitrary selectors as dispatchable fields.
func TestStory461EvaluatorKeepsMethodDiagnostic(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"unknown name": {
			src:  "type T struct { name string }\nfunc main() {\n var v T = T{name: \"x\"}\n v.g()\n}\nmain()\n",
			want: "type T has no method g\n",
		},
		"non-func field": {
			src:  "type T struct { name string }\nfunc main() {\n var v T = T{name: \"x\"}\n v.name()\n}\nmain()\n",
			want: "type T has no method name\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.src), "story461selneg.bpp")
			qt.Assert(t, qt.IsNil(err))
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			err = r.Run(context.Background(), f)
			var status interp.ExitStatus
			qt.Assert(t, qt.IsTrue(errors.As(err, &status)))
			qt.Assert(t, qt.Equals(int(status), 2))
			qt.Assert(t, qt.Equals(out.String(), tc.want))
		})
	}
}
