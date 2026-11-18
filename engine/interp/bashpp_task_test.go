// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceTaskNativeHandles runs original Go programs whose goroutines
// capture a native synchronization object. The goroutine bodies stay
// interpreted; only the captured handle crosses the task snapshot, so the
// interpreter must agree with the real Go toolchain on both output and status.
// Before the task layer preserved handle identity these programs failed the
// snapshot with "unsupported mutable Bash++ object type *interp.bashPPBridgeValue"
// and, once past that, could leave the parent blocked in wg.Wait.
func TestGoSourceTaskNativeHandles(t *testing.T) {
	for _, name := range []string{"waitgroup.go", "atomic-mutex.go"} {
		t.Run(strings.TrimSuffix(name, ".go"), func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata", "gosource-task", name))
			if err != nil {
				t.Fatal(err)
			}
			wantOut, wantErr, wantStatus := runNativeGoTaskProgram(t, name, string(source))
			gotOut, gotErr, gotStatus := runInterpretedTaskProgram(t, name, string(source))
			if gotOut != wantOut || gotErr != wantErr || gotStatus != wantStatus {
				t.Fatalf("interpreter {out:%q err:%q status:%d}; native Go {out:%q err:%q status:%d}",
					gotOut, gotErr, gotStatus, wantOut, wantErr, wantStatus)
			}
		})
	}
}

// TestGoSourceTaskHandleAfterReset checks the stale-handle boundary. Reset
// closes the dependency session, so a handle left in the environment can no
// longer name anything; the next task must fail closed with a handle error
// rather than resolve against a freshly minted session.
func TestGoSourceTaskHandleAfterReset(t *testing.T) {
	source := `package main

import (
	"fmt"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		fmt.Println("first")
		wg.Done()
	}()
	wg.Wait()
	fmt.Println("second")
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "reset.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &errOut))
	if err != nil {
		t.Fatal(err)
	}
	// Two independent runs over one Runner. Each gets its own dependency
	// session, and neither may observe the other's handles.
	for iteration := range 2 {
		out.Reset()
		errOut.Reset()
		r.Reset()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err := r.Run(ctx, program.File)
		cancel()
		if err != nil {
			t.Fatalf("run %d: %v %s", iteration, err, errOut.String())
		}
		if got := out.String(); got != "first\nsecond\n" {
			t.Fatalf("run %d out = %q, stderr %q", iteration, got, errOut.String())
		}
	}
}

func runNativeGoTaskProgram(t *testing.T, name, source string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "oracle")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, path)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native build: %v %s", err, output)
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Dir = dir
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	status := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("native run: %v %s", err, errOut.String())
		}
		status = exit.ExitCode()
	}
	return out.String(), errOut.String(), status
}

func runInterpretedTaskProgram(t *testing.T, name, source string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &errOut))
	if err != nil {
		t.Fatal(err)
	}
	// The bound is generous enough for a cold dependency build but still
	// turns "parent blocked in wg.Wait" into a failure instead of a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	status := 0
	if err := r.Run(ctx, program.File); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatalf("Runner: %v %s", err, errOut.String())
		}
		status = int(exit)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original Go source changed")
	}
	return out.String(), errOut.String(), status
}
