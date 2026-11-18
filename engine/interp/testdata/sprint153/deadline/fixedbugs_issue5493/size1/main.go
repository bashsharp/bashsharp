package main

import "fmt"

const size = 1

func run() int { f1 := func() int { return 1 }; f2 := func() int { return f1() }; return f2() }
func main() {
	total := 0
	for i := 0; i < size; i++ {
		total += run()
	}
	fmt.Println(total)
}
