// A call whose result initialises an interface-typed place — a field of a
// generic interface type in a composite literal, a variable, a parameter, a
// returned value — contributes the interface value it returned, with its
// dynamic type intact (type arguments included).
package main

import "fmt"

type List[a any] interface {
	Len() int
}

type Nil[a any] struct{}

func (Nil[a]) Len() int { return 0 }

type Cons[a any] struct {
	Head a
	Tail List[a]
}

func (xs Cons[a]) Len() int { return 1 + xs.Tail.Len() }

func empty[a any]() List[a] { return Nil[a]{} }

func push[a any](v a, xs List[a]) List[a] {
	return Cons[a]{v, xs}
}

func build[a any](vs ...a) List[a] {
	var xs List[a] = empty[a]()
	for _, v := range vs {
		xs = Cons[a]{v, push[a](v, xs)}
	}
	return xs
}

type Shape interface{ Area() int }
type Sq struct{ s int }

func (q Sq) Area() int { return q.s * q.s }

func mk(s int) Shape { return Sq{s} }

func area(s Shape) int { return s.Area() }

func main() {
	fmt.Println(build[int](1, 2).Len())
	fmt.Println(Cons[string]{"a", empty[string]()}.Len())
	var sh Shape = mk(4)
	fmt.Println(sh.Area(), area(mk(5)))
	sh = mk(6)
	fmt.Println(sh.Area())
}
