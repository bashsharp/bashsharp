package main

import "fmt"

func main() {
	defer func() {
		if recover() == "boom" {
			fmt.Println("recovered")
		}
	}()
	panic("boom")
}
