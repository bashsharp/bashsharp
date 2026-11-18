package main

import (
	"fmt"
	T "internal/runtime/sys"
)

func main() {
	fmt.Println(T.TrailingZeros64(8), T.Len64(255), T.OnesCount64(7))
}
