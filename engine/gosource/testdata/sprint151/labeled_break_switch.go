// Mechanism: labeled break leaving a for loop from inside a switch arm, where a
// bare break would only leave the switch.
package main

import "fmt"

func main() {
	n := 0
loop:
	for {
		switch n {
		case 3:
			break loop
		default:
			n++
		}
	}
	fmt.Println(n)
}
