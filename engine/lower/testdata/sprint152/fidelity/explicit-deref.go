package main

type Pair struct{ p1, p2 *int }
type Box struct{ pair *Pair }

func F(b *Box, p *int, q **int) {
	b.pair.p1 = b.pair.p2
	*p = **q
}

func main() {}
