package main

import (
	"fmt"
	"reflect"
)

// A type parameter mentioned inside a computed callee — the receiver of a
// method call chain such as reflect.TypeOf(new(T)).Elem() — is bound to the
// frame's type argument before the expression is evaluated, exactly as it is
// in a declaration or a call argument.

type E struct{ n int }

func TypeString[T any]() string {
	return reflect.TypeOf(new(T)).Elem().String()
}

func PtrString[T any]() string {
	return reflect.TypeOf(new(T)).String()
}

func ElemKind[T any]() reflect.Kind {
	return reflect.TypeOf(new(T)).Elem().Kind()
}

func SliceOf[T any]() string {
	return reflect.TypeOf(make([]T, 0)).String()
}

func main() {
	fmt.Println(TypeString[int](), TypeString[E](), TypeString[[]string]())
	fmt.Println(PtrString[int](), PtrString[E]())
	fmt.Println(ElemKind[E](), ElemKind[string]())
	fmt.Println(SliceOf[E](), SliceOf[*int]())
	fmt.Println(reflect.TypeOf(new(E)).Elem().String())
}
