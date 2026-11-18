//go:build full

package interp_test

// Sprint: #322; Story: #1119; Story-ID: eb5ea2311479
//
// A native call that writes back into a caller-owned sequence (sort.Strings,
// sort.Ints) rebuilds each element through the bridge. Since sh 36121727 the
// bridge tags a scalar callback result with scalar metadata; that tag must stay
// on the top-level result and never reach the elements of a collection, or a
// range variable over the written-back slice binds as an object ("s is not a
// scalar"). Every case runs the unchanged source against a real Go build.
import "testing"

func TestGoSourceNativeWritebackElementsStayScalar(t *testing.T) {
	cases := map[string]string{
		"sort_strings_then_range": `package main

import (
	"fmt"
	"sort"
)

func main() {
	values := map[string]string{"b": "2", "a": "1"}
	keys := []string{"b", "a"}
	sort.Strings(keys)
	for i, key := range keys {
		fmt.Println(i, key+"="+values[key])
	}
}
`,
		"sort_ints_then_range": `package main

import (
	"fmt"
	"sort"
)

func main() {
	ns := []int{3, 1, 2}
	sort.Ints(ns)
	sum := 0
	for _, n := range ns {
		sum += n + 1
	}
	fmt.Println(ns, sum)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
