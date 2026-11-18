// Mechanism: expression *ast.IndexListExpr — a generic function explicitly
// instantiated with two or more type arguments and used as a value (not called).
package main

import "fmt"

func pair[K comparable, V any](k K, v V) string { return fmt.Sprint(k, "=", v) }

func main() {
	f := pair[string, int]
	fmt.Println(f("a", 1))
}
