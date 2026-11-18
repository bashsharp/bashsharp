//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/go-quicktest/qt"
)

func TestS219BuiltinMultiResultCollections(t *testing.T) {
	src := `package main
import "fmt"
var calls int
var base = []int{1, 2}
var table = map[string]int{"x": 7}
func pair() ([]int, []int) { calls++; return base, []int{8, 9} }
func entry() (map[string]int, string) { calls++; return table, "x" }
func main() {
	fmt.Println(copy(pair()), base, calls)
	delete(entry())
	_, ok := table["x"]
	fmt.Println(ok, calls)
}`
	out, stderr, err := runGoSource(t, "s219builtinmulticollections", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "2 [8 9] 1\nfalse 2\n"))
}

func TestS219BuiltinMultiResultPrint(t *testing.T) {
	src := `package main
var calls int
func values() (int16, float64, string) { calls++; return 7, 2.5, "ok" }
func main() { print(values()); println(); println(values()); println(calls) }`
	out, stderr, err := runGoSource(t, "s219builtinmultiprint", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "72.5ok\n7 2.5 ok\n2\n"))
}

func TestS219BuiltinMultiResultNegativeForms(t *testing.T) {
	tests := map[string]string{
		"mixed_extra_argument": `package main
func pair() ([]int, []int) { println("PAIR-RAN"); return nil, nil }
func main() { copy([]int{}, pair()) }`,
		"wrong_expanded_arity": `package main
func pair() ([]int, []int) { println("PAIR-ONCE"); return nil, nil }
func main() { len(pair()) }`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := gosource.Parse(strings.NewReader(src), "s219builtinmultineg"+name+".go", gosource.Options{RunMain: true})
			qt.Assert(t, qt.Not(qt.IsNil(err)))
			qt.Assert(t, qt.IsTrue(strings.Contains(err.Error(), map[string]string{
				"mixed_extra_argument": "multiple-value pair()",
				"wrong_expanded_arity": "too many arguments for len",
			}[name])))
		})
	}
}
