package main

import (
	"bytes"
	"fmt"
)

func main() {
	b := new(bytes.Buffer)
	b.WriteString("head ")
	fmt.Fprintf(b, "x=%d\n", 7)
	fmt.Print(b.String())
}
