// A conversion to an interface used as a value: as a method call's
// receiver, as an argument, as a returned value, and inside a closure in a
// generic body converting the type parameter's value to its constraint.
package main

import (
	"fmt"
	"strconv"
)

type myint int

func (m myint) String() string { return strconv.Itoa(int(m)) }

type Stringer interface{ String() string }

func describe(s Stringer) string { return "<" + s.String() + ">" }

func box(m myint) Stringer { return Stringer(m) }

func show[T Stringer](v T) string {
	f := func(v1 T) string {
		return Stringer(v1).String()
	}
	return f(v)
}

func main() {
	m := myint(3)
	fmt.Println(Stringer(m).String())
	fmt.Println(any(m).(Stringer).String())
	fmt.Println(describe(Stringer(m)), describe(any(m).(Stringer)))
	fmt.Println(box(m).String())
	fmt.Println(show[myint](myint(4)))
	s := Stringer(m).String() + "!"
	fmt.Println(s)
}
