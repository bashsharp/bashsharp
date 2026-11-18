// Reduced from fixedbugs/bug260.go — array element address stride.
package main

import (
	"fmt"
	"strconv"
)

type T1 struct{ x uint8 }

func main() {
	var b [10]T1
	a0, _ := strconv.ParseUint(fmt.Sprintf("%p", &b[0])[2:], 16, 64)
	a1, _ := strconv.ParseUint(fmt.Sprintf("%p", &b[1])[2:], 16, 64)
	if a1 != a0+1 {
		fmt.Println("FAIL", a1-a0)
	} else {
		fmt.Println("OK")
	}
}
