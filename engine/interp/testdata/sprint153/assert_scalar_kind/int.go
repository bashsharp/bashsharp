// Positive control: an int declared from a type assertion already crossed
// to imported functions as an int.
package main

import "fmt"

func main() {
	var v interface{} = 7
	n, ok := v.(int)
	fmt.Println(n, ok)
	fmt.Println(n+1, n*2)
	var s any = "text"
	t, ok := s.(string)
	fmt.Println(t, ok, len(t))
}
