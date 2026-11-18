// Positive control: a struct naming another materialisable local type keeps
// both declarations and still crosses the dependency boundary.
package main

import "fmt"

type inner struct {
	N int
}

type outer struct {
	Label string
	In    inner
}

func main() {
	o := outer{Label: "pair", In: inner{N: 8}}
	fmt.Println(o)
}
