package main

import "fmt"

// A goroutine and its parent name ONE counter: Go closures capture free
// variables by reference. Synchronization is by channel so the program depends
// on nothing but the language.
func main() {
	counter := 0
	sem := make(chan bool, 1)
	done := make(chan bool)
	for i := 0; i < 8; i++ {
		go func() {
			sem <- true
			counter++
			<-sem
			done <- true
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	fmt.Println("counter", counter)
}
