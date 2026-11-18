package main

import (
	"./a"
	_ "unsafe"
)

//go:linkname alias test/a.Value
var alias string

func main() {
	if a.Value != "a" {
		panic("dependency init was skipped")
	}
	alias = "b"
	if a.Value != "b" {
		panic("linkname did not retain package identity")
	}
}
