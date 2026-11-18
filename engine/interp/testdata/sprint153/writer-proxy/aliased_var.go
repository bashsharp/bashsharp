package main

import (
	"bytes"
	"fmt"
)

func main() {
	var b bytes.Buffer
	fmt.Fprintf(&b, "x=%d ", 7)
	fmt.Fprint(&b, "mid")
	fmt.Fprintln(&b, " end")
	fmt.Print(b.String())
}
