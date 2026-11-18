// Two-target assignment whose single right-hand side yields a comma-ok
// boolean: a map index and a type assertion, assigned with `=` to variables
// that already exist (including the same variable twice, where Go's
// left-to-right store leaves the second value).
package main

import "fmt"

func main() {
	ages := map[string]int{"ann": 41}
	var age int
	var ok bool
	age, ok = ages["ann"]
	fmt.Println(age, ok)
	age, ok = ages["bob"]
	fmt.Println(age, ok)

	var x bool
	present := map[int]bool{0: false}
	x, x = present[0]
	fmt.Println(x)

	var v interface{} = 7
	var n int
	n, ok = v.(int)
	fmt.Println(n, ok)
	var s string
	s, ok = v.(string)
	fmt.Printf("%q %v\n", s, ok)
	var b interface{} = false
	x, x = b.(bool)
	fmt.Println(x)
	_, ok = v.(fmt.Stringer)
	fmt.Println(ok)
}
