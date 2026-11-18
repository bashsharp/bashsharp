//go:build full

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

// Comparable arrays and structs recurse through interface elements. The
// dynamic value still decides whether an interface comparison can proceed:
// comparable values compare normally, a preceding unequal field short-circuits,
// and an interface holding a slice panics when comparison reaches it.
func TestStory461ComparableAggregateInterfaceElements(t *testing.T) {
	src := `package main
import "fmt"

type item struct {
	n int
	v any
}

func main() {
	x := item{1, "ok"}
	y := item{1, "ok"}
	left := [2]any{x, [2]int{3, 4}}
	right := [2]any{y, [2]int{3, 4}}
	different := [2]any{y, [2]int{3, 5}}
	fmt.Println(left == right, left == different)

	var bad any = []int{1}
	fmt.Println(item{1, bad} == item{2, bad})
	fmt.Println([1]any{bad} == [1]any{bad})
}`
	out, stderr, err := runGoSource(t, "story461aggregateiface", src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "true false\nfalse\n"))
	if !strings.Contains(stderr, "panic: runtime error: comparing uncomparable type []int") {
		t.Fatalf("stderr %q does not contain the non-comparable dynamic-type panic", stderr)
	}
}
