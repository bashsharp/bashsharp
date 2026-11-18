//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS219TypeSwitchBinding(t *testing.T) {
	src := `package main
import "fmt"

func whatis(v any) {
	switch x := v.(type) {
	case int:
		fmt.Printf("single %T %v\n", x, x)
	case string, bool:
		y, ok := x.(string)
		fmt.Printf("multi %T %v %q %v\n", x, x, y, ok)
	case nil:
		y, ok := x.(int)
		fmt.Printf("nil %T %v %v %v\n", x, x, y, ok)
	default:
		y, ok := x.(float64)
		fmt.Printf("default %T %v %v %v\n", x, x, y, ok)
	}
}

func main() {
	whatis(1.5)
	whatis("text")
	whatis(true)
	whatis(3)
	whatis(nil)
}
`
	out, stderr, err := runGoSource(t, "s219_typeswitch_binding", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "default float64 1.5 1.5 true\nmulti string text \"text\" true\nmulti bool true \"\" false\nsingle int 3\nnil <nil> <nil> 0 false\n"))
}
