package main

import (
	"fmt"
	"go/ast"
	"time"
)

func build[T any](n, c int) []T { return make([]T, n, c) }
func main() {
	x := build[int](2, 4)
	y := build[struct{}](1, 3)
	z := make([]ast.Expr, 2)
	v := make([]time.Time, 1)
	fmt.Println(len(x), cap(x), len(y), cap(y), z[0] == nil, v[0].IsZero())
}
