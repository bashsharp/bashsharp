package main

func Zero[T any]() T

func main() {
	println(Zero[int]())
}
