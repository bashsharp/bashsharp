// f(g()) with a multi-result g: every result of g is an argument of f, in
// order, whether f is a declared function, a variadic one, a method, or an
// imported function, and whether g is declared here or imported.
package main

import (
	"fmt"
	"strconv"
)

type acc struct{ total int }

func (a *acc) add(x, y int) int { a.total += x + y; return a.total }

func add(a, b int) int { return a + b }
func sum(xs ...int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}
func pair() (int, int)             { return 1, 2 }
func triple() (int, int, int)      { return 3, 4, 5 }
func named() (s string, err error) { return "ok", nil }
func show(s string, err error)     { fmt.Println(s, err) }

var calls int

func counted() (int, int) { calls++; return calls, calls * 10 }

func main() {
	fmt.Println(add(pair()))
	fmt.Println(sum(triple()))
	a := &acc{}
	fmt.Println(a.add(pair()), a.total)
	fmt.Println(pair())
	show(named())
	fmt.Println(strconv.Atoi("12"))
	show(strconv.Quote("q"), nil)
	fmt.Println(add(counted()), calls)
}
