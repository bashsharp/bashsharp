// Reduced from fixedbugs/issue13160.go — many goroutines racing on a shared pointer arena.
package main

import (
	"fmt"
	"runtime"
)

func main() {
	p := runtime.NumCPU()
	runtime.GOMAXPROCS(2 * p)
	collider := make([]*int, p)
	done := make(chan struct{}, p)
	for i := 0; i < p; i++ {
		i := i
		go func() {
			for j := 0; j < 100000; j++ {
				collider[i] = new(int)
				collider[i] = nil
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < p; i++ {
		<-done
	}
	fmt.Println("ok")
}
