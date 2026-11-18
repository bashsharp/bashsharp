//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceS374FinalizerFunctionPointer(t *testing.T) {
	const source = `package main
import ("fmt"; "runtime"; "sync")
func main() {
	var wg sync.WaitGroup
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			f := func() {}
			runtime.SetFinalizer(&f, func(p *func()) { done <- struct{}{} })
			wg.Done()
		}()
	}
	wg.Wait()
	for i := 0; i < 20; i++ { runtime.GC() }
	for i := 0; i < 10; i++ { <-done; fmt.Print("f") }
}
`
	out, stderr, err := runGoSourceS374(t, source)
	if err != nil || stderr != "" || strings.Count(out, "f") != 10 {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestGoSourceS374GoroutineInterpreterErrorFailsRun(t *testing.T) {
	const source = `package main
import ("runtime"; "sync")
func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		f := func() {}
		runtime.SetFinalizer(&f, func(p *int) {})
		wg.Done()
	}()
	wg.Wait()
}
`
	start := time.Now()
	_, stderr, err := runGoSourceS374(t, source)
	got := stderr
	if err != nil {
		got = err.Error() + got
	}
	if !strings.Contains(got, "original callback signature requires value-semantics parameters") {
		t.Fatalf("missing goroutine diagnostic: %q", got)
	}
	if err == nil || errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("goroutine failure was not prompt/nonzero: duration=%v err=%v", time.Since(start), err)
	}
}

func runGoSourceS374(t *testing.T, source string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return stdout.String(), stderr.String(), err
}
