package p

type S struct{}

func (*S) hidden() {}

type I interface{ hidden() }

func Use(I) {}
