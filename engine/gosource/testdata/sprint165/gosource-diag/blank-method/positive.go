package main

type T struct{}

func (T) _() {}

func (T) _() {}

func (*T) _() {}

func _() {}

func _() {}

func (T) named() {}

func main() {
	T{}.named()
}
