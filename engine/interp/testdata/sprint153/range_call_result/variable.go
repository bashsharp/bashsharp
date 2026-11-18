// Positive control: range over the same values held in variables was
// already supported.
package main

import "fmt"

func words() []string { return []string{"q", "r"} }

func main() {
	ws := words()
	for _, w := range ws {
		fmt.Println(w + "?")
	}
	s := "ab"
	for i, r := range s {
		fmt.Println(i, string(r))
	}
	n := 2
	for i := range n {
		fmt.Println("i", i)
	}
}
