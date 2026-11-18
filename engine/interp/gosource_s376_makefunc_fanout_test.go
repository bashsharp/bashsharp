//go:build full

package interp_test

// Sprint: #376; Story: #1550; Story-ID: cae490ea17e7
//
// The exact shape of fixedbugs/issue39541: goroutines calling a zero-argument
// reflect.MakeFunc function whose interpreted implementation returns a struct
// of a function-local type inside an interface. Scale via S376_GOROUTINES and
// S376_CALLS; the root itself is 100 goroutines of 10,000 calls.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s376MakeFuncScale() (goroutines, calls int) {
	goroutines, calls = 4, 50
	if v := os.Getenv("S376_GOROUTINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			goroutines = n
		}
	}
	if v := os.Getenv("S376_CALLS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			calls = n
		}
	}
	return goroutines, calls
}

func s376MakeFuncSource(goroutines, calls int) string {
	return fmt.Sprintf(`package main

import "reflect"

func sub(args []reflect.Value) []reflect.Value {
	type A struct {
		s int
		t int
	}
	return []reflect.Value{reflect.ValueOf(A{1, 2})}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func() interface{})(nil)), sub).Interface().(func() interface{})
	c := make(chan bool, %[1]d)
	for i := 0; i < %[1]d; i++ {
		go func() {
			for j := 0; j < %[2]d; j++ {
				f()
			}
			c <- true
		}()
	}
	for i := 0; i < %[1]d; i++ {
		<-c
	}
}
`, goroutines, calls)
}

// runGoSourceToFiles runs a program whose standard streams are files, as the
// command line gives them to a script. Buffers instead put the dependency's
// output behind pipes and an ordering marker per reply, which a file needs
// none of and which would be most of what a rate measured here counts.
func runGoSourceToFiles(t *testing.T, name, source string) (string, string, error) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource rejected the source: %v", err)
	}
	dir := t.TempDir()
	stream := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	stdout, stderr := stream("stdout"), stream("stderr")
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, stdout, stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err = runner.Run(ctx, program.File)
	read := func(f *os.File) string {
		data, readErr := os.ReadFile(f.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(data)
	}
	return read(stdout), read(stderr), err
}

func TestS376MakeFuncFanout(t *testing.T) {
	goroutines, calls := s376MakeFuncScale()
	start := time.Now()
	out, stderr, err := runGoSourceToFiles(t, "s376-makefunc-fanout", s376MakeFuncSource(goroutines, calls))
	elapsed := time.Since(start)
	if err != nil || out != "" || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
	total := goroutines * calls
	t.Logf("%d goroutines x %d calls = %d callbacks in %v (%.1f us/callback all-in)",
		goroutines, calls, total, elapsed, float64(elapsed.Microseconds())/float64(total))
}

// More made-function calls are in flight at once than the helper has route
// slots (256): every implementation is entered before any may return, so the
// later dispatches are routed by goroutine number. A callback that lost its
// route has no parked request to run on, and the program never finishes.
func TestS376MakeFuncRouteSlotOverflow(t *testing.T) {
	const tasks = 300
	out, stderr, err := runGoSource(t, "s376-route-overflow", fmt.Sprintf(`package main

import (
	"fmt"
	"reflect"
)

var entered = make(chan bool, %[1]d)
var release = make(chan bool)

func impl(args []reflect.Value) []reflect.Value {
	entered <- true
	<-release
	return []reflect.Value{reflect.ValueOf(int(args[0].Int()) + 1)}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func(int) int)(nil)), impl).Interface().(func(int) int)
	results := make(chan int, %[1]d)
	for g := 0; g < %[1]d; g++ {
		go func(g int) { results <- f(g) }(g)
	}
	for g := 0; g < %[1]d; g++ {
		<-entered
	}
	close(release)
	sum := 0
	for g := 0; g < %[1]d; g++ {
		sum += <-results
	}
	fmt.Println(sum)
}
`, tasks))
	want := fmt.Sprintln(tasks * (tasks + 1) / 2)
	if err != nil || out != want || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q want %q", err, out, stderr, want)
	}
}

// The callback is raised far deeper in the dependency than the helper walks
// for a slot frame: the encoder recurses through every nesting level before
// it reaches the original MarshalText. The goroutine number then names the
// route, and the method still runs on the request that raised it.
func TestS376MakeFuncDeepCallbackRoute(t *testing.T) {
	out, stderr, err := runGoSource(t, "s376-deep-route", `package main

import (
	"encoding/json"
	"fmt"
	"reflect"
)

type leaf struct{ n int }

func (l leaf) MarshalText() ([]byte, error) {
	return []byte(fmt.Sprint("leaf", l.n)), nil
}

func impl(args []reflect.Value) []reflect.Value {
	var v any = leaf{int(args[0].Int())}
	for i := 0; i < 400; i++ {
		v = []any{v}
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return []reflect.Value{reflect.ValueOf(len(b))}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func(int) int)(nil)), impl).Interface().(func(int) int)
	fmt.Println(f(7), f(42))
}
`)
	if want := "807 808\n"; err != nil || out != want || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q want %q", err, out, stderr, want)
	}
}

// A made function called from inside another made function's implementation,
// from several goroutines: each inner callback belongs to the request its own
// outer callback raised, and a panic in the inner implementation unwinds
// through both to the goroutine that called, after which the same functions
// are called again.
func TestS376MakeFuncNestedRoutes(t *testing.T) {
	out, stderr, err := runGoSource(t, "s376-nested-routes", `package main

import (
	"fmt"
	"reflect"
)

var inner func(int) int

func innerImpl(args []reflect.Value) []reflect.Value {
	n := int(args[0].Int())
	if n%7 == 3 {
		panic(fmt.Sprint("inner ", n))
	}
	return []reflect.Value{reflect.ValueOf(n * 2)}
}

func outerImpl(args []reflect.Value) []reflect.Value {
	return []reflect.Value{reflect.ValueOf(inner(int(args[0].Int())) + 1)}
}

func call(f func(int) int, n int) (v int) {
	defer func() {
		if r := recover(); r != nil {
			v = -1
		}
	}()
	return f(n)
}

func main() {
	typ := reflect.TypeOf((func(int) int)(nil))
	inner = reflect.MakeFunc(typ, innerImpl).Interface().(func(int) int)
	outer := reflect.MakeFunc(typ, outerImpl).Interface().(func(int) int)
	c := make(chan int, 8)
	for g := 0; g < 8; g++ {
		go func(g int) {
			sum := 0
			for i := 0; i < 20; i++ {
				sum += call(outer, g*20+i)
			}
			c <- sum
		}(g)
	}
	total := 0
	for g := 0; g < 8; g++ {
		total += <-c
	}
	fmt.Println(total)
}
`)
	want := 0
	for n := range 160 {
		if n%7 == 3 {
			want--
		} else {
			want += n*2 + 1
		}
	}
	if err != nil || out != fmt.Sprintln(want) || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q want %d", err, out, stderr, want)
	}
}
