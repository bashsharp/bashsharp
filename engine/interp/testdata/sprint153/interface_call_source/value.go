// Positive control: a value (not a call) initialising the same interface
// places was already supported.
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

type Shape interface{ Area() int }
type Sq struct{ s int }

func (q Sq) Area() int { return q.s * q.s }

func area(s Shape) int { return s.Area() }

func main() {
	fmt.Println(Cons[string]{"a", Nil[string]{}}.Len())
	var sh Shape = Sq{4}
	fmt.Println(sh.Area(), area(Sq{5}))
	sh = Sq{6}
	fmt.Println(sh.Area())
}
