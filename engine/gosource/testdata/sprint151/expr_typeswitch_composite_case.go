// Mechanism: expression *ast.ArrayType / *ast.MapType / *ast.FuncType — a type
// switch arm that lists a composite (unnamed) type. Switch arms convert each
// case through c.expr, which has no type-expression cases. The guard binds a
// name so the bare-guard mechanism (exprstmt_bare_typeswitch.go) stays out.
package main

import "fmt"

func describe(v any) string {
	switch x := v.(type) {
	case []int:
		return fmt.Sprint("slice", len(x))
	case map[string]int:
		return fmt.Sprint("map", len(x))
	case func():
		return "func"
	}
	return "other"
}

func main() {
	fmt.Println(describe([]int{1}), describe(map[string]int{}), describe(func() {}), describe(3))
}
