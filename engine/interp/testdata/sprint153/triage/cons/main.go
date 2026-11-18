// Reduced from typeparam/cons.go — recursive generic Map over a generic linked list,
// with a type assertion to an instantiated generic interface List[a].
package main

import "fmt"

type List[a any] interface {
	Match(f func(Cons[a]) any) any
}

type Cons[a any] struct {
	Head a
	Tail List[a]
}

func (xs Cons[a]) Match(f func(Cons[a]) any) any { return f(xs) }

type Nil[a any] struct{}

func (xs Nil[a]) Match(f func(Cons[a]) any) any { return nil }

func Map[a any](xs List[a]) List[a] {
	r := xs.Match(func(c Cons[a]) any { return Cons[a]{c.Head, Map[a](c.Tail)} })
	if r == nil {
		return Nil[a]{}
	}
	return r.(List[a])
}

func main() {
	var xs List[int] = Cons[int]{1, Cons[int]{2, Nil[int]{}}}
	ys := Map[int](xs)
	fmt.Println(ys.(Cons[int]).Head)
}
