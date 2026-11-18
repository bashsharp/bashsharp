// Outside-corpus reproducer: an untyped constant's NAME stored in an
// interface takes the constant's default type, as its literal does — by
// conversion, by declaration, by assignment and as a type switch operand.
package main

import "fmt"

const a = 0
const s = "text"
const f = 2.5
const b = true
const typed int64 = 7

func kind(v any) string {
	switch v.(type) {
	case int:
		return "int"
	case string:
		return "string"
	case float64:
		return "float64"
	case bool:
		return "bool"
	case int64:
		return "int64"
	}
	return "other"
}

func main() {
	i := (interface{})(a)
	fmt.Printf("%T %v\n", i, i)
	var j any = s
	fmt.Printf("%T %v\n", j, j)
	j = f
	fmt.Printf("%T %v\n", j, j)
	fmt.Println(kind(a), kind(s), kind(f), kind(b), kind(typed))
	switch x := any(a); x.(type) {
	case int:
		fmt.Println("switch int")
	}
}
