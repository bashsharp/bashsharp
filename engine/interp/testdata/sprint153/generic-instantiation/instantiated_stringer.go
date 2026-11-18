// A local generic type instantiated with concrete arguments, handed to an
// imported call that drives its mirrored String method. The instance is
// materialised in the helper under a generated name whose fields are the
// generic body with the type arguments substituted; the String stub calls
// back through the base generic name.
package main

import "fmt"

type Box[T1 any, T2 any] struct {
	first  T1
	second T2
}

func (b *Box[_, _]) String() string {
	return fmt.Sprintf("%v/%v", b.first, b.second)
}

func main() {
	b := &Box[string, int]{first: "hi", second: 7}
	fmt.Println(b)
	fmt.Printf("%v\n", b)
}
