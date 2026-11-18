package main

import "fmt"

type Stringer interface{ String() string }

type myint int

func (m myint) String() string { return fmt.Sprintf("myint(%d)", int(m)) }

type Adder interface{ Add(int) int }

type acc int

func (a acc) Add(n int) int { return int(a) + n }

// stringify selects the bound method on the type parameter itself.
func stringify[T Stringer](s []T) []string {
	var out []string
	f1 := T.String
	for _, v := range s {
		out = append(out, f1(v))
	}
	return out
}

// apply passes a method expression with an argument through a func value.
func apply[T Adder](v T, n int) int {
	f := T.Add
	return f(v, n)
}

func main() {
	fmt.Println(stringify([]myint{1, 2}))
	fmt.Println(apply(acc(40), 2))
}
