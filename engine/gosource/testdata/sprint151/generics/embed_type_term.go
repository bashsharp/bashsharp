package main

import "fmt"

// A constraint may embed a single type rather than a union.
type StringLike interface{ string }

type Bytes interface{ []byte }

type Named interface {
	~string
	Len() int
}

type Name string

func (n Name) Len() int { return len(n) }

func Length[S StringLike](s S) int { return len(s) }

func First[B Bytes](b B) byte { return b[0] }

func Twice[T Named](t T) int { return t.Len() * 2 }

type Pair struct{ a, b int }

func (p *Pair) Sum() int { return p.a + p.b }

// A pointer term beside a method: satisfied by *Pair exactly.
type PairPtr interface {
	*Pair
	Sum() int
}

func Total[P PairPtr](p P) int { return p.Sum() }

func main() {
	fmt.Println(Length("hello"))
	fmt.Println(First([]byte("xyz")))
	fmt.Println(Twice(Name("abc")))
	fmt.Println(Total(&Pair{3, 4}))
}
