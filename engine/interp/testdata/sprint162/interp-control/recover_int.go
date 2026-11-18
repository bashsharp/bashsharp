package main

import "fmt"

func main() {
	defer func() {
		if recover() == 42 {
			fmt.Println("typed")
		}
	}()
	panic(42)
}
