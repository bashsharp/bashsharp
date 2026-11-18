//go:build full

package interp_test

// Sprint: #290; Story: #940; Story-ID: 96bd92fe
//
// A concrete struct field stored in an interface — `m["k"] = s.F`,
// `var a any = s.F`, `[]any{s.F}` — is boxed with the field's declared
// type. The interface builder used to treat every selector as an
// interface-valued field and refused a concrete one with
// "selector is not an interface value".

import "testing"

func TestGoSourceConcreteSelectorIntoAny(t *testing.T) {
	const prelude = `package main
import "fmt"
type inner struct{ X int }
type T struct {
	F string
	N int64
	I inner
	P *inner
}
func main() {
	s := T{F: "v", N: 3, I: inner{X: 7}, P: &inner{X: 9}}
	_ = s
`
	for name, tc := range map[string]struct{ body, want string }{
		"map_index_assign": {`m := map[string]any{}
	m["f"] = s.F
	m["n"] = s.N
	fmt.Printf("%T %v %T %v\n", m["f"], m["f"], m["n"], m["n"])`, "string v int64 3\n"},
		"var_any": {`var a any = s.F
	var b any = s.I
	fmt.Printf("%T %v %T %v\n", a, a, b, b)`, "string v main.inner {7}\n"},
		"map_literal": {`m := map[string]any{"f": s.F, "n": s.N, "i": s.I}
	fmt.Println(m["f"], m["n"], m["i"])`, "v 3 {7}\n"},
		"slice_literal": {`l := []any{s.F, s.N, s.I.X}
	fmt.Printf("%T %T %T %v\n", l[0], l[1], l[2], l)`, "string int64 int [v 3 7]\n"},
		"pointer_field": {`var a any = s.P
	fmt.Printf("%T %v\n", a, a.(*inner).X)`, "*main.inner 9\n"},
		"intermediate_variable": {`x := s.F
	m := map[string]any{}
	m["f"] = x
	fmt.Println(m["f"])`, "v\n"},
	} {
		t.Run(name, func(t *testing.T) {
			out, errout, err := runGoSource(t, name, prelude+"\t"+tc.body+"\n}\n")
			if err != nil || errout != "" || out != tc.want {
				t.Fatalf("out=%q want %q stderr=%q err=%v", out, tc.want, errout, err)
			}
		})
	}
}
