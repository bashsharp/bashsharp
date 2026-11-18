// Mechanism: labeled break leaving a for loop from inside a select case.
package main

import "fmt"

func main() {
	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	ch <- 3
	close(ch)
	total := 0
drain:
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				break drain
			}
			total += v
		}
	}
	fmt.Println(total)
}
