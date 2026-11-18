package lib

type Sym struct{ N int }

func Compare(a, b *Sym) int { return a.N - b.N }
