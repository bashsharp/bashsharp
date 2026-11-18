package main

import (
	"fmt"
	"sort"
)

// Set is a generic map keyed by a type parameter.
type Set[Elem comparable] struct {
	m map[Elem]struct{}
}

func Make[Elem comparable]() Set[Elem] {
	return Set[Elem]{m: make(map[Elem]struct{})}
}

func (s Set[Elem]) Add(v Elem) { s.m[v] = struct{}{} }

func (s Set[Elem]) Contains(v Elem) bool {
	_, ok := s.m[v]
	return ok
}

func (s Set[Elem]) Len() int { return len(s.m) }

// Count builds a map[K]int inside a generic body.
func Count[K comparable](keys []K) map[K]int {
	out := map[K]int{}
	for _, k := range keys {
		out[k]++
	}
	return out
}

func main() {
	s := Make[int]()
	s.Add(1)
	s.Add(2)
	s.Add(1)
	fmt.Println(s.Len(), s.Contains(2), s.Contains(3))
	t := Make[string]()
	t.Add("a")
	fmt.Println(t.Len(), t.Contains("a"))
	c := Count([]string{"x", "y", "x"})
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Println(k, c[k])
	}
}
