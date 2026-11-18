//go:build full

package interp_test

import (
	"strings"
	"testing"
)

func TestGoSourceSprint173BlankFields(t *testing.T) {
	differGoSource(t, `package main
import ("fmt"; "reflect")
type padded struct { _ int; Value int; _ string; Tail string }
var calls int
func number() int { calls++; return 17 }
func text() string { calls++; return "discard" }
func main() {
 p := padded{number(), 23, text(), "kept"}
 fmt.Println(calls, p.Value, p.Tail)
 v := reflect.ValueOf(p)
 fmt.Println(v.NumField(), v.Field(0).Int(), v.Field(2).String())
 fmt.Println(v.Field(1).Int(), v.Field(3).String())
 q := padded{Value: 31, Tail: "named"}
 fmt.Println(q)
 a := struct { First, _, Last int }{3, number(), 5}
 fmt.Println(calls, a.First, a.Last, reflect.ValueOf(a).Field(1).Int())
}
`, nil, "")
}

// The Go-source rule must not relax the existing Bash++ declaration check.
func TestSprint173ClassicBlankFieldDiagnostic(t *testing.T) {
	_, stderr, err := runBashSharpCall(t, `type Padding struct { _ int; _ string }
`)
	if err == nil || !strings.Contains(stderr, "BASHPP-ESTRUCT-FIELD-DUPLICATE:") {
		t.Fatalf("got error %v, stderr %q", err, stderr)
	}
}
