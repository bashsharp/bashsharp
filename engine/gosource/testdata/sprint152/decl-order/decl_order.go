package main

func F[T any](x T) T { return T(x) }

var Fi = F[I]

type I interface{ M() }

func G(x any) any { return x }

var n = 5

func main() {}
