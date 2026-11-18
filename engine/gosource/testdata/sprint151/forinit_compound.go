// Mechanism: for-init that the converter lowers into several statements — a
// tuple assignment with a non-identifier target — so c.one sees a compound
// simple statement.
package main

import "fmt"

func main() {
	a := [2]int{}
	var i int
	for a[0], i = 5, 0; i < 3; i++ {
		a[0]++
	}
	fmt.Println(a[0], i)
}
