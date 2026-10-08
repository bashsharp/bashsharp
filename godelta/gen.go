//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/bashsharp/bashsharp/godelta"
)

func main() {
	if err := os.WriteFile("../docs/go-delta.md", []byte(godelta.Markdown()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
