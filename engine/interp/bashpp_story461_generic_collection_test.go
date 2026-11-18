//go:build full

package interp_test

// Sprint: #209; Story: #461; Story-ID: 4ed649697945

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestStory461GenericNonScalarOperandCarrier(t *testing.T) {
	src := `package main
type list[E any] interface { ~[]E; Equal(E, E) bool }
type sets[E comparable, T []E] []T
func (sets[E, T]) Equal(a, b T) bool { return len(a) == len(b) && a[0] == b[0] }
func intersect[E comparable, L list[[]E]](x, y L) bool {
	for _, xe := range x { for _, ye := range y { if x.Equal(xe, ye) { return true } } }
	return false
}
func main() {
	x := [][]int{{1}}
	if !intersect[int, sets[int, []int]](sets[int, []int](x), sets[int, []int](x)) { panic("slice carrier lost") }
}`
	out, stderr, err := runGoSource(t, "story461generic", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, ""))
}

func TestStory461GenericNonScalarOperandMismatch(t *testing.T) {
	src := `package main
type sets[E comparable, T []E] []T
func main() {
	x := [][]string{{"wrong"}}
	_ = sets[int, []int](x)
}`
	_, err := gosource.Parse(strings.NewReader(src), "story461generic-mismatch.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(err.Error(), "cannot convert x")))
}
