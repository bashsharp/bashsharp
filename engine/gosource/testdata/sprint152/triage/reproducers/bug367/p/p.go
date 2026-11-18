package p

type S struct{}

func (S) private() {}

type I interface{ private() }

func F(v I) { v.private() }
