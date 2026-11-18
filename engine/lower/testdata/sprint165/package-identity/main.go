package main

import (
	"fmt"

	"example/packageidentity/a"
)

func main() {
	var value any = a.Item{}
	fmt.Printf("%T\n", value)
}
