//go:build full

package interp_test

// Sprint: #374; Story: #1498; Story-ID: cf89c5e06bd1
//
// End-to-end shape of fixedbugs/issue39541: many calls of a reflect.MakeFunc
// function from several goroutines, where each native call carries a fresh
// slice (registering a slice origin) and calls back into the interpreted
// implementation. Scale via S374_GOROUTINES and S374_CALLS; defaults stay
// small enough for a quick smoke run.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s374FanoutScale() (goroutines, calls int) {
	goroutines, calls = 4, 50
	if v := os.Getenv("S374_GOROUTINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			goroutines = n
		}
	}
	if v := os.Getenv("S374_CALLS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			calls = n
		}
	}
	return goroutines, calls
}

func s374FanoutSource(goroutines, calls int) string {
	return fmt.Sprintf(`package main

import (
	"fmt"
	"reflect"
)

func impl(args []reflect.Value) []reflect.Value {
	return []reflect.Value{reflect.ValueOf(int(args[0].Int() + args[1].Int()))}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func(int, int) int)(nil)), impl).Interface().(func(int, int) int)
	c := make(chan int, %[1]d)
	for g := 0; g < %[1]d; g++ {
		go func(g int) {
			total := 0
			for i := 0; i < %[2]d; i++ {
				total += f(g, i)
				// A fresh slice per call: each transport registers a new
				// slice origin, the quadratic shape under a table scan.
				_ = fmt.Sprint([]int{g, i})
			}
			c <- total
		}(g)
	}
	sum := 0
	for g := 0; g < %[1]d; g++ {
		sum += <-c
	}
	fmt.Println(sum)
}
`, goroutines, calls)
}

func runGoSourceBench(b *testing.B, name, source string) (string, string, error) {
	b.Helper()
	program, err := gosource.Parse(bytes.NewReader([]byte(source)), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		b.Fatalf("gosource rejected the source: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(b.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

func s374FanoutWant(goroutines, calls int) string {
	return fmt.Sprintln(calls*goroutines*(goroutines-1)/2 + goroutines*calls*(calls-1)/2)
}

func TestS374NativeCallbackFanout(t *testing.T) {
	goroutines, calls := s374FanoutScale()
	out, stderr, err := runGoSource(t, "s374-fanout", s374FanoutSource(goroutines, calls))
	want := s374FanoutWant(goroutines, calls)
	if err != nil || out != want || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q want %q", err, out, stderr, want)
	}
}

func BenchmarkS374NativeCallbackFanout(b *testing.B) {
	goroutines, calls := s374FanoutScale()
	source := s374FanoutSource(goroutines, calls)
	want := s374FanoutWant(goroutines, calls)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, stderr, err := runGoSourceBench(b, "s374-fanout", source)
		if err != nil || out != want || stderr != "" {
			b.Fatalf("run=%v stdout=%q stderr=%q want %q", err, out, stderr, want)
		}
	}
}
