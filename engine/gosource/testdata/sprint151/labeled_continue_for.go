// Mechanism: labeled continue on a nested for loop (LabeledStmt + labeled branch).
package main

import "fmt"

func main() {
	sum := 0
rows:
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			if j == 2 {
				continue rows
			}
			sum += i * j
		}
	}
	fmt.Println(sum)
}
