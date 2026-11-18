// Outside-corpus reproducer: two dot imports and two blank imports in one
// file each bind no identifier, so the emitter must keep every path — the
// generated Go imports all four, exactly as the original does.
package main

import (
	_ "embed"
	. "fmt"
	. "strings"
	_ "unsafe"
)

func main() {
	Println(ToUpper("ok"))
}
