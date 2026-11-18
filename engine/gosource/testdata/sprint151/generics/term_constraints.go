package main

import "fmt"

type MyByte byte

// A non-interface type written as a constraint is the one-term type set.
func text[T []MyByte](x T) string { return string(x) }

type S struct{ f int }

func deref[P *S](p P) int { return (*p).f }

// A union of unnamed types.
func first[T interface{ []int64 | [3]int64 }](x T) int64 { return x[0] }

func length[T interface{ []byte | string }](x T) int { return len(x) }

func main() {
	fmt.Println(text([]MyByte{'h', 'i'}))
	fmt.Println(deref(&S{f: 7}))
	fmt.Println(first([]int64{5, 6}), first([3]int64{8, 9, 10}))
	fmt.Println(length("abc"), length([]byte("de")))
}
