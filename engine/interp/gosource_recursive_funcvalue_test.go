//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// pipelineRejects parses source through the Go-source frontend and reports the
// diagnostic when the program is refused before it can run. A recursive
// func-typed assignment whose literal signature does not match its declared
// variable is a type error the pipeline must keep refusing; only the matching
// signature is the value the runtime is then allowed to accept.
func pipelineRejects(t *testing.T, source string) string {
	t.Helper()
	if _, err := gosource.Parse(strings.NewReader(source), "reject.go", gosource.Options{RunMain: true}); err != nil {
		return err.Error()
	}
	t.Fatalf("want the mismatched signature refused, but it parsed")
	return ""
}

// A named function-typed variable holding a recursive closure is the shape of
// Go by Example's recursion.go: `var fib func(n int) int` reassigned to a
// literal that calls `fib` by name. The literal's value has to arrive naming a
// func type, or the reassignment reads it as an untyped scalar and is rejected
// as "untyped result is not assignable to func(int)(int)".
//
// Negative-first: the identity a func literal now carries is its real
// signature, not a blanket pass, so a literal whose signature differs from the
// declared variable is still refused. Only after those rejections hold do the
// positive recursive shapes — including the closure crossing a call boundary as
// a named argument before its result crosses into native fmt — get to pass.
func TestGoSourceRecursiveFuncValueSignatureIdentity(t *testing.T) {
	t.Run("reject_arity_mismatch", func(t *testing.T) {
		got := pipelineRejects(t, `package main
func main() {
	var f func(n int) int
	f = func(a, b int) int { return a + b }
	_ = f
}
`)
		if !strings.Contains(got, "as func(n int) int value in assignment") {
			t.Fatalf("want a func-type assignment refusal for arity mismatch, got %q", got)
		}
	})

	t.Run("reject_result_mismatch", func(t *testing.T) {
		got := pipelineRejects(t, `package main
func main() {
	var f func(n int) int
	f = func(n int) string { return "x" }
	_ = f
}
`)
		if !strings.Contains(got, "as func(n int) int value in assignment") {
			t.Fatalf("want a func-type assignment refusal for result mismatch, got %q", got)
		}
	})
}

// The positive shapes exercise the value crossing the native boundary in the
// two ways the fixture and its family do: the recursive result printed by
// native fmt.Println, and the recursive closure itself handed to a callee as a
// named func-typed argument before that result is printed.
func TestGoSourceRecursiveFuncValueThreeModes(t *testing.T) {
	cases := map[string]string{
		"named_recursive_closure_result_to_native": `package main
import "fmt"
func main() {
	var fib func(n int) int
	fib = func(n int) int {
		if n < 2 {
			return n
		}
		return fib(n-1) + fib(n-2)
	}
	fmt.Println(fib(7))
}
`,
		"recursive_closure_as_named_argument": `package main
import "fmt"
func run(f func(int) int, n int) int { return f(n) }
func main() {
	var fact func(int) int
	fact = func(n int) int {
		if n == 0 {
			return 1
		}
		return n * fact(n-1)
	}
	fmt.Println(run(fact, 5))
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
