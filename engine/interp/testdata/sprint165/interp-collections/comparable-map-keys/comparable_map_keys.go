// run

package main

import (
	"fmt"
	"math"
)

type pair struct {
	n int
	s string
}

func main() {
	m := map[interface{}]int{}
	m[1] = 10
	m["1"] = 20
	m[pair{2, "x"}] = 30
	m[[2]int{3, 4}] = 40

	if len(m) != 4 || m[1] != 10 || m["1"] != 20 || m[pair{2, "x"}] != 30 || m[[2]int{3, 4}] != 40 {
		panic("typed map key identity was lost")
	}
	fmt.Println("identity")

	f := map[float64]int{}
	f[0] = 1
	f[math.Copysign(0, -1)] = 2
	nan := math.NaN()
	f[nan] = 3
	f[nan] = 4
	if len(f) != 3 || f[0] != 2 {
		panic("floating-point map equality is wrong")
	}
	if _, ok := f[nan]; ok {
		panic("NaN unexpectedly found itself")
	}
	fmt.Println("float")

	delete(m, pair{2, "x"})
	m[[2]int{3, 4}] += 2
	if len(m) != 3 || m[[2]int{3, 4}] != 42 {
		panic("typed delete or update failed")
	}
	fmt.Println("update")
}
