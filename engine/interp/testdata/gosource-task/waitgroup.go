package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var total atomic.Int64
	for i := 1; i <= 4; i++ {
		wg.Add(1)
		go func(n int) {
			total.Add(int64(n))
			wg.Done()
		}(i)
	}
	wg.Wait()
	fmt.Println("total", total.Load())
}
