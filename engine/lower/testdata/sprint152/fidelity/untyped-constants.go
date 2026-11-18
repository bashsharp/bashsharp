package main

func F(s string, f float64) []byte {
	i := 0
	if f != 0 {
		i = 3
	}
	println("i=", i, 1.5)
	return []byte("foo")[i:]
}

func main() {}
