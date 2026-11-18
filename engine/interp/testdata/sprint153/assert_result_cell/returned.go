// A type assertion returned from a function, or passed as an argument,
// keeps the asserted value: an interface value keeps its dynamic type (a
// generic instance's type arguments included), and a struct keeps its
// fields.
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

func wrapInt(v any) List[int] { return v.(List[int]) }

func wrap[a any](v any) List[a] { return v.(List[a]) }

func sum(p Pair) int { return p.A + p.B }

func pairOf(v any) Pair { return v.(Pair) }

func main() {
	var v any = Cons[int]{3}
	fmt.Println(wrapInt(v).Len())
	fmt.Println(wrap[int](v).Len())
	var p any = Pair{1, 2}
	fmt.Println(sum(p.(Pair)))
	fmt.Println(pairOf(p).B)
	var n any = 40
	fmt.Println(n.(int) + 2)
}
