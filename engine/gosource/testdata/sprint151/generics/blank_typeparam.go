package main

import "fmt"

// Blank type parameters may repeat, in a declaration and on a receiver.
type Pair[_ any, _ any] struct{ n int }

func (p Pair[_, _]) N() int { return p.n }

func ignore[_ any, _ any](n int) int { return n * 2 }

func main() {
	p := Pair[string, float64]{n: 21}
	fmt.Println(p.N(), ignore[int, bool](4))
}

// Blank type declarations may repeat too; go/types checks each one.
type (
	_[T struct{}]                     struct{}
	_[M map[K]V, K comparable, V any] struct{}
	_[_ interface{} | int]            struct{}
)
