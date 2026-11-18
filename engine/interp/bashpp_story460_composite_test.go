//go:build full

package interp_test

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestStory460DeepCollectionCarrier(t *testing.T) {
	// This is deliberately outside the Go corpus. A generated nested slice lets
	// one typed value exceed the shell object's JSON depth budget without
	// copying any fixedbugs fixture.
	const depth = 110
	sliceType := strings.Repeat("[]", depth) + "int"
	value := sliceType + strings.Repeat("{", depth) + "1" + strings.Repeat("}", depth)
	arrayType := strings.Repeat("[1]", depth) + "int"
	src := `package main
import "fmt"
type deepArray ` + arrayType + `
func main() {
	v := ` + value + `
	p := new(deepArray)
	fmt.Println(len(v), fmt.Sprint(v) == "` + strings.Repeat("[", depth) + `1` + strings.Repeat("]", depth) + `", fmt.Sprint(p) == "&` + strings.Repeat("[", depth) + `0` + strings.Repeat("]", depth) + `")
}`
	out, stderr, err := runGoSource(t, "story460deepcarrier", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1 true true\n"))
}

func TestStory460CompositeValueCarriers(t *testing.T) {
	src := `package main
import (
	"fmt"
	"reflect"
)
type item struct { n int }
type key struct { a, b int32; c float64 }
type holder struct { values []int }
func main() {
	i := item{n: 1}
	a := [...]*item{&i, &i, &i}
	nested := [][len(a)]*item{a, a}
	pointers := []*item{{n: 2}, {n: 3}}

	m := map[key]bool{{a: 1, b: 2, c: 3}: true}
	t := reflect.ArrayOf(8, reflect.TypeOf(""))
	reflected := reflect.New(t).Elem().Interface().([8]string)
	nativeKey := map[[8]string]int{reflected: 7}

	original := holder{values: []int{9}}
	copied := []holder{original}
	var inferred = []int{1}
	inferred = nil

	fmt.Println(len(nested[1]), pointers[1].n, m[key{a: 1, b: 2, c: 3}], nativeKey[reflected])
	fmt.Println(&original.values[0] == &copied[0].values[0], inferred == nil)
}`
	out, stderr, err := runGoSource(t, "story460composite", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "3 3 true 7\ntrue true\n"))
}
