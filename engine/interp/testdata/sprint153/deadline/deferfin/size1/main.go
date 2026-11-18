package main

import "fmt"

const size = 1

func work() (n int) {
	defer func() { n++ }()
	v := ""
	defer func() {
		if v != "" {
			panic("bad")
		}
		n++
	}()
	return
}
func main() {
	total := 0
	for i := 0; i < size; i++ {
		total += work()
	}
	fmt.Println(total)
}
