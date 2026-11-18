//go:build full

package interp_test

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0
//
// Shared slice storage across original callbacks. Each differential case runs
// the unchanged program both on the interpreter and as a real Go build and
// requires byte-identical output: the ordering callbacks read and permute the
// interpreter's own backing array while the sort runs, so a sub-slice alias,
// the exact Less/Swap call counts and a callback's view of the in-flight order
// all match native Go. The refusal cases pin the shapes that stay out.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const s248Values = `func values(n int) []int {
	out := make([]int, n)
	x := uint32(2463534242)
	for i := range out {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		out[i] = int(x % 50)
	}
	return out
}
`

func TestS248SharedSliceCallbacks(t *testing.T) {
	for name, source := range map[string]string{
		"sort.Sort over a named slice with an alias": `package main
import ("fmt";"sort")
type bySlice []int
var less, swaps int
func (s bySlice) Len() int { return len(s) }
func (s bySlice) Less(i, j int) bool { less++; return s[i] < s[j] }
func (s bySlice) Swap(i, j int) { swaps++; s[i], s[j] = s[j], s[i] }
` + s248Values + `func main() {
	s := values(60)
	alias := s[10:20]
	sort.Sort(bySlice(s))
	fmt.Println(s)
	sorted := sort.IsSorted(bySlice(s))
	fmt.Println(alias, less, swaps, sorted)
}`,
		"sort.Slice closure reads the captured slice": `package main
import ("fmt";"sort")
` + s248Values + `func main() {
	s := values(70)
	alias := s[5:15]
	calls := 0
	sort.Slice(s, func(i, j int) bool { calls++; return s[i] > s[j] })
	fmt.Println(s)
	sorted := sort.SliceIsSorted(s, func(i, j int) bool { calls++; return s[i] > s[j] })
	fmt.Println(alias, calls, sorted)
}`,
		"sort.SliceStable over struct elements": `package main
import ("fmt";"sort")
type rec struct { key int; tag string }
` + s248Values + `func main() {
	var rs []rec
	for i, v := range values(40) {
		rs = append(rs, rec{v % 7, fmt.Sprint("t", i)})
	}
	head := rs[:8]
	calls := 0
	sort.SliceStable(rs, func(i, j int) bool { calls++; return rs[i].key < rs[j].key })
	fmt.Println(rs)
	fmt.Println(head, calls)
}`,
		"sort.Stable and IsSorted through an interface value": `package main
import ("fmt";"sort")
type byMod []int
var calls int
func (s byMod) Len() int { return len(s) }
func (s byMod) Less(i, j int) bool { calls++; return s[i]%5 < s[j]%5 }
func (s byMod) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
` + s248Values + `func main() {
	s := values(45)
	tail := s[30:]
	var data sort.Interface = byMod(s)
	fmt.Println(sort.IsSorted(data))
	sort.Stable(data)
	sorted := sort.IsSorted(data)
	fmt.Println(s, tail, calls, sorted)
}`,
		"slices.SortFunc callback observes the in-flight order": `package main
import ("cmp";"fmt";"slices")
` + s248Values + `func main() {
	s := values(50)
	alias := s[:10]
	trace, calls := 0, 0
	slices.SortFunc(s, func(a, b int) int { calls++; trace = trace*31 + s[0] + 7*s[len(s)-1]; trace %= 1000003; return cmp.Compare(a, b) })
	fmt.Println(s, alias, calls, trace)
	w := []string{"bb", "a", "cc", "d", "eee", "ff", "g"}
	slices.SortStableFunc(w, func(a, b string) int { return cmp.Compare(len(a), len(b)) })
	fmt.Println(w)
}`,
		"pointer receiver sort.Interface": `package main
import ("fmt";"sort")
type box struct { xs []int; swaps int }
func (b *box) Len() int { return len(b.xs) }
func (b *box) Less(i, j int) bool { return b.xs[i] < b.xs[j] }
func (b *box) Swap(i, j int) { b.swaps++; b.xs[i], b.xs[j] = b.xs[j], b.xs[i] }
` + s248Values + `func main() {
	b := &box{xs: values(33)}
	view := b.xs[3:9]
	sort.Sort(b)
	fmt.Println(b.xs, view, b.swaps)
}`,
		"generic ordered slice with NaN": `package main
import ("fmt";"math";"sort")
type Ordered interface { ~int | ~float64 | ~string }
type orderedSlice[E Ordered] []E
func (s orderedSlice[E]) Len() int { return len(s) }
func (s orderedSlice[E]) Less(i, j int) bool {
	if s[i] < s[j] { return true }
	isNaN := func(f E) bool { return f != f }
	return isNaN(s[i]) && !isNaN(s[j])
}
func (s orderedSlice[E]) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func sortOrdered[E Ordered](s []E) { sort.Sort(orderedSlice[E](s)) }
func main() {
	f := []float64{74.3, 59.0, math.Inf(1), 238.2, -784.0, 2.3, math.NaN(), math.NaN(), math.Inf(-1), 9845.768, -959.7485, 905, 7.8, 7.8}
	g := make([]float64, len(f))
	copy(g, f)
	sortOrdered(f)
	sort.Float64s(g)
	fmt.Println(f)
	fmt.Println(g)
	s := []string{"", "Hello", "foo", "bar", "foo", "f00", "%*&^*&^&", "***"}
	sortOrdered(s)
	fmt.Println(s)
}`,
		"panicking Less leaves the native partial order": `package main
import ("fmt";"sort")
` + s248Values + `func main() {
	s := values(40)
	calls := 0
	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		sort.Slice(s, func(i, j int) bool {
			calls++
			if calls == 97 { panic("stop") }
			return s[i] < s[j]
		})
	}()
	fmt.Println(s, calls)
}`,
		"fmt walk over a direct slice whose element callback writes its own receiver": `package main
import "fmt"
type pwn struct { a [3]uint }
func (p *pwn) String() string { p.a[1] = 7; return fmt.Sprint("pwn", p.a[0]) }
func main() {
	var a, b pwn
	b.a[0] = 4
	s := [][][]*pwn{{{&a, &b}}}
	out := fmt.Sprint(s)
	fmt.Println(out, len(s[0][0]), a.a, b.a)
}`,
		"fmt walk over value-receiver elements and a pointer argument": `package main
import "fmt"
type item struct{ n int }
func (i item) String() string { return fmt.Sprint("item", i.n) }
type counter struct{ hits int }
func (c *counter) String() string { c.hits++; return fmt.Sprint("hits", c.hits) }
var items = []item{{1}, {2}}
func main() {
	c := &counter{}
	fmt.Println(items, c, items[1:])
	fmt.Printf("%v %s %d\n", items, c, c.hits)
}`,
		"fmt callback rewrites a later slice element": `package main
import "fmt"
type T struct { n int }
var s []*T
func (t *T) String() string { if t.n == 1 { s[1] = &T{99} }; return fmt.Sprint(t.n) }
func main() {
	s = []*T{{1}, {2}}
	fmt.Println(s)
}`,
		"fmt callback writes a sibling pointee": `package main
import "fmt"
type T struct { n int }
var s []*T
func (t *T) String() string { if t.n == 1 { s[1].n = 42 }; return fmt.Sprint(t.n) }
func main() {
	s = []*T{{1}, {2}}
	fmt.Println(s)
}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

// TestS248SharedSliceRefusals pins the shapes the shared-storage mechanism
// cannot prove. Each must fail loudly rather than print a stale or diverged
// answer.
func TestS248SharedSliceRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ source, want, never string }{
		// sort.Reverse RETAINS its argument inside the value it returns, so
		// the dependency would keep a detached copy past the call.
		"retaining native callee": {`package main
import ("fmt";"sort")
type bySlice []int
func (s bySlice) Len() int { return len(s) }
func (s bySlice) Less(i, j int) bool { return s[i] < s[j] }
func (s bySlice) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func main() {
	s := []int{3, 1, 2}
	sort.Sort(sort.Reverse(bySlice(s)))
	fmt.Println(s)
}`, "original callback with copied slice references is unsupported", "[3 2 1]"},
	} {
		t.Run(name, func(t *testing.T) {
			got := s248RunRefused(t, tc.source)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("missing refusal %q: %q", tc.want, got)
			}
			if strings.Contains(got, tc.never) {
				t.Fatalf("a diverged answer escaped: %q", got)
			}
		})
	}
}

// s248RunRefused runs source on the interpreter, requires a failure, and
// returns the error with everything the program wrote, so a stale answer that
// reached stdout before the failure is caught too.
func s248RunRefused(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	runErr := runner.Run(ctx, program.File)
	if runErr == nil {
		t.Fatalf("want a refusal, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	return runErr.Error() + "\n" + stderr.String() + "\n" + stdout.String()
}
