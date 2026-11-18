// Mechanism: range assignment target that is a pointer dereference (*p).
package main

import "fmt"

func main() {
	n := 0
	p := &n
	for *p = range 4 {
	}
	fmt.Println(*p)
}
