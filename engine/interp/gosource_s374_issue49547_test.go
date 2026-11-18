//go:build full

package interp_test

import (
	"strings"
	"testing"
)

func TestGoSourceS374Issue49547TypeVerb(t *testing.T) {
	out, stderr, err := runGoSource(t, "s374-issue49547", `package main
import ("fmt"; "reflect")
type foo int
type F[T any] struct{}
type G[T any] struct{}
func main() {
 fmt.Printf("%T\n", F[foo]{})
 fmt.Printf("%T\n", F[G[foo]]{})
 fmt.Printf("%T\n", F[*foo]{})
 fmt.Println(reflect.TypeOf(F[foo]{}).String())
 fmt.Println(reflect.TypeOf(F[G[foo]]{}).String())
 fmt.Println(reflect.TypeOf(F[*foo]{}).String())
}
`)
	want := strings.Join([]string{
		"main.F[main.foo]",
		"main.F[main.G[main.foo]]",
		"main.F[*main.foo]",
		"main.F[main.foo]",
		"main.F[main.G[main.foo]]",
		"main.F[*main.foo]",
	}, "\n")
	if err != nil || stderr != "" || strings.TrimSpace(out) != want {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
