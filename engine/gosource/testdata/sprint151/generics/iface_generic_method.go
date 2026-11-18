package main

import "fmt"

type Iterator[T any] interface {
	Iterate(fn func(T) bool)
}

type Stream[T any] struct {
	it Iterator[T]
}

type myIterator struct{}

func (myIterator) Iterate(fn func(int) bool) { fn(1) }

// Named[T] implements Iterator[T] for every T; the interface's method
// signature mentions T on both sides.
type Named[T any] struct{ vals []T }

func (n Named[T]) Iterate(fn func(T) bool) {
	for _, v := range n.vals {
		if !fn(v) {
			return
		}
	}
}

// Count assigns a generic struct to a generic interface inside a generic
// body: both sides are spelled with T and instantiated together.
func Count[T any](vals []T) int {
	var it Iterator[T] = Named[T]{vals: vals}
	n := 0
	it.Iterate(func(T) bool { n++; return true })
	return n
}

func main() {
	s := Stream[int]{}
	s.it = myIterator{}
	s.it.Iterate(func(i int) bool { fmt.Println("got", i); return true })
	t := Stream[int]{it: myIterator{}}
	t.it.Iterate(func(i int) bool { fmt.Println("got2", i); return true })
	fmt.Println(Count([]string{"a", "b", "c"}))
	fmt.Println(Count([]float64{1, 2}))
}
