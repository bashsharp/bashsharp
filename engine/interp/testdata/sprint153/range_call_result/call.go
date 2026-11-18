// range over a call's result: a slice, a map, a string and an integer
// returned by a function, at top level and nested inside another range.
package main

import "fmt"

func words() []string       { return []string{"q", "r"} }
func table() map[string]int { return map[string]int{"k": 1} }
func text() string          { return "ab" }
func limit() int            { return 2 }

var calls int

func counted() []int { calls++; return []int{7, 8} }

func main() {
	for _, w := range words() {
		fmt.Println(w + "?")
	}
	for i, w := range words() {
		fmt.Printf("%d %T\n", i, w)
	}
	for k, v := range table() {
		fmt.Println(k, v)
	}
	for i, r := range text() {
		fmt.Println(i, string(r))
	}
	for i := range limit() {
		fmt.Println("i", i)
	}
	for _, c := range "AB" {
		for _, w := range words() {
			fmt.Println(string(c) + w)
		}
	}
	for _, n := range counted() {
		fmt.Println(n)
	}
	fmt.Println("calls", calls)
}
