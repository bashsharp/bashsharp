// The predeclared identifiers true and false stored in an interface: Go
// gives them the dynamic type bool, so an assertion to bool succeeds and a
// type switch takes the bool arm.
package main

import "fmt"

func describe(v interface{}) string {
	switch v.(type) {
	case bool:
		return "bool"
	case int:
		return "int"
	}
	return "other"
}

func main() {
	var i interface{} = false
	fmt.Println(i, describe(i))
	var j any = true
	fmt.Println(j, describe(j))
	i = true
	b, ok := i.(bool)
	fmt.Println(b, ok)
	var k interface{}
	k = false
	fmt.Println(k == false, k == true, describe(k))
	fmt.Println(describe(3))
}
