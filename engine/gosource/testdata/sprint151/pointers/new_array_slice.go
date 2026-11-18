package main

import "fmt"

func main() {
	values := new([1]*int)[:]
	values[0] = nil
	fmt.Println(len(values), values[0] == nil)
}
