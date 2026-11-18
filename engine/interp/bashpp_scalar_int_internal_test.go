package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPScalarIntPlan(t *testing.T) {
	source := `package main
func pair(x int) (int, int) {
	if x < 3 { return 0, x }
	a, b := pair(x - 2)
	c, d := pair(x - 1)
	return a + d, b + c
}
func fallback(x int) int { return x / 2 }
func main() { a, b := pair(12); println(a, b, fallback(8)) }
`
	program, err := gosource.Parse(strings.NewReader(source), "scalar_int.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("run: %v; output=%q", err, out.String())
	}
	if got, want := out.String(), "55 178 4\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if fn := runner.bashPPFuncs["pair"]; fn == nil || fn.scalarInt == nil {
		t.Fatalf("pure recursive int function did not acquire scalar plan")
	}
	if fn := runner.bashPPFuncs["fallback"]; fn == nil || fn.scalarInt != nil {
		t.Fatalf("unsupported division function acquired scalar plan")
	}
}
