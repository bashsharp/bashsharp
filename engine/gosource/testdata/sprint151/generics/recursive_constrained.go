package main

import "fmt"

type Ordered interface {
	~int | ~int64 | ~float64 | ~string
}

// List refers to itself with its own type parameter as the argument; the
// constraint is checked where List is instantiated, not where it is declared.
type List[T Ordered] struct {
	next *List[T]
	val  T
}

func (l *List[T]) Push(v T) *List[T] { return &List[T]{next: l, val: v} }

func (l *List[T]) Largest() T {
	var max T
	for p := l; p != nil; p = p.next {
		if p.val > max {
			max = p.val
		}
	}
	return max
}

func main() {
	var l *List[int]
	l = l.Push(3).Push(9).Push(4)
	fmt.Println(l.Largest())
	var s *List[string]
	fmt.Println(s.Push("b").Push("a").Largest())
}
