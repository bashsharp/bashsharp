// Positive control: the same booleans in declarations and expressions were
// already supported.
package main

import "fmt"

func main() {
	flag := true
	var b bool = false
	fmt.Println(flag, b, flag && !b, flag == true)
	flag = !flag
	fmt.Println(flag)
}
