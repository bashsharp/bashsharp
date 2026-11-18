package main

type T struct{ p *int }

func F(t *T) int {
	return *t.p
}

func main() {}
