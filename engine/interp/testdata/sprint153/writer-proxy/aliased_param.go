package main

import (
	"bytes"
	"fmt"
)

func emit(w *bytes.Buffer, label string, n int) {
	fmt.Fprintf(w, "%s=%d\n", label, n)
}

func main() {
	var b bytes.Buffer
	emit(&b, "n", 42)
	emit(&b, "m", 7)
	fmt.Print(b.String())
}
