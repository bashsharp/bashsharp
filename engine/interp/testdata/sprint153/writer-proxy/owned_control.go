package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintf(os.Stdout, "out=%d\n", 1)
	fmt.Fprintln(os.Stderr, "err=2")
}
