//go:build full

package interp_test

// Sprint: #153; Story: S153.4; Story-ID: e58cccba74f8
import (
	"strings"
	"testing"
)

// A refusal recorded inside a callee is the program's outcome wherever the
// callee was called from. (The refusing operation is a retained finalizer
// on an interior pointer, which the interpreter-owned finalizers of Sprint 248
// still refuse — the one bridge refusal S153.2 kept by design; reflect.TypeOf
// of a closure, the original pin, is admitted since S153.2 C1a.) A dependency operation the bridge refuses has no
// native counterpart, so this is pinned by the diagnostic itself rather than
// by a native oracle: the same refusal must surface whether the refusing
// function is called as a statement, as an assignment's producer, or as the
// single call a `return` forwards — the last used to come back as a bare
// "exit status 2".
func TestSprint153FatalDiagnosticSurvivesReturnCall(t *testing.T) {
	const body = `
type Stream struct{}

func Pipe(s Stream) Stream {
	x := new([2]int)
	runtime.SetFinalizer(&x[1], func(p *int) {})
	fmt.Println("unreached")
	return Stream{}
}

func Forward(s Stream) Stream { return Pipe(s) }

func Nested(s Stream) Stream { return Forward(s) }
`
	for name, main := range map[string]string{
		"statement":      `Pipe(Stream{})`,
		"assignment":     `s := Stream{}; s = Pipe(s); _ = s`,
		"return_call":    `s := Stream{}; s = Forward(s); _ = s`,
		"return_nested":  `Nested(Stream{})`,
		"return_in_main": `_ = Nested(Stream{})`,
	} {
		t.Run(name, func(t *testing.T) {
			source := "package main\n\nimport (\n\t\"fmt\"\n\t\"runtime\"\n)\n" + body + "\nfunc main() {\n\t" + main + "\n}\n"
			got := runGoSourceRunnerError(t, source)
			if !strings.Contains(got, "original callback signature requires value-semantics parameters") {
				t.Fatalf("diagnostic lost: %q", got)
			}
		})
	}
}

// A task that stops on a fatal diagnostic reports that diagnostic, not only
// its status: the same refusal a statement reports in the main goroutine
// must be the text of the task's failure.
func TestSprint153FatalDiagnosticSurvivesTask(t *testing.T) {
	const program = `package main

import (
	"fmt"
	"runtime"
)

func main() {
	done := make(chan bool)
	go func() {
		x := new([2]int)
		runtime.SetFinalizer(&x[1], func(p *int) {})
		fmt.Println("unreached")
		done <- true
	}()
	<-done
}
`
	got := runGoSourceRunnerError(t, program)
	if !strings.Contains(got, "task failed: ") || !strings.Contains(got, "original callback signature requires value-semantics parameters") {
		t.Fatalf("diagnostic lost: %q", got)
	}
}
