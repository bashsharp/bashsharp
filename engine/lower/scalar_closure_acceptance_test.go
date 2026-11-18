//go:build full

package lower_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These bounded sources exercise full Compile/build/artifact execution, not
// selected expression emitters. Establish the source interpreter's exact bytes
// first so an interpreter regression cannot be mistaken for compiler parity.
func TestScalarClosureAcceptance(t *testing.T) {
	cases := []struct{ name, source, want string }{
		// Preserve the literal bytes from syntax/TestBashPPTypedOperands/raw;
		// only add a print observation and invoke the enclosing typed function.
		{"raw-literal", "func main() {\n s := `héllo $HOME \\n \\\nworld`\n printf '%s' \"$s\"\n}\nmain()\n", "héllo $HOME \\n \\\nworld"},
		// Preserve syntax/TestBashPPTypedOperands/parenthesis and observe its value.
		{"leading-parenthesis", "func main() {\n n := (1 + 2) * 3\n printf '%s' \"$n\"\n}\nmain()\n", "9"},
		// Extend the same typed short-declaration carrier used by
		// interp/TestBashPPScalarShellOperatorExpressionsEvaluate to the remaining
		// unary and plain XOR spellings. These are new probes, not corpus extracts.
		{"logical-negation", "func main() {\n n := !false\n printf '%s' \"$n\"\n}\nmain()\n", "true"},
		{"bitwise-complement", "func main() {\n n := ^1\n printf '%s' \"$n\"\n}\nmain()\n", "-2"},
		{"plain-xor", "func main() {\n n := 6 ^ 3\n printf '%s' \"$n\"\n}\nmain()\n", "5"},
		// Exact public runtime source: interp/TestBashPPPositionedScalarReturnGuard.
		{"positioned-short-circuit-guards", `func positive(p *int) bool {
 return p != nil && *p > 0
}
func empty(xs []int) bool {
 return xs == nil || len(xs) == 0
}
func main() {
 var missing *int
 pointer := new(int)
 *pointer = 3
 no := positive(missing)
 yes := positive(pointer)
 var xs []int
 populated := []int{3}
 a := empty(xs)
 b := empty(populated)
 println(no, yes, a, b)
}
main()
`, "false true true false\n"},
		// New observable RHS probe: both skipped calls must remain silent, while
		// both selected calls execute exactly once and in source order.
		{"short-circuit-call-effects", `func touched(label string) bool {
 println(label)
 return true
}
func main() {
 a := false && touched("skipped-and")
 b := true || touched("skipped-or")
 c := true && touched("and")
 d := false || touched("or")
 println(a, b, c, d)
}
main()
`, "and\nor\nfalse true true true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr), interp.Env(expand.ListEnviron("PATH=/no-tools")))
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), parse(t, tc.source, "input.bpp"))
			if err != nil || stdout.String() != tc.want || stderr.Len() != 0 {
				t.Fatalf("source baseline stdout=%q stderr=%q status=%v; want stdout=%q status=0", stdout.String(), stderr.String(), err, tc.want)
			}
			out, errout := execute(t, compile(t, tc.source))
			if out != tc.want || errout != "" {
				t.Fatalf("artifact stdout=%q stderr=%q; want %q and empty", out, errout, tc.want)
			}
		})
	}
}
