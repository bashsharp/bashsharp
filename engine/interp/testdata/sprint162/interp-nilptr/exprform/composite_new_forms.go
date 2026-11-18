package main

import (
	"fmt"
	"reflect"
)

type T struct{ a, b int }
type i4 struct{ a, b, c, d int }

func F(x i4) i4 { return x }

func main() {
	if (T{1, 2}) == (T{3, 4}) {
		fmt.Println("BUG")
	}
	if (T{1, 2}) == (T{1, 2}) {
		fmt.Println("equal")
	}
	z := F(i4{12, 34, 6, 8})
	if (i4{12, 34, 6, 8}) != z {
		fmt.Println("BUG")
	}
	switch (T{1, 2}) {
	case T{1, 2}:
		fmt.Println("matched")
	default:
		fmt.Println("BUG")
	}
	fmt.Println(reflect.TypeOf(new(int32)), fmt.Sprint(new(int32)) != "")
	fmt.Println(reflect.TypeOf(new([3]int)).Elem())
	var x interface{}
	x = interface{}(new(T))
	fmt.Println(x != nil, x.(*T).a)
	var t T
	t = T{5, 6}
	fmt.Println(t)
}

func init() {
	// A composite literal compared with a value of another type does not
	// compile in Go and is refused by the checker; the switch below is the
	// value form with a fallthrough default.
	switch (i4{1, 2, 3, 4}) {
	case i4{9, 9, 9, 9}:
		fmt.Println("BUG")
	default:
		fmt.Println("no match")
	}
}
