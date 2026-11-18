// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full && unix

package interp_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceEmbedDirectiveOriginal(t *testing.T) {
	fixture := filepath.Join("..", "lower", "testdata", "gosource-gbe")
	source, err := os.ReadFile(filepath.Join(fixture, "embed-directive.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "embed-directive.go")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"single_file.txt", "file1.hash", "file2.hash"} {
		data, err := os.ReadFile(filepath.Join(fixture, "folder", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "folder", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := runNativeOracle(t, dir, path, nil, "")
	got := runGoSourceRunner(t, dir, path, string(source), nil, "")
	if got != want {
		t.Fatalf("interpreted=%+v oracle=%+v", got, want)
	}
}

const goByExamplePanic = `// A ` + "`panic`" + ` typically means something went unexpectedly
// wrong. Mostly we use it to fail fast on errors that
// shouldn't occur during normal operation, or that we
// aren't prepared to handle gracefully.

package main

import (
	"os"
	"path/filepath"
)

func main() {

	// We'll use panic throughout this site to check for
	// unexpected errors. This is the only program on the
	// site designed to panic.
	panic("a problem")

	// A common use of panic is to abort if a function
	// returns an error value that we don't know how to
	// (or want to) handle. Here's an example of
	// ` + "`panic`" + `king if we get an unexpected error when creating a new file.
	path := filepath.Join(os.TempDir(), "file")
	_, err := os.Create(path)
	if err != nil {
		panic(err)
	}
}
`

func TestGoSourcePanicOriginalTraceBoundary(t *testing.T) {
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(goByExamplePanic))); got != "776fafea232f80b6dd876725afc2f7febe395e928b4bb8985e9c91a91a4cb156" {
		t.Fatalf("original fixture bytes changed: %s", got)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "panic.go")
	if err := os.WriteFile(path, []byte(goByExamplePanic), 0o600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, nil, "")
	got := runGoSourceRunner(t, dir, path, goByExamplePanic, nil, "")
	normalize := func(text string) string {
		lines := strings.SplitAfter(text, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "goroutine ") {
				return strings.Join(lines[:i], "")
			}
		}
		return text
	}
	if got.status != want.status || got.stdout != want.stdout || normalize(got.stderr) != normalize(want.stderr) {
		t.Fatalf("interpreted=%+v oracle=%+v", got, want)
	}
	if !strings.Contains(got.stderr, "\ngoroutine 1 [running]:\nmain.main()\n") {
		t.Fatalf("interpreted panic lost its retained frame: %q", got.stderr)
	}
}

const goByExampleSignals = `// Sometimes we'd like our Go programs to intelligently
// handle [Unix signals](https://en.wikipedia.org/wiki/Unix_signal).
// For example, we might want a server to gracefully
// shutdown when it receives a ` + "`SIGTERM`" + `, or a command-line
// tool to stop processing input if it receives a ` + "`SIGINT`" + `.
// Here's a modern way to handle signals using contexts.

package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
)

func main() {
	// ` + "`signal.NotifyContext`" + ` returns a context that's canceled
	// when one of the listed signals arrives.
	ctx, stop := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The program will wait here until one of the
	// configured signals is received.
	fmt.Println("awaiting signal")
	<-ctx.Done()

	// ` + "`context.Cause`" + ` reports why the context was canceled.
	// For a signal-triggered cancellation, this includes
	// the signal value.
	fmt.Println()
	fmt.Println(context.Cause(ctx))
	fmt.Println("exiting")
}
`

func TestGoSourceSignalProxyOriginalShape(t *testing.T) {
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(goByExampleSignals))); got != "ae3085f94eda7c2199a167f73628efaf8e3e0b17904abace594103a29008a50b" {
		t.Fatalf("original fixture bytes changed: %s", got)
	}
	switch os.Getenv("SH_GOSOURCE_SIGNAL_HELPER") {
	case "program":
		program, err := gosource.Parse(strings.NewReader(goByExampleSignals), filepath.Join(t.TempDir(), "signals.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, os.Stdout, os.Stderr), interp.Params("--"), interp.WithSignalResetter(interp.OSSignalResetter{}))
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(context.Background(), program.File); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "launcher":
		// Match the macOS harness: the launcher ignores SIGINT before exec and
		// the interpreted program remains in its process group. Its native Go
		// helper moves to a separate group and therefore needs the proxy.
		signal.Ignore(syscall.SIGINT)
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestGoSourceSignalProxyOriginalShape$")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "SH_GOSOURCE_SIGNAL_HELPER=") &&
				!strings.HasPrefix(entry, "GOSH_PROG=") && !strings.HasPrefix(entry, "GOSH_CMD=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "SH_GOSOURCE_SIGNAL_HELPER=program")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			t.Fatal(err)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestGoSourceSignalProxyOriginalShape$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOSH_PROG=") && !strings.HasPrefix(entry, "GOSH_CMD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "SH_GOSOURCE_SIGNAL_HELPER=launcher")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer // bashpp-racegate:safe-synchronized os/exec owns the copier until Wait joins it; the only earlier read is the fatal path.
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "awaiting signal\n" {
		t.Fatalf("executable=%q readiness=%q err=%v stderr=%q", executable, line, err, stderr.String())
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder // bashpp-racegate:safe-private the test goroutine is the sole writer.
	output.WriteString(line)
	for {
		line, err = reader.ReadString('\n')
		output.WriteString(line)
		if err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("signal program: %v stdout=%q stderr=%q", err, output.String(), stderr.String())
	}
	if want := "awaiting signal\n\ninterrupt signal received\nexiting\n"; output.String() != want || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), stderr.String())
	}
}
