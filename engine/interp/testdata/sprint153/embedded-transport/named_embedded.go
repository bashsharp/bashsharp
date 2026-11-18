package main

import "fmt"

type Base struct{ N int }

func (b Base) Tag() string { return fmt.Sprintf("base-%d", b.N) }

type Wrap struct {
	Base
	Extra string
}

func main() {
	w := Wrap{Base{7}, "x"}
	fmt.Println(w)
	fmt.Println(w.N, w.Extra)
	fmt.Println(w.Tag())
}
