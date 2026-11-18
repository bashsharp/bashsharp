// run

package main

import "fmt"

func main() {
	var m map[string]int
	delete(m, "x")
	fmt.Println("zero", len(m))
}
