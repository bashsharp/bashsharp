// A float declared from a type assertion keeps being a float: printing it
// through an imported function must carry it across as a float64, and
// arithmetic on it stays floating-point.
package main

import "fmt"

func half(v interface{}) float64 {
	f, ok := v.(float64)
	if !ok {
		return -1
	}
	return f / 2
}

func main() {
	var f interface{} = 1.5
	x, ok := f.(float64)
	fmt.Println(x, ok)
	fmt.Println(x+1, half(f), half(3.0))
	y := f.(float64)
	fmt.Printf("%T %v\n", y, y*2)
}
