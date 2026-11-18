// Mechanism: labeled break on a nested for loop (LabeledStmt + labeled branch).
package main

import "fmt"

func main() {
	found := 0
outer:
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			if i*j == 6 {
				found = i*10 + j
				break outer
			}
		}
	}
	fmt.Println(found)
}
