//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestGoSourceBlankTypeDeclarations(t *testing.T) {
	for name, source := range map[string]string{
		"single": `package main
import "fmt"
type _ struct { Value int }
func main() { fmt.Println("ok") }
`,
		"alias": `package main
import "fmt"
type Named struct { Value int }
type _ = Named
func main() { fmt.Println(Named{7}) }
`,
		"repeated_and_nested": `package main
import ("fmt"; "reflect")
type _ struct { Inner struct { Value int }; _ int }
type _ int
type Named struct { _ int; Value int }
var calls int
func touch() int { calls++; return calls }
var _ = touch()
func main() {
 type _ = string
 _ = touch()
 v := Named{touch(), 9}
 a := struct { Value int }{11}
 fmt.Println(calls, v.Value, reflect.ValueOf(v).NumField(), reflect.ValueOf(v).Field(0).Int(), a)
}
`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

func TestGoSourceBlankTypeNamesRemainUnusable(t *testing.T) {
	for _, source := range []string{
		"package main\ntype _ int\nfunc main(){var x _;_ = x}",
		"package main\ntype Named int\ntype Named string\nfunc main(){}",
	} {
		if _, err := gosource.Parse(strings.NewReader(source), "invalid.go", gosource.Options{RunMain: true}); err == nil {
			t.Fatalf("accepted invalid declaration: %s", source)
		}
	}
}

func TestGoSourceBlankMethods(t *testing.T) {
	differGoSource(t, `package main
import ("fmt"; "reflect")
type T int
func (T) _() { panic("blank value method called") }
func (*T) _() { panic("blank pointer method called") }
func (T) Value() int { return 7 }
func main() {
 var t T
 fmt.Println(t.Value(), reflect.TypeOf(t).NumMethod(), reflect.TypeOf(&t).NumMethod())
}
`, nil, "")
}

func TestGoSourceBlankMethodChecks(t *testing.T) {
	for _, source := range []string{
		"package main\ntype T int\nfunc(T) M(){}\nfunc(*T) M(){}\nfunc main(){}",
		"package main\nfunc(Missing) _(){}\nfunc main(){}",
		"package main\ntype P *int\nfunc(P) _(){}\nfunc main(){}",
		"package main\ntype T int\nfunc(T) _(){var x int = \"wrong\";_ = x}\nfunc main(){}",
	} {
		if _, err := gosource.Parse(strings.NewReader(source), "invalid.go", gosource.Options{RunMain: true}); err == nil {
			t.Fatalf("accepted invalid method: %s", source)
		}
	}
}

func TestGoSourceDeclaredFunctionAssignment(t *testing.T) {
	differGoSource(t, `package main
import "fmt"
var fp = func(_ int, y int) int { return y }
func init() { fp = add }
func add(x, y int) int { return x+y }
func main() {
 fmt.Println(fp(2,3))
 a,b := fp,fp
 a,b = add,(add)
 fmt.Println(a(1,2),b(3,4))
 add := func(x,y int) int { return x*y }
 fp = add
 fmt.Println(fp(2,3))
}
`, nil, "")
}

func TestGoSourceFunctionAssignmentChecks(t *testing.T) {
	for _, source := range []string{
		"package main\nfunc named(int){}\nfunc main(){var f func(string);f=named;_ = f}",
		"package main\nfunc main(){var f func();f=missing;_ = f}",
	} {
		if _, err := gosource.Parse(strings.NewReader(source), "invalid.go", gosource.Options{RunMain: true}); err == nil {
			t.Fatalf("accepted invalid function assignment: %s", source)
		}
	}
}
