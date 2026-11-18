package main

import "fmt"

type record struct {
	text string
}

func main() {
	left := "ab"
	right := "cd"
	r := &record{text: "abc"}
	fmt.Println(r.text[0], (left + right)[2], r.text[1:])
}
