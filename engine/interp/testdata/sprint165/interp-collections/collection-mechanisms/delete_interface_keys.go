// run

package main

import "fmt"

type key struct {
	n int
	s string
}

func main() {
	var k interface{} = key{1, "x"}
	m := map[interface{}]int{k: 7, [0]bool{}: 9}
	delete(m, k)
	delete(m, [0]bool{})
	delete(m, interface{}(key{2, "missing"}))
	fmt.Println("delete", len(m))
}
