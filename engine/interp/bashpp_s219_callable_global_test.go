//go:build full

// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
func TestS219CallableGlobal(t *testing.T) {
	t.Run("success_nil_capture_and_order", func(t *testing.T) {
		const src = `package main
import "fmt"

var trace string

func callable(tag string, start int) func() int {
	trace += tag
	n := start
	return func() int { n++; return n }
}

func nilCallable() func() { trace += "N"; return nil }
func ordinary() int { trace += "V"; return 7 }
func controlledInit() func() int { trace += "C"; return g }

var g = callable("G", 10)
var nilg = nilCallable()
var value = ordinary()
var h = g
var controlled = controlledInit()

func main() {
	fmt.Println(trace, value, nilg == nil)
	fmt.Println(g(), h(), controlled(), g())
}
`
		out, stderr, err := runGoSource(t, "s219callableglobal", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "GNVC 7 true\n11 12 13 14\n"))
	})

	t.Run("initializer_panic_once", func(t *testing.T) {
		const src = `package main
import "fmt"

func fail() func() {
	fmt.Println("initializer-called")
	panic("initializer-failed")
}

var failed = fail()

func main() { fmt.Println("unreachable", failed) }
`
		out, stderr, err := runGoSource(t, "s219callableglobalpanic", src)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.Equals(out, "initializer-called\n"))
		qt.Assert(t, qt.Equals(strings.Count(stderr, "initializer-failed"), 1), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Not(qt.StringContains(stderr, "unreachable")))
	})
}
