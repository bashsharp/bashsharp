package main

import (
	"fmt"
	"math"
)

func main() {
	values := []float64{math.NaN(), math.Inf(-1)}
	fmt.Println(math.IsNaN(values[0]), math.IsInf(values[1], -1))
}
