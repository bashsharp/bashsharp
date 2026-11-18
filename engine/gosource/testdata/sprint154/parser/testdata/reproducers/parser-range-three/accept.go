package p

func f(s []int) {
	for a, b := range s {
		_, _ = a, b
	}
}
