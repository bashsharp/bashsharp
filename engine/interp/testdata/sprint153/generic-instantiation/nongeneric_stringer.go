// Positive control: the non-generic form of the same shape, already supported
// before the instantiation mechanism.
package main

import "fmt"

type Box struct {
	first  string
	second int
}

func (b *Box) String() string {
	return fmt.Sprintf("%v/%v", b.first, b.second)
}

func main() {
	b := &Box{first: "hi", second: 7}
	fmt.Println(b)
}
