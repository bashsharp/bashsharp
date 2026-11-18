package main

func main() {
	defer recover()
	panic("must escape")
}
