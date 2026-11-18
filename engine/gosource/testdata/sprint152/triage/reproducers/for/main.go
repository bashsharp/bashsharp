package main

func assert(got, want int, message string) {
	if got != want {
		print("assertion fail", message, "\n")
		panic(1)
	}
}

func main() {
	var i, sum int
	for {
		i++
		if i > 5 {
			break
		}
	}
	assert(i, 6, "break")
	for i := 0; i <= 10; i++ {
		sum += i
	}
	assert(sum, 55, "all three")
	sum = 0
	for i := 0; i <= 10; {
		sum += i
		i++
	}
	assert(sum, 55, "only two")
	sum = 0
	for sum < 100 {
		sum += 9
	}
	assert(sum, 108, "only one")
	sum = 0
	for i := 0; i <= 10; i++ {
		if i%2 == 0 {
			continue
		}
		sum += i
	}
	assert(sum, 25, "continue")
	i = 0
	for i = range [5]struct{}{} {
	}
	assert(i, 4, " incorrect index value after range loop")
}
