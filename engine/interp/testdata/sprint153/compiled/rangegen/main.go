package main

func values(yield func(int) bool) {
	for i := 0; i < 100000; i++ {
		if !yield(i) {
			return
		}
	}
}

func main() {
	total := 0
	for n := range values {
		total += n
	}
	if total == 0 {
		panic("range-over-func did not run")
	}
}
