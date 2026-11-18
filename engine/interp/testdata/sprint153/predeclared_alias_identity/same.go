// Positive control: a type switch and an assertion on the same spelling
// were already supported.
package main

import "fmt"

func kind(x interface{}) string {
	switch x.(type) {
	case byte:
		return "byte"
	case rune:
		return "rune"
	}
	return "other"
}

func main() {
	fmt.Println(kind(byte(1)), kind(rune(2)), kind("s"))
	var x interface{} = rune(9)
	r, ok := x.(rune)
	fmt.Println(r, ok)
}
