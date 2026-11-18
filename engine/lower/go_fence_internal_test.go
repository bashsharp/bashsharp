//go:build full

package lower_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoFenceTopLevelDeclarations(t *testing.T) {
	for _, tc := range []struct{ name, decl, body, want string }{
		{"struct method", `type Point struct { X int }
func (p *Point) Add(n int) { p.X += n }
func (p Point) Main() int { return p.X }`, `p := Point{2}; p.Add(3); fmt.Println(p.Main())`, "5\n"},
		{"interface", `type Value interface { Get() int }
type Number int
func (n Number) Get() int { return int(n) }`, `var v Value = Number(7); fmt.Println(v.Get())`, "7\n"},
		{"var const iota", `type State int
const ( Idle State = iota; Ready; Done )
var Current = Ready`, `Current++; fmt.Println(Idle, Ready, Current == Done)`, "0 1 true\n"},
		{"generic type", `type Box[T any] struct { Value T }
func (b Box[T]) Get() T { return b.Value }`, `fmt.Println(Box[string]{"boxed"}.Get())`, "boxed\n"},
		{"init", `var state string
func init() { state = "first" }
func init() { state += " second" }`, `fmt.Println(state)`, "first second\n"},
		{"stringer", `type Person struct { Name string; Age int }
func (p Person) String() string { return fmt.Sprintf("%s (%d)", p.Name, p.Age) }`, `var s fmt.Stringer = Person{"Arthur", 42}; fmt.Println(s)`, "Arthur (42)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "~~~go as g\nimport \"fmt\"\n" + tc.decl + "\nfunc Main() { " + tc.body + " }\n~~~\ng.Main()\n"
			t.Run("interpreted", func(t *testing.T) {
				var out, diagnostic bytes.Buffer
				runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic))
				if err != nil {
					t.Fatal(err)
				}
				if err := runner.Run(context.Background(), parse(t, source, "input.bpp")); err != nil {
					t.Fatalf("%v: %s", err, diagnostic.String())
				}
				if out.String() != tc.want {
					t.Fatalf("output = %q, want %q", out.String(), tc.want)
				}
			})
			if got := testPythonFenceInterpretedNativeParityAt(t, source, "input.bpp"); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoFenceModuleInternalFunctions(t *testing.T) {
	for _, tc := range []struct{ name, decl, call, reason string }{
		{"SlicesIndex", "func SlicesIndex[S ~[]E, E comparable](s S, v E) int { for i := range s { if v == s[i] { return i } }; return -1 }", "SlicesIndex([]string{\"a\", \"b\"}, \"b\")", "generic type parameters"},
		{"Variadic", "func Variadic(v ...int) int { return len(v) }", "Variadic(1)", "variadic"},
		{"Unsupported", "func Unsupported(v []int) int { return len(v) }", "Unsupported([]int{1})", "unsupported parameter type"},
		{"Result", "func Result() []int { return []int{1} }", "len(Result())", "unsupported result type"},
		{"Many", "func Many() (int, int, error) { return 1, 2, nil }", "func() int { v, _, _ := Many(); return v }()", "at most one value plus error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fence := "~~~go as g\n" + tc.decl + "\n"
			source := "~~~go as g\nimport \"fmt\"\n" + tc.decl + "\nfunc Main() { fmt.Println(" + tc.call + ") }\n~~~\ng.Main()\n"
			if got := testPythonFenceInterpretedNativeParityAt(t, source, "input.bpp"); got != "1\n" {
				t.Fatalf("output = %q", got)
			}
			// With no callable exports, both engines must still load the module.
			if got := testPythonFenceInterpretedNativeParityAt(t, fence+"~~~\necho loaded\n", "input.bpp"); got != "loaded\n" {
				t.Fatalf("output = %q", got)
			}
			for _, spelling := range []string{"g.", "", "v := g.", "v := "} {
				alias := strings.TrimPrefix(spelling, "v := ")
				source := fence + "~~~\n" + spelling + tc.name + "()\n"
				if alias == "" {
					source = strings.Replace(source, "go as g", "go", 1)
				}
				file := parse(t, source, "input.bpp")
				want := "Go function " + tc.name + " is not callable from Bash#: " + tc.reason
				var out, diagnostic bytes.Buffer
				runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic))
				if err != nil {
					t.Fatal(err)
				}
				err = runner.Run(context.Background(), file)
				if err == nil || !strings.Contains(diagnostic.String()+err.Error(), want) {
					t.Errorf("interpreted: err=%v diagnostic=%q, want %q", err, diagnostic.String(), want)
				}
				_, err = lower.Compile(file, lower.Options{})
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("lowered: err=%v, want %q", err, want)
				}
			}
		})
	}
}
