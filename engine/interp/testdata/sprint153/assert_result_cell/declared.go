// Positive control: the same assertions bound by a declaration first were
// already supported.
package main

import "fmt"

type List[a any] interface {
	Len() int
}

type Cons[a any] struct {
	Head a
}

func (xs Cons[a]) Len() int { return 1 }

type Pair struct{ A, B int }

func sum(p Pair) int { return p.A + p.B }

func main() {
	var v any = Cons[int]{3}
	l, ok := v.(List[int])
	fmt.Println(ok, l.Len())
	var p any = Pair{1, 2}
	pp := p.(Pair)
	fmt.Println(sum(pp), pp.B)
}
