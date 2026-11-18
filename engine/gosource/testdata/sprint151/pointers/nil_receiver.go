package main

import "fmt"

type sentinel struct{}

type wrapper struct {
	*sentinel
}

func (s *sentinel) nilReceiver() bool {
	return s == nil
}

func main() {
	var s *sentinel
	var w wrapper
	fmt.Println(s.nilReceiver(), w.nilReceiver())
}
