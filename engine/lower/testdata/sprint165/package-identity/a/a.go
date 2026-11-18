package a

import "fmt"

var _ = announceInit()

func announceInit() int {
	fmt.Println("a.init")
	return 0
}

type Item struct{}
