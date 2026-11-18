//go:build full

// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// TestS219ArrayMethodRegression covers the issue27961 method chain without
// depending on the Go corpus fixture. Named arrays remain value types while a
// non-finite element travels through successive method results.
func TestS219ArrayMethodRegression(t *testing.T) {
	const src = `package main
import (
	"fmt"
	"math"
)

type Vec2 [2]float64

func (v Vec2) A() Vec2 { return Vec2{v[0], v[0]} }
func (v Vec2) B() Vec2 { return Vec2{1.0 / v.D(), 0} }
func (v Vec2) C() Vec2 { return Vec2{v[0], v[0]} }
func (v Vec2) D() float64 { return math.Sqrt(v[0]) }

func main() {
	var zero Vec2
	zero.A().B().C().D()
	finite := Vec2{16, 9}.A().C().D()
	original := Vec2{4, 9}
	copy := original.A()
	copy[0] = 25
	fmt.Println(finite, original[0], copy[0])
}
`
	out, stderr, err := runGoSource(t, "s219arraymethod", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "4 4 25\n"))
}
