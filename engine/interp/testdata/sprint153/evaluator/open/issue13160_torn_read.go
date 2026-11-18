// Reduced from fixedbugs/issue13160.go: a writer and a reader race on one
// slot of a []*int. Go tolerates the race on a word-sized pointer; the
// interpreter's slots are two-word interfaces, and the object validator
// (expand.NewObject -> json.Marshal) panics on a half-written one. Fails
// intermittently; 200 iterations reproduce it reliably here.
package main

import "fmt"

func main() {
	p := 2
	ptrs := make([]*int, p)
	for i := 0; i < p; i++ {
		ptrs[i] = new(int)
	}
	collider := make([]*int, p)
	done := make(chan struct{}, 2*p)
	for i := 0; i < p; i++ {
		i := i
		go func() {
			for j := 0; j < 200; j++ {
				copy(collider[i:i+1], ptrs[i:i+1])
				r := collider[i : i+1]
				for k := range r {
					r[k] = nil
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < p; i++ {
		i := i
		go func() {
			for j := 0; j < 200; j++ {
				var ptr [1]*int
				copy(ptr[:], collider[i:i+1])
				if ptr[0] != nil && ptr[0] != ptrs[i] {
					panic("bad")
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 2*p; i++ {
		<-done
	}
	fmt.Println("ok")
}
