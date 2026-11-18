// Positive control: a literal-length named array value keeps crossing under
// its materialised name.
package main

import "fmt"

type pair [2]int

func main() {
	fmt.Println(pair{4, 5})
}
