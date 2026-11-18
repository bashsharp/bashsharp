// Mechanism: goto out of nested blocks — a for inside an if inside a range —
// to a label in the enclosing function body.
package main

import "fmt"

func find(rows [][]int, want int) (int, int) {
	var ri, ci int
	for r, row := range rows {
		if len(row) > 0 {
			for c, v := range row {
				if v == want {
					ri, ci = r, c
					goto found
				}
			}
		}
	}
	return -1, -1
found:
	return ri, ci
}

func main() {
	rows := [][]int{{1, 2}, {}, {3, 4, 5}}
	fmt.Println(find(rows, 4))
	fmt.Println(find(rows, 9))
}
