// A type alias is the type it names, so `Eint` and `E[int]` are interchangeable
// when a struct embeds an instantiated generic. The embedded field is declared
// `E[int]` and `*E[int]`, but the composite literal and the pointer value spell
// the alias `Eint`; matching the literal's type against the field's must see
// through the alias, for the value embed and through the pointer alike.
package main

import "fmt"

type E[T any] struct{ v T }

type Eint = E[int]

type S struct {
	E[int]
	p *E[int]
}

func main() {
	s := S{Eint{3}, &Eint{4}}
	fmt.Println(s.E.v, s.p.v)
}
