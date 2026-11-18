// Positive control: other untyped constants stored in an interface already
// took their Go default types.
package main

import "fmt"

func main() {
	var i interface{} = 3
	n, ok := i.(int)
	fmt.Println(n, ok)
	var s any = "text"
	t, ok := s.(string)
	fmt.Println(t, ok)
}
