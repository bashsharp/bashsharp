package main

import "fmt"

// Recovering a panic also discards every older panic the recovering frame
// (or a deeper one) raised while it was already unwinding — a `defer
// panic(v)` that replaced the first — so the frame returns normally. A panic
// an outer frame is unwinding is untouched, and the newest value is the one
// recover yields.
func main() {
	defer func() {
		fmt.Println("main recover:", recover())
	}()
	func() {
		defer func() {
			defer func() {
				fmt.Println("inner recover:", recover())
			}()
			defer panic(3)
			panic(2)
		}()
		defer func() {
			fmt.Println("mid recover:", recover())
		}()
		panic(1)
	}()
	fmt.Println("first block returned normally")

	// An unrecovered replacement propagates: the deferred call that panics
	// while 4 unwinds hands 5 to the outer recover, and 4 is gone.
	func() {
		defer func() {
			fmt.Println("sees:", recover())
			panic(6)
		}()
		func() {
			defer func() {
				defer panic(5)
			}()
			panic(4)
		}()
	}()
	fmt.Println("unreachable")
}
