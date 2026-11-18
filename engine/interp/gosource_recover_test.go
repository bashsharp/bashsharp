// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

// Sprint 118 Story #3 (fa07603b71dc): interpreted GoSource panic/recover.
//
// recover's static type is interface{}, so `r := recover()` binds an interface,
// not a bare scalar. The regression these tests pin is `if r := recover(); r !=
// nil` — the canonical recover idiom from Go's own examples: a recovered payload
// must compare unequal to nil (even the empty string), and a recover that found
// nothing must compare equal to nil, without the comparison failing on "nil is
// not a scalar".

// gosourceRecoverExample is the Go blog "Defer, Panic, and Recover" program,
// the shape examples/recover exercises: g recurses until it panics, every
// abandoned frame's defer runs, and f's deferred recover catches the value and
// the caller resumes normally.
const gosourceRecoverExample = `package main

import "fmt"

func main() {
	f()
	fmt.Println("Returned normally from f.")
}

func f() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered in f", r)
		}
	}()
	fmt.Println("Calling g.")
	g(0)
	fmt.Println("Returned normally from g.")
}

func g(i int) {
	if i > 3 {
		fmt.Println("Panicking!")
		panic(fmt.Sprintf("%v", i))
	}
	defer fmt.Println("Defer in g", i)
	fmt.Println("Printing in g", i)
	g(i + 1)
}
`

const gosourceRecoverExampleWant = `Calling g.
Printing in g 0
Printing in g 1
Printing in g 2
Printing in g 3
Panicking!
Defer in g 3
Defer in g 2
Defer in g 1
Defer in g 0
Recovered in f 4
Returned normally from f.
`

func TestGoSourceRecoverExample(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/recover.go", gosourceRecoverExample)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, gosourceRecoverExampleWant))
}

// TestGoSourceRecoverInterfaceComparison pins the interface binding directly:
// the recover idiom must take both branches depending on whether a panic was in
// flight, and an empty recovered payload is still a non-nil interface.
func TestGoSourceRecoverInterfaceComparison(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			// A recovered payload is a non-nil interface: `r != nil` holds.
			"recovered payload compares unequal to nil",
			gosourceRecoverProgram(`	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered:", r)
		} else {
			fmt.Println("no panic")
		}
	}()
	panic("boom")`),
			"recovered: boom\n",
		},
		{
			// The empty string is a real recovered value, so the interface is
			// still non-nil even though the payload prints as nothing.
			"recovered empty payload is still non-nil",
			gosourceRecoverProgram(`	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered non-nil")
		} else {
			fmt.Println("nil")
		}
	}()
	panic("")`),
			"recovered non-nil\n",
		},
		{
			// No panic in flight: recover returns the nil interface, so the
			// idiom takes the else branch and the caller resumes.
			"no active panic compares equal to nil",
			gosourceRecoverProgram(`	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered:", r)
		} else {
			fmt.Println("no panic")
		}
	}()
	fmt.Println("body")`),
			"body\nno panic\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			out, errOut, err := bashPPRunGoSource(t, dir, dir+"/r.go", tc.src)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(errOut, ""))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

// gosourceRecoverProgram wraps a function body that runs under a deferred
// recover into a complete program whose main calls it.
func gosourceRecoverProgram(body string) string {
	return "package main\n\nimport \"fmt\"\n\nfunc f() {\n" + body + "\n}\n\nfunc main() {\n\tf()\n}\n"
}

// TestGoSourcePanicUnrecovered pins examples/panic: an unrecovered panic
// abandons every frame, reports `panic: <value>` on stderr, and terminates with
// status 2. Exact native stack-trace normalization is deliberately not
// attempted — only the panic line and the status are asserted.
func TestGoSourcePanicUnrecovered(t *testing.T) {
	t.Parallel()
	src := `package main

import "fmt"

func main() {
	fmt.Println("before")
	panic("boom")
}
`
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/panic.go", src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.Equals(out, "before\n"))
	qt.Assert(t, qt.IsTrue(strings.Contains(errOut, "panic: boom")))
}
