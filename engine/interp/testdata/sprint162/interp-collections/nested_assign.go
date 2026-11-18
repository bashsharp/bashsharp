package main

import "fmt"

func main() {
	values := map[string][]string{"key": {"old"}}
	values["key"][0] = "new"
	array := new([2]int)
	array[1] = 7
	var direct [1]int
	direct[0] = 8
	fmt.Println(values["key"][0], array[1], direct[0])
}
