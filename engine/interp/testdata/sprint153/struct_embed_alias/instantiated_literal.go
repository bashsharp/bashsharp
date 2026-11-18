// Positive control: the same embeds filled with the instantiated spelling
// `E[int]` the fields are declared with. No alias is involved, so this form was
// always accepted.
package main

import "fmt"

type E[T any] struct{ v T }

type S struct {
	E[int]
	p *E[int]
}

func main() {
	s := S{E[int]{3}, &E[int]{4}}
	fmt.Println(s.E.v, s.p.v)
}
