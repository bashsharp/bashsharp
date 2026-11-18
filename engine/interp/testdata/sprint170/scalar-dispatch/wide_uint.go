package main

import "fmt"

type Word uint64
type WordAlias = Word

func word() WordAlias { return 1<<63 + 3 }

func main() {
	w := word()
	fmt.Printf("%T %d %T %d %q\n", w, w, int64(w), int64(w), string(w))
}
