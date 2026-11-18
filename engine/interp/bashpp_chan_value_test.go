// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 118 Story S118-CHANEVAL. Unmodified Go source drives channel
// operations through evaluated values, not through the operand's source text.
// Every program below is reproduced byte for byte from the canonical corpora
// (the Go tour's concurrency chapter and the Go-by-Example channel examples),
// so the oracle is the Go toolchain's own output for the same bytes.
var bashPPChannelEvaluationPrograms = map[string]string{
	// tour/concurrency/buffered-channels.go
	"tour-buffered-channels": `package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	fmt.Println(<-ch)
	fmt.Println(<-ch)
}
`,
	// tour/concurrency/channels.go
	"tour-channels": `package main

import "fmt"

func sum(s []int, c chan int) {
	sum := 0
	for _, v := range s {
		sum += v
	}
	c <- sum // send sum to c
}

func main() {
	s := []int{7, 2, 8, -9, 4, 0}

	c := make(chan int)
	go sum(s[:len(s)/2], c)
	go sum(s[len(s)/2:], c)
	x, y := <-c, <-c // receive from c

	fmt.Println(x, y, x+y)
}
`,
	// tour/concurrency/range-and-close.go
	"tour-range-and-close": `package main

import (
	"fmt"
)

func fibonacci(n int, c chan int) {
	x, y := 0, 1
	for i := 0; i < n; i++ {
		c <- x
		x, y = y, x+y
	}
	close(c)
}

func main() {
	c := make(chan int, 10)
	go fibonacci(cap(c), c)
	for i := range c {
		fmt.Println(i)
	}
}
`,
	// tour/concurrency/select.go
	"tour-select": `package main

import "fmt"

func fibonacci(c, quit chan int) {
	x, y := 0, 1
	for {
		select {
		case c <- x:
			x, y = y, x+y
		case <-quit:
			fmt.Println("quit")
			return
		}
	}
}

func main() {
	c := make(chan int)
	quit := make(chan int)
	go func() {
		for i := 0; i < 10; i++ {
			fmt.Println(<-c)
		}
		quit <- 0
	}()
	fibonacci(c, quit)
}
`,
	// examples/channel-buffering/channel-buffering.go
	"gbe-channel-buffering": `package main

import "fmt"

func main() {

	messages := make(chan string, 2)

	messages <- "buffered"
	messages <- "channel"

	fmt.Println(<-messages)
	fmt.Println(<-messages)
}
`,
	// examples/channel-directions/channel-directions.go
	"gbe-channel-directions": `package main

import "fmt"

func ping(pings chan<- string, msg string) {
	pings <- msg
}

func pong(pings <-chan string, pongs chan<- string) {
	msg := <-pings
	pongs <- msg
}

func main() {
	pings := make(chan string, 1)
	pongs := make(chan string, 1)
	ping(pings, "passed message")
	pong(pings, pongs)
	fmt.Println(<-pongs)
}
`,
}

// bashPPRunGoSource runs one Go source file through the interpreter and returns
// what it wrote. The program is never rewritten and never forwarded to the Go
// toolchain for execution.
func bashPPRunGoSource(t *testing.T, dir, path, source string) (string, string, error) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var out, errOut bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir),
		interp.StdIO(strings.NewReader(""), &out, &errOut))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runErr := runner.Run(ctx, program.File)
	return out.String(), errOut.String(), runErr
}

// TestBashPPChannelEvaluationMatchesGo is the interpreted half of the three-mode
// comparison: the native Go build of the unchanged source is the oracle.
func TestBashPPChannelEvaluationMatchesGo(t *testing.T) {
	t.Parallel()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Skipf("no Go toolchain: %v", err)
	}
	for name, source := range bashPPChannelEvaluationPrograms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			oracle := filepath.Join(dir, "oracle")
			if output, err := exec.Command(goBinary, "build", "-o", oracle, path).CombinedOutput(); err != nil {
				t.Fatalf("oracle build: %v %s", err, output)
			}
			var wantOut, wantErr bytes.Buffer
			native := exec.Command(oracle)
			native.Dir = dir
			native.Stdout, native.Stderr = &wantOut, &wantErr
			if err := native.Run(); err != nil {
				t.Fatalf("oracle run: %v", err)
			}
			gotOut, gotErr, runErr := bashPPRunGoSource(t, dir, path, source)
			if runErr != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", runErr, gotOut, gotErr)
			}
			// tour-channels fans two tasks into one unbuffered channel, so
			// which sum arrives first is genuinely unspecified and the native
			// oracle picks its own order too. Every other program's output is
			// fixed by the language and is compared byte for byte.
			if bashPPUnorderedChannelPrograms[name] {
				if sortedFields(gotOut) != sortedFields(wantOut.String()) {
					t.Fatalf("stdout=%q; oracle %q", gotOut, wantOut.String())
				}
			} else if gotOut != wantOut.String() {
				t.Fatalf("stdout=%q; oracle %q", gotOut, wantOut.String())
			}
			if gotErr != wantErr.String() {
				t.Fatalf("stderr=%q; oracle %q", gotErr, wantErr.String())
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != source {
				t.Fatal("original source changed")
			}
		})
	}
}

// bashPPUnorderedChannelPrograms names the programs whose output order the
// language does not fix.
var bashPPUnorderedChannelPrograms = map[string]bool{"tour-channels": true}

func sortedFields(s string) string {
	fields := strings.Fields(s)
	sort.Strings(fields)
	return strings.Join(fields, " ")
}

// TestBashPPChannelSendEvaluatesOperand pins the individual defects rather than
// only the end-to-end programs, so a regression names the operand that broke.
func TestBashPPChannelSendEvaluatesOperand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   string
	}{{
		name: "send-local-variable",
		source: `package main

import "fmt"

func main() {
	c := make(chan int, 1)
	sum := 42
	c <- sum
	fmt.Println(<-c)
}
`,
		want: "42\n",
	}, {
		name: "send-string-variable",
		source: `package main

import "fmt"

func main() {
	c := make(chan string, 1)
	msg := "passed message"
	c <- msg
	fmt.Println(<-c)
}
`,
		want: "passed message\n",
	}, {
		name: "receive-in-call-argument",
		source: `package main

import "fmt"

func main() {
	c := make(chan int, 2)
	c <- 7
	c <- 8
	fmt.Println(<-c, <-c)
}
`,
		want: "7 8\n",
	}, {
		name: "receive-in-arithmetic",
		source: `package main

import "fmt"

func main() {
	c := make(chan int, 2)
	c <- 7
	c <- 8
	fmt.Println(<-c + <-c)
}
`,
		want: "15\n",
	}, {
		name: "goroutine-computed-argument",
		source: `package main

import "fmt"

func fill(n int, c chan int) {
	for i := 0; i < n; i++ {
		c <- i
	}
	close(c)
}

func main() {
	c := make(chan int, 3)
	go fill(cap(c), c)
	for v := range c {
		fmt.Println(v)
	}
}
`,
		want: "0\n1\n2\n",
	}, {
		name: "goroutine-slice-argument",
		source: `package main

import "fmt"

func total(s []int, c chan int) {
	sum := 0
	for _, v := range s {
		sum += v
	}
	c <- sum
}

func main() {
	s := []int{7, 2, 8, -9, 4, 0}
	c := make(chan int)
	go total(s[:len(s)/2], c)
	fmt.Println(<-c)
}
`,
		want: "17\n",
	}, {
		name: "close-as-call",
		source: `package main

import "fmt"

func main() {
	c := make(chan int, 1)
	c <- 1
	close(c)
	v, more := <-c
	fmt.Println(v, more)
	w, rest := <-c
	fmt.Println(w, rest)
}
`,
		want: "1 true\n0 false\n",
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			gotOut, gotErr, runErr := bashPPRunGoSource(t, dir, path, test.source)
			if runErr != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", runErr, gotOut, gotErr)
			}
			if gotOut != test.want || gotErr != "" {
				t.Fatalf("stdout=%q stderr=%q; want %q", gotOut, gotErr, test.want)
			}
		})
	}
}

// TestBashPPChannelEvaluationPreservesClassic keeps the Classic dialect's word
// meaning: outside a Go region a send operand is its own literal, which is the
// established ABI these evaluations must not reach.
func TestBashPPChannelEvaluationPreservesClassic(t *testing.T) {
	t.Parallel()
	const script = `func main() {
 sum=42
 c := make(chan string, 2)
 c <- sum
 c <- $sum
 v := <-c
 w := <-c
 echo "$v $w"
}
main()
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(script), "classic.sh")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()),
		interp.StdIO(strings.NewReader(""), &out, &errOut))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	if out.String() != "sum 42\n" {
		t.Fatalf("stdout=%q stderr=%q; want %q", out.String(), errOut.String(), "sum 42\n")
	}
}
