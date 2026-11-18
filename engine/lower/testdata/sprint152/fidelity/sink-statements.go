package main

func F(n int) []int {
	var r []int
	i := 0
	for ; i < n; i++ {
		r = append(r, i)
	}
	return r
}

func main() {}
