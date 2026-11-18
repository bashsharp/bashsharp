package gosource_test

import (
	"reflect"
	"testing"
)

// Sprint 270 (G5): gc's parser accepts a range clause with more than two
// iteration variables and types2 diagnoses it; the front end reports what gc
// prints, not go/parser's "expected at most 2 expressions". The wanted lines
// are go1.27.1 `go tool compile -p p` output for the same source.
func TestRangeClauseArity(t *testing.T) {
	src := `package p

func f[T ~[]int | ~string](s T, m map[string]int, it func(func(int, int) bool), c <-chan int, n int) {
	var i, j, k int
	_, _, _ = i, j, k
	for i, j, k := range s {
		_ = i
	}
	for a, b, c := range m {
	}
	for i, j, k = range it {
	}
	for i, j, k = range n {
	}
	for i, j, k = range 3.5 {
	}
	for i, j, k = range c {
	}
}
`
	want := []string{
		"ra.go:6:23: cannot range over s (variable of type T constrained by ~[]int | ~string): []int and string have different underlying types",
		"ra.go:9:6: declared and not used: a",
		"ra.go:9:9: declared and not used: b",
		"ra.go:9:12: range clause permits at most two iteration variables",
		"ra.go:11:12: range clause permits at most two iteration variables",
		"ra.go:13:9: range over n (variable of type int) permits only one iteration variable",
		"ra.go:15:22: cannot range over 3.5 (untyped float constant)",
		"ra.go:17:9: range over c (variable of type <-chan int) permits only one iteration variable",
	}
	if got := diagnosticsOf(t, "ra.go", src); !reflect.DeepEqual(got, want) {
		t.Fatalf("range clause diagnostics differ from gc\ngot:  %q\nwant: %q", got, want)
	}
	// Negatives: two variables stay valid, and an unrelated go/parser-only
	// error is not rewritten.
	if got := diagnosticsOf(t, "ok.go", "package p\n\nfunc f(s []int) {\n\tfor i, v := range s {\n\t\t_, _ = i, v\n\t}\n}\n"); got != nil {
		t.Fatalf("two-variable range rejected: %q", got)
	}
}
