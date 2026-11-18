// run

package main

import "fmt"

func main() {
	defer func() {
		if recover() == nil {
			panic("map assignment did not panic")
		}
		fmt.Println("recovered")
	}()
	m := map[interface{}]int{}
	m[[]int{1}] = 1
}
