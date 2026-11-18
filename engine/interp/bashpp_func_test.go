package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func runBashPPFunc(t *testing.T, src string) string {
	t.Helper()
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	return out.String()
}

func TestBashPPFuncRuntimeProbe(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"typed params and result", "func pick(a int, b string) int {\n return a\n}\nx := pick(7, hello)\necho \"x=$x\"\n", "x=7\n"},
		{"multiple results", "func pair(a, b int) (x, y int) {\n x=a\n y=b\n return\n}\nx, y := pair(1, 2)\necho \"$x:$y\"\n", "1:2\n"},
		{"named result", "func named() (n int) {\n n=5\n return\n}\nx := named()\necho \"x=$x\"\n", "x=5\n"},
		{"closure observes live cell", "var prefix = 1\nfunc f() int {\n return prefix\n}\nprefix=2\nx := f()\necho \"x=$x\"\n", "x=2\n"},
		{"recursion", "func down(n int) {\n if [ \"$n\" -gt 0 ]; then\n  echo $n\n  down($((n-1)))\n fi\n}\ndown(3)\n", "3\n2\n1\n"},
		{"defer order", "func g(v) {\n echo \"g:$v\"\n}\nfunc f() {\n defer g(1)\n defer g(2)\n}\nf()\n", "g:2\ng:1\n"},
		{"defer captures and propagates failure", "func bad() {\n return 7\n}\nfunc f() int {\n n=1\n defer bad()\n n=2\n return n\n}\nx := f()\necho \"x=$x status=$?\"\n", "x=2 status=7\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qt.Assert(t, qt.Equals(runBashPPFunc(t, tc.src), tc.want))
		})
	}
}

func TestBashPPGenericFuncRuntime(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"explicit scalar substitution",
			"func id[T any](v T) T {\n return v\n}\nx := id[int](7)\necho \"x=$x\"\n",
			"x=7\n",
		},
		{
			"ordinary scalar inference",
			"func id[T any](v T) T {\n return v\n}\nx := id(hello)\necho \"x=$x\"\n",
			"x=hello\n",
		},
		{
			"composite inference",
			"func sink[T any](v T) {\n echo ok\n}\nvar xs []int = []int{1, 2}\nsink(xs)\n",
			"ok\n",
		},
		{
			"pointer explicit argument",
			"type Box struct { N int }\nfunc sink[T any](v T) {\n echo ok\n}\nfunc main() {\n p := new(Box)\n sink[*Box](p)\n}\nmain()\n",
			"ok\n",
		},
		{
			"comparable constraint",
			"func same[T comparable](a, b T) T {\n return a\n}\nx := same[int](1, 2)\necho \"$x\"\n",
			"1\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qt.Assert(t, qt.Equals(runBashPPFunc(t, tc.src), tc.want))
		})
	}
}

func TestBashPPGenericFuncDiagnostics(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"too many type arguments",
			"func id[T any](v T) T {\n return v\n}\nid[int, string](1)\n",
			"BASHPP-EGENERIC-ARITY: id expects 1 type argument(s); got 2\n",
		},
		{
			"cannot infer",
			"func zero[T any]() T {\n return 0\n}\nzero()\n",
			"BASHPP-EGENERIC-INFER: cannot infer type arguments for zero\n",
		},
		{
			"constraint",
			"func bad[T comparable](v T) {}\nvar xs []int = []int{1}\nbad(xs)\n",
			"BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for T in bad\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestBashPPGenericConstraintDependentTypeSet(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"reject outside substituted type set",
			"func accept[S ~[]E, E comparable](s S, v E) { echo accepted }\nvar values []int = []int{1}\nvar needle string = \"1\"\naccept(values, needle)\n",
			"BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for S in accept\n",
		},
		{
			"accept substituted slice approximation",
			"func accept[S ~[]E, E comparable](s S, v E) { echo accepted }\nvar values []string = []string{\"one\"}\naccept(values, \"one\")\n",
			"accepted\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}
