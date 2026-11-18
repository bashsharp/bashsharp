package main

import "fmt"

type Iterator[T any] interface {
	Iterate(fn func(T) bool)
}

type IteratorFunc[T any] func(fn func(T) bool)

func (f IteratorFunc[T]) Iterate(fn func(T) bool) {
	f(fn)
}

func FromIterator[T any](it Iterator[T]) int {
	n := 0
	it.Iterate(func(t T) bool { n++; fmt.Println("item", t); return true })
	return n
}

func Make[R any](vals []R) int {
	it := func(fn func(R) bool) {
		for _, v := range vals {
			if !fn(v) {
				return
			}
		}
	}
	return FromIterator[R](IteratorFunc[R](it))
}

func main() {
	fmt.Println(Make([]int{1, 2}))
	fmt.Println(Make([]string{"a"}))
}
