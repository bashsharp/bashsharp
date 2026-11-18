//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// A value-receiver fmt protocol method (String/Format) that runs while fmt
// formats a slice it copied is no longer refused up front. This is the shape
// cmd/compile/internal/ir's TestHTMLWriter reaches: an IR node value carrying a
// slice field is handed to a Printf-style helper that forwards its named
// variadic slice into fmt, so the slice crosses the bridge nested inside a
// value the coherence path now admits, and fmt then looks up the value's
// String/Format method.
//
// Safety is general, not a shape special case: a value receiver owns only a
// decoded copy of itself and cannot reach the interpreter's backing storage
// through it (a write through that copy's reference storage is caught by the
// copied-receiver digest), and the post-callback coherence re-read walks every
// copied source — including a slice nested in the receiver's own fields — and
// fails the callback if the body wrote one, so a read-only method prints as Go
// does while a stale write fails loudly.

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// A read-only value String and value Format over a slice the caller copies must
// print exactly what native Go prints. differGoSource runs each source through
// both the interpreter and the real Go oracle and requires them to agree.
func TestS809ValueProtocolCallbackReadOnly(t *testing.T) {
	cases := map[string]string{
		// A value String reading its own slice field (len), forwarded through a
		// variadic helper exactly as ir.HTMLWriter.Printf forwards into fmt.
		"value_string_reads_own_slice": `package main

import "fmt"

type node struct {
	name string
	kids []int
}

func (n node) String() string { return fmt.Sprintf("%s(%d)", n.name, len(n.kids)) }

func printf(format string, v ...any) string { return fmt.Sprintf(format, v...) }

func main() {
	n := node{name: "root", kids: []int{1, 2, 3}}
	fmt.Println(printf("<%v>", n))
}
`,
		// A value Format method (fmt.Formatter), which ir dumps reach through the
		// %+v verb, over the same copied-slice boundary and a variadic forward.
		"value_format_over_writer": `package main

import "fmt"

type node struct {
	tag  string
	data []byte
}

func (n node) Format(f fmt.State, verb rune) { fmt.Fprintf(f, "%s:%d", n.tag, len(n.data)) }

func fprintf(format string, v ...any) { fmt.Printf(format, v...) }

func main() {
	n := node{tag: "loc", data: []byte{7, 8}}
	fprintf("%+v\n", n)
}
`,
		// A slice of struct values whose element String reads a slice field: the
		// copied source is nested one level below the direct slice fmt walks.
		"slice_of_values_string": `package main

import "fmt"

type node struct {
	name string
	kids []int
}

func (n node) String() string { return fmt.Sprintf("%s#%d", n.name, len(n.kids)) }

func main() {
	fmt.Println([]node{{"a", []int{1}}, {"b", []int{2, 3}}})
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, src, nil, "") })
	}
}

// A value protocol method that writes a copied source fmt cannot re-read live
// must not print stale. Here the written slice (global g) is reachable only
// nested inside the value fmt formats, so there is no direct slice argument to
// synchronise; the post-callback coherence re-read catches the write and fails
// the callback loudly, printing nothing rather than a stale value.
func TestS809ValueProtocolCallbackStaleWriteRefused(t *testing.T) {
	src := `package main
import "fmt"
var g = []int{1, 2, 3}
type node struct{ kids []int }
func (n node) String() string { g[0] = 99; return fmt.Sprintf("n%d", len(n.kids)) }
func printf(format string, v ...any) string { return fmt.Sprintf(format, v...) }
func main() {
	n := node{kids: g}
	fmt.Println(printf("<%v>", n))
	println("after")
}`
	out, stderr, err := runGoSource(t, "s809stale", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "the copy would be stale"))
	qt.Assert(t, qt.IsFalse(strings.Contains(out+stderr, "after")), qt.Commentf("program continued: %q %q", out, stderr))
}

// TestS809NestedNativeSlicesValuesIterators pins a source-only regression from
// Go's rangefunc corpus: each slices.Values call returns a native iterator
// handle, and slices.Collect invokes that handle through the same checked yield
// callback path as direct range-over-function. Nested loops also prove that the
// synthetic yield callback owns its lexical cells per invocation instead of
// sharing the inner accumulator between sibling ranges.
func TestS809NestedNativeSlicesValuesIterators(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"iter"
	"slices"
)

func Bug70035(s1, s2, s3 []string) string {
	var c1 string
	for v1 := range slices.Values(s1) {
		var c2 string
		for v2 := range slices.Values(s2) {
			var c3 string
			for v3 := range slices.Values(s3) {
				c3 = c3 + v3
			}
			c2 = c2 + v2 + c3
		}
		c1 = c1 + v1 + c2
	}
	return c1
}

func main() {
	fmt.Println(Bug70035([]string{"1", "2", "3"}, []string{"a", "b", "c"}, []string{"A", "B", "C"}))
	fmt.Println(slices.Collect(slices.Values([]string{"x", "y"})))
	fmt.Println(slices.Collect(slices.Values([]int{})) == nil)
	fmt.Println(slices.Collect(slices.Values([]string{"x"})) != nil)
	it := slices.Values([]int{7, 8})
	fmt.Println(slices.Collect(it))
	var named iter.Seq[int] = slices.Values([]int{9, 10})
	fmt.Println(slices.Collect(named))
	var first []string
	for v := range slices.Values([]string{"stop", "skip"}) {
		first = append(first, v)
		break
	}
	fmt.Println(first)
}
`
	got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/rangefunc")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "1aABCbABCcABC2aABCbABCcABC3aABCbABCcABC\n[x y]\ntrue\ntrue\n[7 8]\n[9 10]\n[stop]\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want native stdout %q", got, want)
	}
}

func TestS809NativeSlicesCollectIteratorDifferential(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"iter"
	"slices"
)

func main() {
	fmt.Println(slices.Collect(slices.Values([]string{"a", "b"})))
	fmt.Println(slices.Collect(slices.Values([]int{})) == nil)
	fmt.Println(slices.Collect(slices.Values([]string{"a"})) != nil)
	it := slices.Values([]int{7, 8})
	fmt.Println(slices.Collect(it))
	var named iter.Seq[int] = slices.Values([]int{9, 10})
	fmt.Println(slices.Collect(named))
	var first []string
	for v := range slices.Values([]string{"stop", "skip"}) {
		first = append(first, v)
		break
	}
	fmt.Println(first)
}
`
	differGoSource(t, source, nil, "")
}
