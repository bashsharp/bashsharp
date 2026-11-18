// Outside-corpus reproducer: anonymous types in expression position. new
// with a struct or array literal operand (returned, assigned, declared) and
// method expressions whose receiver is an interface literal, a struct
// literal, or an instantiated generic type (value and pointer receivers).
package main

import "fmt"

type I interface{ M() string }

type A struct{}

func (A) M() string { return "A.M" }

type S[K, V any] struct{ k K }

func (s *S[K, V]) P() K { return s.k }

func (s S[K, V]) V() K { return s.k }

func zero() *struct{} { return new(struct{}) }

func arr() *[4]int32 { return new([4]int32) }

func main() {
	p := arr()
	(*p)[2] = 7
	fmt.Println(len(*p), (*p)[2], zero() != nil)
	var q *[3]byte
	q = new([3]byte)
	fmt.Println(len(*q))
	var r = new(struct{ a, b int })
	r.b = 5
	fmt.Println(r.a, r.b)
	f := interface{ M() string }.M
	fmt.Println(f(A{}))
	g := struct{ I }.M
	fmt.Println(g(struct{ I }{A{}}))
	h := (*S[int, string]).P
	fmt.Println(h(&S[int, string]{k: 42}))
	v := S[int, string].V
	fmt.Println(v(S[int, string]{k: 8}))
}
