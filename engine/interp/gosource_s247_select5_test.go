//go:build full

package interp_test

// Sprint: #247; Story: #673; Story-ID: f24307569417

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestS247OriginalSelect5 runs the assigned upstream generator byte-for-byte.
// Its templates repeatedly call methods on an origin-bearing *arg whose method
// bodies mutate the same pointee that Execute continues to inspect.
func TestS247OriginalSelect5(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "chan", "select5.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != "ad1913e484be13bffbda7c07522f64ec7fd9ffbe30288ca39e2f6a3781d79d98" {
		t.Fatalf("select5.go source digest = %s; update the pinned output only after review", got)
	}
	out, stderr, err := runGoSource(t, "select5", string(source))
	if err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(out))); got != "b6d4ffdf8ec7bd7f697424858c7e03b368484a00d2941b29748f8426fa08d8ea" {
		t.Fatalf("generated output digest = %s, len=%d", got, len(out))
	}
	// select5 is a runoutput root: the corpus also runs the generated
	// program. Generating it correctly is not the root passing.
	genOut, genErr, err := runGoSource(t, "select5-generated", out)
	if err != nil || genErr != "" || genOut != "" {
		t.Fatalf("generated program: run=%v stdout=%q stderr=%q", err, genOut, genErr)
	}
}

// A select receive may assign to a map element. The element is not
// addressable; the spec evaluates the left-hand side only once the case is
// chosen, after the channel operand, and then assigns as `m[k] = v` does.
func TestS247SelectReceiveIntoMapElement(t *testing.T) {
	const source = `package main
import "fmt"
var order []string
func key(k int) int { order = append(order, fmt.Sprint("key", k)); return k }
func ch(c chan int) chan int { order = append(order, "chan"); return c }
func main() {
	c := make(chan int, 2)
	m := map[int]int{}
	var mi map[string]interface{} = map[string]interface{}{}
	c <- 7
	select {
	case m[key(1)] = <-ch(c):
	}
	c <- 8
	select {
	case mi["x"] = <-c:
	default:
		panic("default")
	}
	select {
	case m[key(2)] = <-c:
		panic("empty channel selected")
	default:
	}
	fmt.Println(m, mi["x"], order)
}`
	out, stderr, err := runGoSource(t, "select-map-element", source)
	if err != nil || stderr != "" || out != "map[1:7] 8 [chan key1]\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A function-free, invocation-free template may inspect an origin pointer and
// call its niladic methods synchronously. Each callback refreshes the worker's
// pointee, and the final pointer update writes dependency-observed state back.
func TestS247TemplateOriginalPointerCoherence(t *testing.T) {
	const source = `package main
import ("os"; "fmt"; "text/template")
type state struct { Values []string; N int }
func (s *state) Next() string { v:=s.Values[s.N]; s.N++; return v }
func main() {
	t:=template.Must(template.New("x").Parse("{{.Next}}/{{.Next}}"))
	s:=&state{Values:[]string{"a","b"}}
	if err:=t.Execute(os.Stdout,s); err!=nil { panic(err) }
	fmt.Printf(" n=%d\n",s.N)
}`
	out, stderr, err := runGoSource(t, "template-origin", source)
	if err != nil || stderr != "" || out != "a/b n=2\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS247TemplateOriginalPointerRefusals(t *testing.T) {
	const copied = `package main
import ("os"; "text/template")
type state struct { Values []string }
func (s state) First() string { return s.Values[0] }
func main(){ t:=template.Must(template.New("x").Parse("{{.First}}")); s:=state{Values:[]string{"x"}}; _=t.Execute(os.Stdout,s) }`
	_, stderr, err := runGoSource(t, "template-copied", copied)
	if err == nil || !strings.Contains(err.Error()+stderr, "original callback with copied slice references is unsupported") {
		t.Fatalf("copied reference was not refused: err=%v stderr=%q", err, stderr)
	}

	const invocation = `package main
import ("os"; "text/template")
type state struct { Values []string }
func (s *state) First() string { return s.Values[0] }
func main(){ t:=template.Must(template.New("x").Parse("{{define \"inner\"}}{{.First}}{{end}}{{template \"inner\" .}}")); s:=&state{Values:[]string{"x"}}; _=t.Execute(os.Stdout,s) }`
	_, stderr, err = runGoSource(t, "template-invocation", invocation)
	if err == nil || !strings.Contains(err.Error()+stderr, "dependency mutation of interpreter-owned references is unsupported") {
		t.Fatalf("template invocation was not refused: err=%v stderr=%q", err, stderr)
	}
}
