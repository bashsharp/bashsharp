// Positive control: a read-only byte comparison that was already supported
// before the reconciled-buffer mechanism.
package main

import (
	"bytes"
	"fmt"
)

func main() {
	fmt.Println(bytes.Equal([]byte("ab"), []byte("ab")))
}
