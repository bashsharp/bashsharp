//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS219VariadicTransport(t *testing.T) {
	src := `package main
import "fmt"

var effects int

func sumSlice(values []int) int {
	values[0]++
	total := 0
	for _, value := range values { total += value }
	return total
}

func sum(values ...int) int { return sumSlice(values) }

func pair() (int, int) {
	effects++
	return 4, 5
}

func add(a, b int) int { return a + b }

func chooser() func(...int) int {
	effects++
	return sum
}

func main() {
	values := []int{1, 2, 3}
	got := sum(values...)
	fmt.Println(got, values[0])
	fmt.Println(add(pair()), effects)
	fmt.Println(chooser()(values...), effects, values[0])
}
`
	out, stderr, err := runGoSource(t, "s219variadictransport", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "7 2\n9 1\n8 2 3\n"))
}
