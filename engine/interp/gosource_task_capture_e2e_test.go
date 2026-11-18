//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
//
// Original Go controls for GoSource task capture.
//
// Capture metadata is not the deliverable; running an original Go program and
// getting Go's answer is. Every case here is a real, compilable Go program and
// is measured in all three modes:
//
//  1. the ORACLE — the program built and run by the Go toolchain;
//  2. the INTERPRETER — the same source through gosource.Parse and interp;
//  3. the COMPILED lowering — lower.Compile's output, built and run.
//
// The original file is checked byte-for-byte afterwards: nothing here rewrites
// the program it measures.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// captureThreeModes runs source as the Go toolchain does, as the interpreter
// does, and as the lowered program does, and requires all three to agree.
func captureThreeModes(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := func(input, output string) {
		t.Helper()
		cmd := exec.Command(sdk, "build", "-p", "2", "-o", output, input)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	run := func(binary string) (string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary)
		cmd.Dir = dir
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("run: %v %s", err, stderr.String())
		}
		return out.String(), stderr.String()
	}

	oracle := filepath.Join(dir, "oracle")
	build(path, oracle)
	wantOut, wantErr := run(oracle)

	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
	if out.String() != wantOut || stderr.String() != wantErr {
		t.Fatalf("Runner %q %q; oracle %q %q", out.String(), stderr.String(), wantOut, wantErr)
	}

	lowered, err := lower.Compile(program.File, lower.Options{Origin: path, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(generated, lowered.Source, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "compiled")
	build(generated, artifact)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != source {
		t.Fatal("original changed")
	}
	os.Remove(path)
	os.Remove(generated)
	if gotOut, gotErr := run(artifact); gotOut != wantOut || gotErr != wantErr {
		t.Fatalf("compiled %q %q; oracle %q %q", gotOut, gotErr, wantOut, wantErr)
	}
}

// The shared-counter control is the defect this story exists for: a correctly
// synchronized Go program printed the parent's untouched copy under the
// deep-copy snapshot.
//
// Synchronization here is by CHANNEL, not by sync.Mutex/WaitGroup. Those are
// imported native handles, and the snapshot rule that carries a native handle
// across the task boundary is a sibling story's, not in this lineage. Channels
// own their own cross-task
// identity already, so a channel-synchronized program measures exactly what
// this story decides — whether the plain interpreted cell is shared — without
// depending on work that is not here. A sync-synchronized program is measured
// separately by TestGoSourceCaptureNativeMutexSharedLocal.
func TestGoSourceCaptureSharedCounterThreeModes(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-task/shared-counter.go")
	if err != nil {
		t.Fatal(err)
	}
	captureThreeModes(t, string(source))
}

func TestGoSourceCaptureThreeModes(t *testing.T) {
	cases := map[string]string{
		// An explicit argument is copied BY VALUE, so the parent's loop
		// variable is never shared with a running task.
		"explicit_args_are_copied": `package main

import "fmt"

func main() {
	total := 0
	sem := make(chan bool, 1)
	done := make(chan bool)
	for i := 1; i <= 4; i++ {
		go func(n int) {
			sem <- true
			total += n
			<-sem
			done <- true
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	fmt.Println("total", total)
}
`,
		// A local of the same spelling is a different variable from the outer
		// one. The outer must keep the value the parent gave it.
		"local_shadow_is_not_the_outer": `package main

import "fmt"

func main() {
	counter := 100
	done := make(chan bool)
	go func() {
		counter := 0
		counter++
		_ = counter
		done <- true
	}()
	<-done
	fmt.Println("counter", counter)
}
`,
		// Go identifiers are Unicode.
		"unicode_identifier": `package main

import "fmt"

func main() {
	计数器 := 0
	sem := make(chan bool, 1)
	done := make(chan bool)
	for i := 0; i < 4; i++ {
		go func() {
			sem <- true
			计数器++
			<-sem
			done <- true
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	fmt.Println("计数器", 计数器)
}
`,
		// `go f()` where f is a closure held in a variable used to fall back to
		// the deep copy silently.
		"closure_held_in_variable": `package main

import "fmt"

func main() {
	counter := 0
	sem := make(chan bool, 1)
	done := make(chan bool)
	bump := func() {
		sem <- true
		counter++
		<-sem
		done <- true
	}
	for i := 0; i < 4; i++ {
		go bump()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	fmt.Println("counter", counter)
}
`,
		// A nested closure's free variable is captured by the body enclosing it.
		"nested_closures": `package main

import "fmt"

func main() {
	counter := 0
	sem := make(chan bool, 1)
	done := make(chan bool)
	for i := 0; i < 4; i++ {
		go func() {
			add := func(n int) {
				sem <- true
				counter += n
				<-sem
			}
			add(2)
			done <- true
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	fmt.Println("counter", counter)
}
`,
		// A captured pointer names the parent's variable through its target.
		"captured_pointer": `package main

import "fmt"

func main() {
	n := 0
	p := &n
	done := make(chan bool)
	go func() {
		*p = 7
		done <- true
	}()
	<-done
	fmt.Println("n", n, "p", *p)
}
`,
		// Local aggregates stay local; only their operands are captured.
		"local_array_map_slice": `package main

import "fmt"

func main() {
	seed := 3
	key := "k"
	sum := 0
	sem := make(chan bool, 1)
	done := make(chan bool)
	for i := 0; i < 2; i++ {
		go func() {
			xs := []int{seed, seed}
			m := map[string]int{key: seed}
			var arr [2]int
			arr[0] = xs[0]
			arr[1] = m[key]
			sem <- true
			sum += arr[0] + arr[1]
			<-sem
			done <- true
		}()
	}
	for i := 0; i < 2; i++ {
		<-done
	}
	fmt.Println("sum", sum, "seed", seed, "key", key)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { captureThreeModes(t, source) })
	}
}

// TestGoSourceCaptureNativeMutexSharedLocal is what the story text asked for
// all along, now that it runs.
//
// This test replaces TestGoSourceCaptureNativeHandlePending, which pinned the
// opposite claim: that a sync.Mutex / sync.WaitGroup program failed the
// snapshot with "unsupported mutable Bash++ object type" before capture was
// ever consulted, and that the case would move up into the three-mode table
// the day the sibling native-handle rule landed. It has landed — the task
// snapshot carries an imported native handle across by copying the descriptor
// and keeping Session/Handle (bashpp_task.go) — so the pending test was
// asserting a boundary that no longer exists and would have kept failing as a
// false alarm.
//
// The measurement it was standing in for is the interesting one: a NATIVE
// mutex protecting an INTERPRETED local. The two rules meet on one statement —
// the mutex keeps its object identity through bashpp_task.go, and the plain
// `counter` cell is shared by the lexical capture set here — and Go's answer
// is only reproduced if BOTH hold.
func TestGoSourceCaptureNativeMutexSharedLocal(t *testing.T) {
	captureThreeModes(t, `package main

import (
	"fmt"
	"sync"
)

func main() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	counter := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			mu.Lock()
			counter++
			mu.Unlock()
			wg.Done()
		}()
	}
	wg.Wait()
	fmt.Println("counter", counter)
}
`)
}

// TestGoSourceCaptureClassicBashPPDeepCopy is the blast-radius control from the
// other side: a classic Bash++ task is a private copy of the shell, and sharing
// must not reach it. This program is NOT GoSource, so its task must still see
// and leave behind an untouched parent variable.
func TestGoSourceCaptureClassicBashPPDeepCopy(t *testing.T) {
	const src = `counter := 0
go func() {
	counter = 99
}()
wait
echo "counter $counter"
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "classic.sh")
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Runner: %v stderr=%q", err, stderr.String())
	}
	if got := out.String(); got != "counter 0\n" {
		t.Fatalf("classic Bash++ deep copy changed: %q stderr=%q", got, stderr.String())
	}
}
