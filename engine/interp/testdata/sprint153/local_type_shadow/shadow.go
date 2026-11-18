// A type declared inside a function body is scoped to that body: two
// functions may each declare `type s`, a body may run its declaration again
// on every call, and a local type shadows a package-level type of the same
// name only while its function runs.
package main

import "fmt"

type s struct{ top string }

type S1 struct{}

func (S1) Fix() string {
	type s struct {
		f int
	}
	v := s{3}
	return fmt.Sprint("f=", v.f)
}

type S2 struct{}

func (S2) Fix() string {
	type s struct {
		g bool
	}
	v := s{true}
	return fmt.Sprint("g=", v.g)
}

func repeat(n int) int {
	type s struct{ n int }
	return s{n}.n * 2
}

func main() {
	fmt.Println(S1{}.Fix(), S2{}.Fix())
	fmt.Println(repeat(1), repeat(2), repeat(3))
	fmt.Println(s{"pkg"}.top)
	fmt.Println(S1{}.Fix(), s{"again"}.top)
}
