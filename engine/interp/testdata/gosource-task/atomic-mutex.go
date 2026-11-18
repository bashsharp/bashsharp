package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var total atomic.Int64
	var guarded atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			total.Add(int64(n))
			mu.Lock()
			guarded.Add(1)
			mu.Unlock()
			wg.Done()
		}(i)
	}
	wg.Wait()
	fmt.Println("total", total.Load())
	fmt.Println("guarded", guarded.Load())
}
