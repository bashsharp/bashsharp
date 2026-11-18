package main

import "fmt"

type S struct{ Word string }

func (s S) String() string { return "<" + s.Word + ">" }

func main() {
	fmt.Println(S{"ok"})
}
