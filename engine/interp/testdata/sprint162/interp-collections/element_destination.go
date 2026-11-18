package main

type record struct {
	name  string
	value float64
}

func main() {
	name := "ok"
	value := 1.25
	_ = []record{{name, value}}
}
