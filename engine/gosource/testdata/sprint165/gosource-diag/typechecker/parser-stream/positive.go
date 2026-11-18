package p

func T[P any](x P) P { return x }

var _, _ = 0x1, 0x2

var _ = T(1)
