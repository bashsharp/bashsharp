// Positive control: the same comma-ok reads spelled with `:=`, which the
// interpreter already supported.
package main

import "fmt"

func main() {
	ages := map[string]int{"ann": 41}
	age, ok := ages["ann"]
	fmt.Println(age, ok)
	missing, found := ages["bob"]
	fmt.Println(missing, found)

	var v interface{} = 7
	n, isInt := v.(int)
	fmt.Println(n, isInt)
	s, isString := v.(string)
	fmt.Printf("%q %v\n", s, isString)
}
