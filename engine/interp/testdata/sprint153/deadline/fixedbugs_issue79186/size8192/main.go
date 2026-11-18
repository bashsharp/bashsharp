package main

import (
	"fmt"
	"sync"
)

type M struct {
	mu sync.RWMutex
	m  map[int]int
}

func (x *M) Get(k int) (int, bool) { x.mu.RLock(); v, ok := x.m[k]; x.mu.RUnlock(); return v, ok }
func (x *M) Set(k, v int)          { x.mu.Lock(); x.m[k] = v; x.mu.Unlock() }

const size = 8192

func main() {
	x := &M{m: map[int]int{}}
	for i := 0; i < size; i++ {
		k := i & 15
		if _, ok := x.Get(k); !ok || i&7 == 0 {
			x.Set(k, i)
		}
	}
	fmt.Println(len(x.m))
}
