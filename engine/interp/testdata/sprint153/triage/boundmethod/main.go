// Reduced from typeparam/boundmethod.go — bound-method call and method expression on a type parameter.
package main

import (
	"fmt"
	"strconv"
)

type myint int

func (m myint) String() string { return strconv.Itoa(int(m)) }

type Stringer interface{ String() string }

func stringify[T Stringer](s []T) string {
	v := s[0]
	f := T.String // method expression on the type parameter
	return v.String() + f(v)
}

func main() {
	fmt.Println(stringify([]myint{7}))
}
