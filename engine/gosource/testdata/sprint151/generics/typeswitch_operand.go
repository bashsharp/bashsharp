package main

import "fmt"

type MyString string

// kind switches on a type parameter's dynamic type through any(x).
func kind[S string | []byte | int | MyString](x S) string {
	switch v := any(x).(type) {
	case string:
		return "string:" + v
	case []byte:
		return fmt.Sprintf("bytes:%d", len(v))
	case int:
		return fmt.Sprintf("int:%d", v+1)
	case MyString:
		return "MyString:" + string(v)
	}
	return "other"
}

// unbound switches without a binding and with the older spelling.
func unbound[T any](d T) string {
	switch interface{}(d).(type) {
	case bool:
		return "bool"
	case float64:
		return "float64"
	default:
		return "default"
	}
}

func boxed() any { return 7 }

func main() {
	fmt.Println(kind("hello"))
	fmt.Println(kind([]byte("hi")))
	fmt.Println(kind(41))
	fmt.Println(kind(MyString("ms")))
	fmt.Println(unbound(true), unbound(2.0), unbound("s"))
	// A call result as the operand.
	switch v := boxed().(type) {
	case int:
		fmt.Println("boxed int", v)
	default:
		fmt.Println("boxed other")
	}
	// Assertion on a converted type parameter.
	fmt.Println(assertInt(5), assertInt("no"))
}

func assertInt[T any](x T) bool {
	_, ok := any(x).(int)
	return ok
}
