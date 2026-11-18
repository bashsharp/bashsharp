package main

import "fmt"

// Without a recover the deferred call still runs its goroutine to completion,
// and the program then dies of the panic: status 2, and nothing after the
// fault runs.

func main() {
	defer func() {
		done := make(chan bool)
		go func() {
			fmt.Println("literal ran")
			done <- true
		}()
		<-done
	}()
	panic("boom")
}
