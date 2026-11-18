package main

import "fmt"

// Negative set: a deferred call that merely panics without being recovered
// still abandons the frame — the discard rule only fires on recover — and a
// recover in a nested (not directly deferred) call still recovers nothing.
func helper() interface{} { return recover() }

func main() {
	defer func() {
		fmt.Println("main recover:", recover())
	}()
	func() {
		defer func() {
			fmt.Println("nested recover:", helper())
		}()
		defer panic("second")
		panic("first")
	}()
	fmt.Println("unreachable")
}
