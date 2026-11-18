// A parenthesized `new` builtin must lower like the bare form: `(new)(T)`
// allocates a *T. Out-of-corpus reduction of fixedbugs/issue63436.go, which
// the converter rejected with LOWER-EUNDEFINED: undefined: new because the
// new-builtin detectors only matched an unparenthesized *ast.Ident callee and
// the parenthesized callee fell through to the generic call path.
package main

import "fmt"

func main() {
	p := (new)(int)
	*p = 42
	q := (new)([]string)
	*q = append(*q, "x")
	fmt.Println(*p, len(*q), (*q)[0])
}
