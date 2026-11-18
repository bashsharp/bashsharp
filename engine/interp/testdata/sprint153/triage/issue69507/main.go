// Reduced from fixedbugs/issue69507.go — range-over-func iterator (Go 1.23) with a byte-keyed map.
package main

import "fmt"

type Seq[V any] func(yield func(V) bool)

func selections(s string) Seq[string] {
	return func(yield func(string) bool) {
		for bits := 1; bits < 1<<len(s); bits++ {
			var choice string
			for j, char := range s {
				if bits&(1<<j) != 0 {
					choice += string(char)
				}
			}
			if !yield(choice) {
				break
			}
		}
	}
}

func main() {
	m := make(map[byte][]string)
	for sel := range selections("AB") {
		m[sel[0]] = append(m[sel[0]], sel)
	}
	fmt.Println(len(m))
}
