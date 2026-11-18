// run

package main

import "fmt"

func main() {
	values := []int{3, 4, 5}
	fmt.Println(values[func() int { return 1 }()])
	values[func() int { return 0 }()] = 9
	fmt.Println(values[0])
	fmt.Println(values[:func() int { return 2 }()][1])
}
