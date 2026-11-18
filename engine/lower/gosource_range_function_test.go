package lower_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runRangeFunctionSource(t *testing.T, source string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "main.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), program.File)
	return stdout.String(), stderr.String(), err
}

func TestGoSourceRangeFunctionCallbackTransport(t *testing.T) {
	const source = `package main
import "fmt"
func pairs(yield func(int, string) bool) {
	for i := range 3 {
		if !yield(i, "value") { fmt.Println("stopped"); return }
	}
	fmt.Println("exhausted")
}
func main() {
	for i, value := range pairs {
		fmt.Println(i, value)
		if i == 1 { break }
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != "0 value\n1 value\nstopped\n" || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestGoSourceRangeFunctionTypedInterfaceYieldTransport(t *testing.T) {
	const source = `package main
import (
	"fmt"
	"go/types"
)
func typeset(seed types.Type) func(func(types.Type, types.Type) bool) {
	return func(yield func(types.Type, types.Type) bool) {
		yield(seed, types.Typ[types.String])
	}
}
func cond(t, u types.Type) bool { return t.String() == "int" && u.String() == "string" }
func main() {
	for t, u := range typeset(types.Typ[types.Int]) {
		fmt.Println(cond(t, u), t.String(), u.String())
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != "true int string\n" || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestGoSourceRangeFunctionLocalInterfaceYieldTransport keeps range-function
// admission tied to the transport the iterator actually uses. A local
// iterator calls its synthetic yield closure inside the same Runner, so an
// interface value remains a typed cell and does not cross the dependency
// callback bridge. This is the standalone shape used by compiler type-set
// iterators: both the iterator argument and its two yields have a package-local
// interface type.
func TestGoSourceRangeFunctionLocalInterfaceYieldTransport(t *testing.T) {
	const source = `package main
import "fmt"
type Type interface {
	Underlying() Type
	String() string
}
type Basic struct { name string }
func (b *Basic) Underlying() Type { return b }
func (b *Basic) String() string { return b.name }
func all(t Type, yield func(Type, Type) bool) bool {
	return yield(t, t.Underlying())
}
func typeset(t Type) func(func(Type, Type) bool) {
	return func(yield func(Type, Type) bool) { all(t, yield) }
}
func names(t, u Type) string { return t.(*Basic).name + " " + u.(*Basic).name }
func main() {
	var seed Type = &Basic{name: "local"}
	for t, u := range typeset(seed) {
		fmt.Println(names(t, u))
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != "local local\n" || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestGoSourceRangeFunctionValueAggregateYieldTransport(t *testing.T) {
	const source = `package main
import "fmt"
type Pair struct { Left, Right int }
func values(yield func(Pair, [2]string) bool) {
	yield(Pair{Left: 3, Right: 5}, [2]string{"go", "types"})
}
func main() {
	for pair, words := range values {
		fmt.Println(pair.Left+pair.Right, words[0], words[1])
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != "8 go types\n" || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestGoSourceRangeFunctionYieldAfterFalsePanics(t *testing.T) {
	const source = `package main
import "fmt"
func bad(yield func(int) bool) {
	if !yield(1) { yield(2) }
}
func main() {
	defer func() { fmt.Println("recovered:", recover()) }()
	for value := range bad {
		fmt.Println(value)
		break
	}
	fmt.Println("unreachable")
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	const want = "1\nrecovered: runtime error: range function continued iteration after function for loop body returned false\n"
	if stdout != want || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestGoSourceRangeFunctionYieldAfterExhaustedPanics pins the distinct
// runtime error Go reports when a yield closure escapes its iterator (saved
// to an outer variable, as cmd/compile/internal/rangefunc's TrickyIterator
// does) and is invoked after the whole range statement — and thus the
// iterator's call — has already returned. That is a different misuse from
// TestGoSourceRangeFunctionYieldAfterFalsePanics above, where the *same*
// live iterator call keeps calling yield right after it returned false:
// Go's checked rangefunc rewrite tracks state per range statement and
// reports "after whole loop exit" only once the iterator call has actually
// returned, not merely once the body has broken out.
func TestGoSourceRangeFunctionYieldAfterExhaustedPanics(t *testing.T) {
	const source = `package main
import "fmt"
var saved func(int) bool
func iterAll(yield func(int) bool) {
	saved = yield
	for i := 0; i < 10; i++ {
		if !yield(i) { return }
	}
}
func main() {
	sum := 0
	for x := range iterAll {
		sum += x
		if sum >= 6 { break }
	}
	fmt.Println("sum:", sum)
	defer func() { fmt.Println("recovered:", recover()) }()
	saved(1)
	fmt.Println("unreachable")
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	const want = "sum: 6\nrecovered: runtime error: range function continued iteration after whole loop exit\n"
	if stdout != want || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestGoSourceRangeFunctionSwallowedPanicMissingReport pins Go's fourth
// rangefunc misuse report — RF_MISSING_PANIC, "recovered a loop body panic
// and did not resume panicking" — for an iterator that wraps each yield call
// in its own defer/recover (as cmd/compile/internal/rangefunc's
// SwallowPanicOfSliceIndex does) and lets a body panic be swallowed there
// instead of resuming it. Go's rangefunc rewrite records the per-loop state
// as PANIC the moment the body starts running and only clears it once the
// body finishes normally; the iterator call returning with state still PANIC
// (nothing having resumed the panic or called yield again) is itself the
// misuse, reported once the iterator's own call to the range function
// returns. Sprint #281 Story #809: the interpreter used to fold this into an
// ordinary false return and let the iterator return with no report at all.
func TestGoSourceRangeFunctionSwallowedPanicMissingReport(t *testing.T) {
	const source = `package main
import "fmt"
func swallow(yield func(int) bool) {
	for _, v := range []int{1, 2, 3, 4} {
		done := false
		func() {
			defer func() {
				if r := recover(); r != nil { done = true }
			}()
			done = !yield(v)
		}()
		if done { return }
	}
}
func main() {
	var result []int
	defer func() {
		fmt.Println("recovered:", recover())
		fmt.Println("result:", result)
	}()
	for x := range swallow {
		result = append(result, x)
		if x == 3 { panic("x is 3") }
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	const want = "recovered: runtime error: range function recovered a loop body panic and did not resume panicking\nresult: [1 2 3]\n"
	if stdout != want || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestGoSourceRangeFunctionYieldAfterBodyPanicReportsDistinctError pins the
// THIRD distinct rangefunc misuse message — RF_PANIC, "continued iteration
// after loop body panic" — kept apart from the DONE case
// (TestGoSourceRangeFunctionYieldAfterFalsePanics) and the EXHAUSTED case
// (TestGoSourceRangeFunctionYieldAfterExhaustedPanics). Modeled on
// cmd/compile/internal/rangefunc's `twice` helper: the iterator recovers a
// body panic from its first yield call in a wrapper func, notices its `done`
// flag was never set (the panic aborted the assignment that would have set
// it), and calls yield a second time anyway — while the per-loop state is
// still PANIC, left over from the call that panicked, rather than DONE from
// an ordinary false return. Sprint #281 Story #809: the interpreter
// conflated a panicking body with an ordinary `return` in the body (both set
// the same "stop" flag), so this second call was reported as the DONE case
// instead.
func TestGoSourceRangeFunctionYieldAfterBodyPanicReportsDistinctError(t *testing.T) {
	const source = `package main
import "fmt"
func twice(x, y int) func(func(int) bool) {
	return func(yield func(int) bool) {
		done := false
		func() {
			defer func() { recover() }()
			done = !yield(x)
		}()
		if done { return }
		yield(y)
	}
}
func main() {
	defer func() { fmt.Println("recovered:", recover()) }()
	for x := range twice(0, 1) {
		if x == 0 { panic("x is zero") }
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	const want = "recovered: runtime error: range function continued iteration after loop body panic\n"
	if stdout != want || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestGoSourceRangeFunctionLabeledContinueSkipsUnrelatedIteratorLoop pins a
// labeled continue escaping THREE range-over-function levels, past a
// misbehaving innermost iterator (cmd/compile/internal/rangefunc's
// BadOfSliceIndex, which ignores yield's false return and keeps calling it)
// straight to the outermost loop's label — the shape TestMultiCont3 in that
// corpus exercises. Sprint #281 Story #809: the interpreter threaded the
// pending escape as one runner-global flag with no notion of which function
// it belonged to, so BadOfSliceIndex's own (unrelated) slice-range loop —
// entered by a real function call the escaping continue happened to unwind
// through — silently consumed one level of the escape meant only for the
// range statements textually nested in main, landing the continue one loop
// short of its label instead of at it.
func TestGoSourceRangeFunctionLabeledContinueSkipsUnrelatedIteratorLoop(t *testing.T) {
	const source = `package main
import "fmt"
func ofSlice(s []int) func(func(int, int) bool) {
	return func(yield func(int, int) bool) {
		for i, v := range s {
			if !yield(i, v) { return }
		}
	}
}
func badOfSlice(s []int) func(func(int, int) bool) {
	return func(yield func(int, int) bool) {
		for i, v := range s {
			yield(i, v)
		}
	}
}
func main() {
	var result []int
	defer func() {
		fmt.Println("recovered:", recover())
		fmt.Println("result:", result)
	}()
W:
	for _, w := range ofSlice([]int{1000, 2000}) {
		result = append(result, w)
		if w == 2000 { break }
		for _, x := range ofSlice([]int{100, 200}) {
			for _, y := range ofSlice([]int{10, 20}) {
				result = append(result, y)
				for _, z := range badOfSlice([]int{1, 2, 3, 4}) {
					result = append(result, z)
					if z >= 2 { continue W }
				}
			}
			result = append(result, x)
		}
	}
}`
	stdout, stderr, err := runRangeFunctionSource(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	const want = "recovered: runtime error: range function continued iteration after function for loop body returned false\nresult: [1000 10 1 2]\n"
	if stdout != want || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestGoSourceRangeFunctionCallbackShapeBoundaries(t *testing.T) {
	for name, test := range map[string]struct {
		source string
		want   string
	}{
		"slice": {`package main
import "slices"
func main() { for range slices.Values([][]int{{1}}) { println("body-ran") } }`, "native slice retention or mutation is unsupported"},
		"map": {`package main
import "slices"
func main() { for range slices.Values([]map[string]int{{"x": 1}}) { println("body-ran") } }`, "iterator callback yield parameter requires shared-reference transport"},
		"pointer": {`package main
import "slices"
func main() { for range slices.Values([]*int{new(int)}) { println("body-ran") } }`, "iterator callback yield parameter requires shared-reference transport"},
		"function": {`package main
import "slices"
func main() { for range slices.Values([]func(){func(){}}) { println("body-ran") } }`, "string for func()"},
		"channel": {`package main
import "slices"
func main() { for range slices.Values([]chan int{make(chan int)}) { println("body-ran") } }`, "iterator callback yield parameter requires shared-reference transport"},
		"interface": {`package main
import "slices"
type Stringer interface { String() string }
func main() { var values []Stringer; for range slices.Values(values) { println("body-ran") } }`, "iterator callback yield parameter requires shared-reference transport"},
	} {
		t.Run("shared-reference "+name+" yield parameter", func(t *testing.T) {
			stdout, stderr, err := runRangeFunctionSource(t, test.source)
			if err == nil || !strings.Contains(err.Error()+stderr, test.want) {
				t.Fatalf("missing boundary: %v stdout=%q stderr=%q", err, stdout, stderr)
			}
			if strings.Contains(stdout+stderr, "body-ran") {
				t.Fatalf("unsupported callback executed: stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}

	for name, source := range map[string]string{
		"non-bool yield result":  `package main; func bad(yield func(int) int) {}; func main() { for range bad {} }`,
		"three yield parameters": `package main; func bad(yield func(int, int, int) bool) {}; func main() { for range bad {} }`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gosource.Parse(strings.NewReader(source), "main.go", gosource.Options{RunMain: true}); err == nil {
				t.Fatal("invalid Go range callback shape was accepted")
			}
		})
	}
}

func TestGoSourceGbERangeIteratorsInterpreted(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-gbe/range-over-iterators.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runRangeFunctionSource(t, string(source))
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	const want = "10\n13\n23\nall: [10 13 23]\npart: go\npart: by\npart: example\n0\n1\n1\n2\n3\n5\n8\n"
	if stdout != want || stderr != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout, stderr)
	}
}
