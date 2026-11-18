// A dependency call that rewrites its transported buffer in place: the
// changed elements are written back over the visible length, so the original
// storage and its aliases observe the fill.
package main

import (
	"fmt"
	"unicode/utf8"
)

func main() {
	buf := make([]byte, 4)
	n := utf8.EncodeRune(buf, '£')
	fmt.Println(n, buf[0], buf[1], buf[2], buf[3])
	alias := buf
	fmt.Println(alias[0], alias[1])
}
