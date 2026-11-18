// Mechanism: range assignment target that is an index expression (a[i]).
package main

import "fmt"

func main() {
	a := [2]int{}
	for a[0], a[1] = range []int{10, 20} {
	}
	fmt.Println(a[0], a[1])
}
