package main

import (
	f "fmt"
	_ "image/gif"
	"unicode/utf8"
)

func main() {
	f.Println(utf8.RuneCountInString("gopher"))
}
