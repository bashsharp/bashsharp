package main

import (
	"example.com/init/a"
)

func init() {
	println("main.init", a.X)
}

func main() {
	println("main", a.X)
}
