//go:build full

package interp_test

import (
	"github.com/go-quicktest/qt"
	"testing"
)

func TestS219NumericCallComparison(t *testing.T) {
	src := `package main
var calls int
func value() float64 { calls++; return 42 }
func main() {
 println(value() == 42, 42 == value(), value() != 41, value() == 41, calls)
}`
	_, stderr, err := runGoSource(t, "s219numericcompare", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.Equals(stderr, "true true true false 4\n"))
}
