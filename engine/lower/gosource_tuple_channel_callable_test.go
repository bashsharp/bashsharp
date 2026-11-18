//go:build full

package lower_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #118; Story: #67; Story-ID: 83b5cdc6fca6
//
// These cover the four surfaces the Go-by-Example originals spawning-processes,
// stateful-goroutines, time and variadic-functions stopped on: general
// multi-result assignment, the channel element bridge, the callable/method
// bridge over a dependency-owned defined scalar, and variadic slice expansion.
// The originals themselves run at the bottom of the file, unchanged and pinned.

func runGoSourceInterpreted(t *testing.T, source string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "main.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return stdout.String(), stderr.String(), err
}

func goSourceWantStreams(t *testing.T, source, want string) {
	t.Helper()
	stdout, stderr, err := runGoSourceInterpreted(t, source)
	if err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q want=%q", stdout, stderr, want)
	}
}

func TestGoSourceVariadicSliceExpansion(t *testing.T) {
	t.Run("parameter is a slice", func(t *testing.T) {
		// Printing, len, cap, indexing and range all have to see one []int.
		goSourceWantStreams(t, `package main
import "fmt"
func sum(nums ...int) int {
	total := 0
	for _, n := range nums { total += n }
	fmt.Println(nums, len(nums), cap(nums))
	return total
}
func main() {
	fmt.Println(sum(1, 2))
	fmt.Println(sum())
	fmt.Println(sum(1, 2, 3, 4))
}`, "[1 2] 2 2\n3\n[] 0 0\n0\n[1 2 3 4] 4 4\n10\n")
	})

	t.Run("spread passes the caller's slice", func(t *testing.T) {
		// `f(xs...)` aliases xs, so a write through the parameter is a write to
		// xs; the packed form copies and must not be.
		goSourceWantStreams(t, `package main
import "fmt"
func first(nums ...int) { if len(nums) > 0 { nums[0] = 99 } }
func main() {
	xs := []int{1, 2}
	first(xs...)
	fmt.Println(xs)
	ys := []int{1, 2}
	first(ys[0], ys[1])
	fmt.Println(ys)
}`, "[99 2]\n[1 2]\n")
	})

	t.Run("spread of a nil slice", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
func take(nums ...int) { fmt.Println(nums == nil, len(nums)) }
func main() {
	var xs []int
	take(xs...)
	take()
}`, "true 0\nfalse 0\n")
	})

	t.Run("aggregate and forwarded elements", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
type point struct{ X, Y int }
func show(ps ...point) { fmt.Println(ps, len(ps)) }
func forward(ps ...point) { show(ps...) }
func main() {
	forward(point{1, 2}, point{3, 4})
	show([]point{{5, 6}}...)
}`, "[{1 2} {3 4}] 2\n[{5 6}] 1\n")
	})

	t.Run("spread into a non-variadic function", func(t *testing.T) {
		// Rejected before anything runs: Go's own type checker owns this, and
		// the spread binding must never be reached for a callee that has no
		// variadic parameter to bind.
		const source = `package main
func take(a int) {}
func main() { xs := []int{1}; take(xs...) }`
		_, err := gosource.Parse(strings.NewReader(source), "main.go", gosource.Options{RunMain: true})
		if err == nil || !strings.Contains(err.Error(), "non-variadic") {
			t.Fatalf("spread into a non-variadic callee: %v", err)
		}
	})
}

func TestGoSourceChannelElementBridge(t *testing.T) {
	t.Run("struct field", func(t *testing.T) {
		// The request/response idiom: the reply channel travels inside the
		// request value and has to arrive as the same channel.
		goSourceWantStreams(t, `package main
import "fmt"
type readOp struct {
	key  int
	resp chan int
}
func main() {
	reads := make(chan readOp)
	go func() {
		for {
			read := <-reads
			read.resp <- read.key * 2
		}
	}()
	op := readOp{key: 21, resp: make(chan int)}
	reads <- op
	fmt.Println(<-op.resp)
}`, "42\n")
	})

	t.Run("assigned field", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
type box struct{ c chan string }
func main() {
	var b box
	b.c = make(chan string, 1)
	b.c <- "hi"
	fmt.Println(<-b.c)
}`, "hi\n")
	})

	t.Run("slice and map elements", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
func main() {
	cs := []chan int{make(chan int, 1), make(chan int, 1)}
	cs[0] <- 1
	cs[1] <- 2
	fmt.Println(<-cs[0], <-cs[1])
	m := map[string]chan int{"a": make(chan int, 1)}
	m["a"] <- 7
	fmt.Println(<-m["a"])
}`, "1 2\n7\n")
	})

	t.Run("nil channel field", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
type box struct{ c chan int }
func main() {
	b := box{c: nil}
	fmt.Println(b.c == nil)
}`, "true\n")
	})

	t.Run("field of an interpreter-owned element type", func(t *testing.T) {
		// A channel whose element is a local struct is the interpreter's own
		// channel rather than a dependency handle; both must survive a field.
		goSourceWantStreams(t, `package main
import "fmt"
type item struct{ n int }
type req struct{ resp chan item }
func main() {
	r := req{resp: make(chan item, 1)}
	r.resp <- item{n: 5}
	got := <-r.resp
	fmt.Println(got.n)
}`, "5\n")
	})
}

func TestGoSourceNativeScalarMethodBridge(t *testing.T) {
	t.Run("methods on a dependency-owned defined scalar", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import (
	"fmt"
	"time"
)
func main() {
	a := time.Date(2009, 11, 17, 20, 34, 58, 651387237, time.UTC)
	b := time.Date(2009, 11, 18, 20, 34, 58, 651387237, time.UTC)
	d := b.Sub(a)
	fmt.Println(d)
	fmt.Println(d.Hours(), d.Minutes(), d.Seconds(), d.Nanoseconds())
	fmt.Println(d.String())
	fmt.Println(a.Add(d))
	fmt.Println(a.Add(-d))
}`, "24h0m0s\n24 1440 86400 86400000000000\n24h0m0s\n2009-11-18 20:34:58.651387237 +0000 UTC\n2009-11-16 20:34:58.651387237 +0000 UTC\n")
	})

	t.Run("an unknown method is still rejected", func(t *testing.T) {
		const source = `package main
import "time"
func main() {
	d := time.Duration(1)
	d.NoSuchMethod()
}`
		_, err := gosource.Parse(strings.NewReader(source), "main.go", gosource.Options{RunMain: true})
		if err == nil || !strings.Contains(err.Error(), "NoSuchMethod") {
			t.Fatalf("unknown method on a dependency scalar: %v", err)
		}
	})

	t.Run("assertion to a dependency-owned type", func(t *testing.T) {
		// The three spellings of one imported type — the program's rewritten
		// import alias, the package path reflect reports, and Go's printed
		// pointer form — have to name the same type.
		goSourceWantStreams(t, `package main
import (
	"errors"
	"fmt"
	"os/exec"
)
func main() {
	_, err := exec.Command("false").Output()
	if e, ok := errors.AsType[*exec.Error](err); ok {
		fmt.Println("failed executing:", e)
	} else if e, ok := errors.AsType[*exec.ExitError](err); ok {
		fmt.Println("command exit rc =", e.ExitCode())
	} else {
		panic(err)
	}
}`, "command exit rc = 1\n")
	})
}

func TestGoSourceMultiResultAssignment(t *testing.T) {
	t.Run("structured targets", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
type point struct{ X, Y int }
func two() (int, int) { return 3, 4 }
func main() {
	var p point
	xs := make([]int, 2)
	m := map[string]int{}
	p.X, xs[1] = two()
	m["k"], p.Y = two()
	q := &p
	var z int
	z, q.X = two()
	fmt.Println(p, xs, m, z)
}`, "{4 4} [0 4] map[k:3] 3\n")
	})

	t.Run("blank and existing targets", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
type box struct{ v int }
func two() (int, error) { return 8, nil }
func main() {
	var b box
	var err error
	_, err = two()
	b.v, err = two()
	fmt.Println(b.v, err)
}`, "8 <nil>\n")
	})

	t.Run("right-hand side evaluated before assignment", func(t *testing.T) {
		// A swap through structured targets still reads both old values first.
		goSourceWantStreams(t, `package main
import "fmt"
func main() {
	xs := []int{1, 2}
	xs[0], xs[1] = xs[1], xs[0]
	fmt.Println(xs)
}`, "[2 1]\n")
	})

	t.Run("comma-ok into a field", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import "fmt"
type result struct {
	v  int
	ok bool
}
func main() {
	m := map[string]int{"a": 1}
	var r result
	r.v, r.ok = m["a"]
	fmt.Println(r)
	r.v, r.ok = m["b"]
	fmt.Println(r)
}`, "{1 true}\n{0 false}\n")
	})
}

func TestGoSourceAtomicIntegerFunctions(t *testing.T) {
	t.Run("operations", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import (
	"fmt"
	"sync/atomic"
)
func main() {
	var u uint64
	fmt.Println(atomic.AddUint64(&u, 1), atomic.AddUint64(&u, 41), atomic.LoadUint64(&u))
	var n int32 = 5
	fmt.Println(atomic.SwapInt32(&n, 9), atomic.LoadInt32(&n))
	fmt.Println(atomic.CompareAndSwapInt32(&n, 9, 11), n)
	fmt.Println(atomic.CompareAndSwapInt32(&n, 9, 12), n)
	atomic.StoreInt32(&n, -3)
	fmt.Println(n, atomic.AddInt32(&n, -1))
}`, "1 42 42\n5 9\ntrue 11\nfalse 11\n-3 -4\n")
	})

	t.Run("wraps at the operand width", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import (
	"fmt"
	"math"
	"sync/atomic"
)
func main() {
	n := int32(math.MaxInt32)
	fmt.Println(atomic.AddInt32(&n, 1))
	u := uint32(0)
	fmt.Println(atomic.AddUint32(&u, ^uint32(0)))
}`, "-2147483648\n4294967295\n")
	})

	t.Run("concurrent increments are not lost", func(t *testing.T) {
		goSourceWantStreams(t, `package main
import (
	"fmt"
	"sync"
	"sync/atomic"
)
func main() {
	var ops uint64
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 100 {
				atomic.AddUint64(&ops, 1)
			}
		})
	}
	wg.Wait()
	fmt.Println("ops:", atomic.LoadUint64(&ops))
}`, "ops: 2000\n")
	})
}

// goSourceGbEFixture reads one archived original and refuses a fixture whose
// bytes have drifted from the pinned upstream copy.
func goSourceGbEFixture(t *testing.T, name string) string {
	t.Helper()
	pinsBytes, err := os.ReadFile("testdata/gosource-gbe/sha256.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err := json.Unmarshal(pinsBytes, &pins); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/gosource-gbe/" + name + ".go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != pins[name] {
		t.Fatal("original fixture bytes changed")
	}
	return string(data)
}

func TestGoSourceGbEVariadicFunctionsInterpreted(t *testing.T) {
	goSourceWantStreams(t, goSourceGbEFixture(t, "variadic-functions"),
		"[1 2] 3\n[1 2 3] 6\n[1 2 3 4] 10\n")
}

// The time original prints one wall-clock reading and two intervals derived
// from it, so those three lines cannot be compared as text. Everything the
// original prints about the fixed 2009 date is compared exactly, and the
// volatile lines are checked for the shape real Go gives them — a Duration, a
// float, a whole number of nanoseconds — rather than skipped.
func TestGoSourceGbETimeInterpreted(t *testing.T) {
	stdout, stderr, err := runGoSourceInterpreted(t, goSourceGbEFixture(t, "time"))
	if err != nil || stderr != "" {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	// The unchanged example has 21 prints: now, then, eight components,
	// weekday, three comparisons, diff, four unit conversions, and two Adds.
	if len(lines) != 21 {
		t.Fatalf("want 21 lines, got %d: %q", len(lines), stdout)
	}
	fixed := map[int]string{
		1: "2009-11-17 20:34:58.651387237 +0000 UTC",
		2: "2009", 3: "November", 4: "17", 5: "20", 6: "34", 7: "58",
		8: "651387237", 9: "UTC", 10: "Tuesday",
		11: "true", 12: "false", 13: "false",
	}
	for i, want := range fixed {
		if lines[i] != want {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want)
		}
	}
	if _, err := time.Parse("2006-01-02 15:04:05", lines[0][:19]); err != nil {
		t.Fatalf("line 0 is not a time reading: %q", lines[0])
	}
	if _, err := time.ParseDuration(lines[14]); err != nil {
		t.Fatalf("line 14 is not a duration: %q", lines[14])
	}
	for _, i := range []int{15, 16, 17} {
		if _, err := strconv.ParseFloat(lines[i], 64); err != nil {
			t.Fatalf("line %d is not a float: %q", i, lines[i])
		}
	}
	nanos, err := strconv.ParseInt(lines[18], 10, 64)
	if err != nil || nanos <= 0 {
		t.Fatalf("line 18 is not a nanosecond count: %q", lines[18])
	}
	// then.Add(diff) is now, and then.Add(-diff) is that far before then.
	for _, i := range []int{19, 20} {
		if _, err := time.Parse("2006-01-02 15:04:05", lines[i][:19]); err != nil {
			t.Fatalf("line %d is not a time: %q", i, lines[i])
		}
	}
	if !strings.HasSuffix(lines[19], "+0000 UTC") || !strings.HasSuffix(lines[20], "+0000 UTC") {
		t.Fatalf("Add lost the location: %q %q", lines[19], lines[20])
	}
}

// The op counts are a real schedule; real Go prints different ones on every
// run. What is checked is everything else: both counters are reported, in
// order, both advanced, and nothing reached stderr.
func TestGoSourceGbEStatefulGoroutinesInterpreted(t *testing.T) {
	stdout, stderr, err := runGoSourceInterpreted(t, goSourceGbEFixture(t, "stateful-goroutines"))
	if err != nil || stderr != "" {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	counts := regexp.MustCompile(`^readOps: (\d+)\nwriteOps: (\d+)\n$`).FindStringSubmatch(stdout)
	if counts == nil {
		t.Fatalf("unexpected streams: %q", stdout)
	}
	for _, count := range counts[1:] {
		n, err := strconv.Atoi(count)
		if err != nil || n <= 0 {
			t.Fatalf("counter did not advance: %q", stdout)
		}
	}
}

// The date, the working directory listing and grep's own binary are the host's,
// so the exact bytes are not this test's to fix. The structure real Go produces
// is: each section header, a non-empty body under it, the ExitError branch
// taken for `date -x`, and only the matching line surviving grep.
func TestGoSourceGbESpawningProcessesInterpreted(t *testing.T) {
	stdout, stderr, err := runGoSourceInterpreted(t, goSourceGbEFixture(t, "spawning-processes"))
	if err != nil || stderr != "" {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	sections := strings.Split(stdout, "> ")
	if len(sections) != 4 || sections[0] != "" {
		t.Fatalf("want three spawned sections: %q", stdout)
	}
	if !strings.HasPrefix(sections[1], "date\n") || len(strings.TrimSpace(sections[1][len("date\n"):])) == 0 {
		t.Fatalf("date produced no output: %q", sections[1])
	}
	if !strings.Contains(sections[1], "\ncommand exit rc = 1\n") {
		t.Fatalf("the *exec.ExitError branch was not taken: %q", sections[1])
	}
	if sections[2] != "grep hello\nhello grep\n\n" {
		t.Fatalf("grep section: %q", sections[2])
	}
	if !strings.HasPrefix(sections[3], "ls -a -l -h\n") || !strings.Contains(sections[3], " .\n") {
		t.Fatalf("ls section: %q", sections[3])
	}
}
