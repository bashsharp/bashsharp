package main

import (
	"errors"
	"fmt"
)

const size = 100

var sink any

func keep(err *error) { sink = err }
func a(n int) (res int, err error) {
	defer keep(&err)
	if n < 0 {
		return 0, errors.New("negative")
	}
	if n <= 1 {
		return n, nil
	}
	res, err = a(n - 1)
	return res + 1, err
}
func main() {
	x := 0
	var e error
	for i := 0; i < size; i++ {
		x, e = a(i)
	}
	fmt.Println(x, e == nil)
}
