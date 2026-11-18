// A struct literal type is identified by its fields: names, types and
// embedding. An assertion, a type switch and an interface comparison tell
// struct{ x int } from struct{ y string }, struct{ x int64 } and
// struct{ int }.
package main

import "fmt"

func kind(v any) string {
	switch v.(type) {
	case struct{ y string }:
		return "y"
	case struct{ x int }:
		return "x"
	case struct{ int }:
		return "embedded"
	}
	return "other"
}

func main() {
	var v any = struct{ x int }{1}
	_, ok1 := v.(struct{ y string })
	_, ok2 := v.(struct{ x int })
	_, ok3 := v.(struct{ x int64 })
	fmt.Println(ok1, ok2, ok3)
	var w any = struct{ int }{0}
	_, ok4 := w.(struct{ int })
	_, ok5 := w.(struct{ x int })
	fmt.Println(ok4, ok5)
	fmt.Println(kind(v), kind(w), kind(struct{ z bool }{}))
	fmt.Println(v == any(struct{ x int }{1}), v == any(struct{ x int }{2}), v == w)
	var e any = struct{}{}
	fmt.Println(e == any(struct{}{}), e == v)
}
