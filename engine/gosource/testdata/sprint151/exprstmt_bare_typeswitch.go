// Mechanism: expression statement — a type switch with no binding. go/ast
// stores the guard `v.(type)` as an *ast.ExprStmt in TypeSwitchStmt.Assign,
// and the converter's c.one routes it through the generic statement path,
// which only admits calls and receives.
package main

import "fmt"

func kind(v any) string {
	switch v.(type) {
	case int:
		return "int"
	case string:
		return "string"
	}
	return "other"
}

func main() {
	fmt.Println(kind(1), kind("s"), kind(1.5))
}
