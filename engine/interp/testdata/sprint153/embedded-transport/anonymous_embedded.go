package main

import "fmt"

func main() {
	type inner struct{ A int }
	v := struct {
		inner
		B string
	}{inner{1}, "b"}
	fmt.Println(v)
}
