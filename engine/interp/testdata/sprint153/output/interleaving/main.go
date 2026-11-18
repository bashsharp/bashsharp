package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("native-stdout-1")
	fmt.Fprintln(os.Stderr, "native-stderr-2")
	println("interpreted-stderr-3")
	fmt.Println("native-stdout-4")
	panic("final-panic")
}
