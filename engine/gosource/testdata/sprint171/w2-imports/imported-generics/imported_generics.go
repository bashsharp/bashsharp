// Outside-corpus reproducer: calls of imported generic functions. The type
// arguments are inferred (maps.Clone, slices.Max), spelled (slices.Max[[]int]),
// partly a local type (maps.Keys over a map[key]int), reached only through a local generic body whose parameter
// binds them (dedupe[string]), and return an instantiated imported type
// (unique.Make), and name a type of a package the helper imports after
// the function's own (cmp.Or over time.Duration).
package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"
	"unique"
)

type key string

func dedupe[T comparable](in []T) []T {
	seen := map[T]bool{}
	var out []T
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return slices.Clip(out)
}

func main() {
	m := map[string]int{"a": 1, "b": 2}
	c := maps.Clone(m)
	delete(m, "a")
	fmt.Println(len(m), len(c), c["a"])

	fmt.Println(slices.Max([]int{3, 9, 2}), slices.Max[[]int]([]int{5, 1}))
	fmt.Println(slices.Index([]string{"a", "b"}, "b"))
	keys := slices.Sorted(maps.Keys(map[key]int{"x": 1, "y": 2}))
	fmt.Println(keys)

	fmt.Println(dedupe([]string{"a", "b", "a"}))

	h := unique.Make("handle")
	fmt.Println(h.Value(), h == unique.Make("handle"))

	fmt.Println(cmp.Or(0*time.Second, time.Minute))
}
