package main

import "fmt"

type Ordered interface {
	~int | ~int64 | ~float64 | ~string
}

func Abs[T ~int | ~float64](a T) T {
	if a < 0 {
		return -a
	}
	return a
}

func Diff[T ~int | ~float64](a, b T, abs func(T) T) T {
	return abs(a - b)
}

// Forward instantiates Abs with the enclosing type parameter and passes it
// as a value.
func Forward[T ~int | ~float64](a, b T) T {
	return Diff(a, b, Abs[T])
}

func Join[K comparable, V any](k K, v V) string { return fmt.Sprint(k, "=", v) }

func Show[T any](prefix string, xs ...T) {
	fmt.Println(prefix, len(xs), xs)
}

type Op struct {
	Name string
	Run  func(int) int
}

func Double[T ~int](x T) T { return x * 2 }

func main() {
	fmt.Println(Forward(3, 10))
	fmt.Println(Forward(2.0, 5.0))
	// Bare generic name: the checker infers T from the parameter's type.
	fmt.Println(Diff(7, 9, Abs))
	f := Join[string, int]
	fmt.Println(f("a", 1))
	var g func(float64, string) string = Join
	fmt.Println(g(3.0, "b"))
	show := Show[int]
	show("ints", 1, 2, 3)
	show("none")
	op := Op{Name: "double", Run: Double[int]}
	run := op.Run
	fmt.Println(op.Name, run(21))
	ops := map[string]func(int) int{"abs": Abs[int], "double": Double}
	fmt.Println(ops["abs"](-4), ops["double"](4))
}
