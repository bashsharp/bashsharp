// Positive control: an interface conversion bound by a declaration first was
// already supported.
package main

import (
	"fmt"
	"strconv"
)

type myint int

func (m myint) String() string { return strconv.Itoa(int(m)) }

type Stringer interface{ String() string }

func direct[T Stringer](v T) string {
	v1 := Stringer(v)
	return v1.String()
}

func main() {
	m := myint(3)
	s := Stringer(m)
	fmt.Println(s.String())
	i := any(m)
	fmt.Println(i.(Stringer).String())
	fmt.Println(direct[myint](myint(5)))
}
