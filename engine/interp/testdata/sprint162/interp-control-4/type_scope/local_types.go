package main

import "fmt"

// A type declared inside a function is its own type: the same spelling in
// another function, or at package level, is a different type to an
// assertion, a type switch and an interface comparison.
var X interface{}

type T struct{}

func report(name string, f func()) {
	defer func() {
		fmt.Println(name, recover())
	}()
	f()
}

func set() {
	type T struct{}
	X = T{}
}

func assertLocal() {
	type T struct{}
	_ = X.(T)
	fmt.Println("assertLocal ok")
}

func assertPackage() {
	_ = X.(T)
	fmt.Println("assertPackage ok")
}

func setAndAssertLocal() {
	type T struct{}
	X = T{}
	_ = X.(T)
	fmt.Println("setAndAssertLocal ok")
}

func classify(v interface{}) string {
	type T struct{}
	switch v.(type) {
	case T:
		return "local T"
	default:
		return "other"
	}
}

func nested() {
	type T struct{}
	X = T{}
	{
		type T struct{}
		_, ok := X.(T)
		fmt.Println("inner sees outer local T:", ok)
	}
	_, ok := X.(T)
	fmt.Println("outer sees its own T:", ok)
}

func main() {
	set()
	report("assertLocal", assertLocal)
	report("assertPackage", assertPackage)
	report("setAndAssertLocal", setAndAssertLocal)
	X = T{}
	report("assertPackage", assertPackage)
	report("assertLocal", assertLocal)
	fmt.Println(classify(X))
	var a, b interface{}
	func() { type T struct{}; a = T{} }()
	func() { type T struct{}; b = T{} }()
	fmt.Println("different locals equal:", a == b)
	nested()
}
