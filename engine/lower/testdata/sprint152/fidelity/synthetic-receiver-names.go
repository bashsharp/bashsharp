package main

type M0 struct{ p *int }
type M1 struct{ p *int }

func (M0) M(int, string) {}

func (_ *M1) M(_ int) {}

func main() {}
