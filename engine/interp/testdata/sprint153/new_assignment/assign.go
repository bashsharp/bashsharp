// new(T) on the right of a plain assignment — to a declared pointer, to a
// slice element, to a map element, to a struct field — and as the single
// result of a return.
package main

import "fmt"

type S struct{ n int }
type holder struct{ p *int }

func fresh() *S { return new(S) }

func freshOf[T any]() *T { return new(T) }

func main() {
	var q *int
	q = new(int)
	*q = 4
	fmt.Println(*q)
	ptrs := make([]*int, 2)
	for i := range ptrs {
		ptrs[i] = new(int)
		*ptrs[i] = i + 10
	}
	fmt.Println(*ptrs[0], *ptrs[1], ptrs[0] == ptrs[1])
	m := map[string]*S{}
	m["a"] = new(S)
	m["a"].n = 7
	fmt.Println(m["a"].n)
	var h holder
	h.p = new(int)
	fmt.Println(*h.p)
	fmt.Println(fresh().n, *freshOf[int](), *freshOf[string]() == "")
}
