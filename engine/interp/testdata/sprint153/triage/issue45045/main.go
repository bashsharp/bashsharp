// Reduced from fixedbugs/issue45045.go — runtime.SetFinalizer delivery after GC.
package main

import (
	"fmt"
	"runtime"
	"time"
)

func main() {
	c := make(chan string, 1)
	b := make([]byte, 16)
	runtime.SetFinalizer(&b[0], func(*byte) { c <- "final" })
	b = nil
	for i := 0; i < 10; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	select {
	case s := <-c:
		fmt.Println(s)
	default:
		fmt.Println("nofinal")
	}
}
