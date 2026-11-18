package main

func main() {
	var values [4]int
	const width = len(values)
	if width != 4 {
		panic("wrong width")
	}
}
