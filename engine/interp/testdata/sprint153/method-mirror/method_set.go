package main

import (
	"fmt"
	"reflect"
)

type M int

func (m M) UniqueMethodName() { fmt.Println("called", int(m)) }

func (m M) Add(n int) int { return int(m) + n }

func (m M) pair() (int, string) { return int(m), "pair" }

func main() {
	t := reflect.TypeOf(M(7))
	fmt.Println(t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		fmt.Println(t.Method(i).Name)
	}
	m, ok := t.MethodByName("UniqueMethodName")
	fmt.Println(m.Name, ok)
}
