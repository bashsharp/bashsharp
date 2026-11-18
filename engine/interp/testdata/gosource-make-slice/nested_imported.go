package main

import (
	"fmt"
	"time"
)

type Local struct{ T time.Time }

func main() {
	a := make([]Local, 1)
	b := make([][2]time.Time, 1)
	fmt.Println(len(a), len(b), a[0].T.IsZero(), b[0][1].IsZero())
}
