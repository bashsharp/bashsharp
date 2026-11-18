package main

func g(x int) func() { return func() {} }

func F(a, b, c string, i, n int, s string) int {
	g(i)()
	x := len([]rune(s)) + len([]byte(a+b+c))
	return x*i + n - -i
}

func main() {}
