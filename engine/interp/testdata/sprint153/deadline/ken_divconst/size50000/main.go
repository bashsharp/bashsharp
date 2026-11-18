package main

import "fmt"

const size = 50000

func main() {
	var sum int64
	for i := int64(1); i <= size; i++ {
		sum += (i * 1234567) / 97
		sum += (i * 7654321) / 31
		sum += (i * 17) / 3
		sum += (i * 19) / 7
	}
	fmt.Println(sum)
}
