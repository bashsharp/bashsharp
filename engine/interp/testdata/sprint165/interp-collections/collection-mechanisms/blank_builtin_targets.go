// run

package main

import "fmt"

func main() {
	_ = make([]int, 2)
	s := []int{1, 2}
	_ = append(s, 3)
	dst := []int{0, 0}
	_ = copy(dst, s)
	_ = len(dst)
	fmt.Println("blank", dst[0], dst[1])
}
