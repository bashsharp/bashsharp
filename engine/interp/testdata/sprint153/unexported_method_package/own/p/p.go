package p

type S struct{}

func (S) Public() string { return "S.Public" }

type I interface{ Public() string }

func F(v I) string { return v.Public() }
