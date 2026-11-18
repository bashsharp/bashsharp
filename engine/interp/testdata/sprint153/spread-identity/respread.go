package main

import "fmt"

func emit(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func main() {
	fmt.Print(emit("%d %t %s %g\n", 7, true, "ok", 1.5))
	fmt.Printf("%d-%s\n", 1, "x")
}
