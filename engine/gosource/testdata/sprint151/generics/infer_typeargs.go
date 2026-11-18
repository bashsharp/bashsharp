package main

import "fmt"

type Number interface {
	~int | ~int64 | ~float64
}

func Sum[T Number](xs []T) T {
	var s T
	for _, x := range xs {
		s += x
	}
	return s
}

func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

// Twice calls a generic function from a generic body: the inner call's type
// argument is the outer type parameter.
func Twice[T Number](xs []T) T {
	return Sum(xs) + Sum[T](xs)
}

type MyFloat float64

func main() {
	fmt.Println(Sum([]float64{1.5, 2.5}))
	fmt.Println(Sum([]int{1, 2, 3}))
	fmt.Println(Sum([]MyFloat{0.25, 0.5}))
	fmt.Println(Map([]int{1, 2, 3}, func(x int) string { return fmt.Sprint(x * 2) }))
	fmt.Println(Map[float64, int]([]float64{1, 2}, func(x float64) int { return int(x) * 3 }))
	fmt.Println(Twice([]float64{1, 2}))
	fmt.Println(Twice([]int64{4, 5}))
}
