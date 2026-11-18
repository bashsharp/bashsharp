package main

import "fmt"

const size = 5000

func main() {
	var sum int64
	for i := int64(1); i <= size; i++ {
		sum += (i * 1234567) / 97
		sum += (i * 7654321) / 31
	}
	fmt.Println(sum)
}
