// The Sprint 151 M1 spike fixture: a two-package program whose dependency is
// supplied only through the explicit package map. The relative import is the
// compiler's -D form: with ImportBase "test" it resolves to the mapped path
// "test/a" and is refused, not looked up on disk, when the map lacks it.
package main

import (
	"fmt"

	"./a"
)

func main() { fmt.Println(a.Greeting("mapped")) }
