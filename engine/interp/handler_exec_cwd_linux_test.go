// Copyright (c) 2017, Daniel Martí
// See LICENSE for licensing information

//go:build linux

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"golang.org/x/sys/unix"
)

func TestDefaultExecHandlerResolvesRelativeProgramsFromRunnerDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	working := filepath.Join(root, "working")
	if err := os.Mkdir(working, 0o755); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "relative-program")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'ran-relative\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plainProgram := filepath.Join(root, "plain-program")
	if err := os.WriteFile(plainProgram, []byte("printf 'ran-plain\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryProgram := filepath.Join(root, "binary-program")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, binaryProgram); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{name: "explicit-path", src: "command ../relative-program"},
		{name: "relative-PATH-entry", src: "PATH=.. command relative-program"},
		{name: "no-shebang", src: "GOSH_PROG=1 GOSH_CMD=relative_exec_script command ../plain-program"},
		{
			name: "binary-argv0-and-underscore",
			src:  "GOSH_PROG=1 GOSH_CMD=relative_exec_identity command ../binary-program",
			want: "argv0=../binary-program\nunderscore=../binary-program\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			runner, err := interp.New(
				interp.Dir(working),
				interp.StdIO(nil, &output, &output),
				interp.Params("-o", "posix"),
			)
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.src), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatalf("run: %v; output=%q", err, output.String())
			}
			want := tc.want
			if want == "" {
				if tc.name == "no-shebang" {
					want = "ran-plain\n"
				} else {
					want = "ran-relative\n"
				}
			}
			if got := output.String(); got != want {
				t.Fatalf("output=%q, want %q", got, want)
			}
		})
	}
}

func TestDefaultExecHandlerBadInterpreterKeepsShellPath(t *testing.T) {
	root := t.TempDir()
	working := filepath.Join(root, "working")
	if err := os.Mkdir(working, 0o755); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "bad-interpreter")
	if err := os.WriteFile(program, []byte("#!/definitely/missing/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner, err := interp.New(
		interp.Dir(working),
		interp.StdIO(nil, &output, &output),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("../bad-interpreter"), "bad-script")
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), file)
	var status interp.ExitStatus
	if !errors.As(err, &status) || status != 126 {
		t.Fatalf("status = %v, want 126; output=%q", err, output.String())
	}
	want := "bad-script: ../bad-interpreter: /definitely/missing/interpreter: bad interpreter: No such file or directory\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestDefaultExecHandlerExecutesBeyondPathMax(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	working := filepath.Join(root, "working")
	if err := os.Mkdir(working, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(working, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	components := make([]string, 17)
	for index := range components {
		component := fmt.Sprintf("%02d%s", index, strings.Repeat("x", 239))
		components[index] = component
		if err := unix.Mkdirat(fd, component, 0o755); err != nil {
			t.Fatal(err)
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
		fd = next
	}
	if err := unix.Symlinkat(self, fd, "long-binary"); err != nil {
		t.Fatal(err)
	}
	scriptFD, err := unix.Openat(fd, "long-script", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(scriptFD, []byte("printf 'long-script-ok\\n'\n")); err != nil {
		_ = unix.Close(scriptFD)
		t.Fatal(err)
	}
	if err := unix.Close(scriptFD); err != nil {
		t.Fatal(err)
	}
	relativeDir := strings.Join(components, "/")
	if len(relativeDir) <= 4096 {
		t.Fatalf("relative PATH length = %d, want over PATH_MAX", len(relativeDir))
	}

	var output bytes.Buffer
	runner, err := interp.New(
		interp.Dir(working),
		interp.StdIO(nil, &output, &output),
		interp.Params("-o", "posix"),
	)
	if err != nil {
		t.Fatal(err)
	}
	src := "PATH=" + relativeDir + " GOSH_PROG=1 GOSH_CMD=relative_exec_long_identity command long-binary; " +
		"PATH=" + relativeDir + " GOSH_PROG=1 GOSH_CMD=relative_exec_script command long-script"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "long-relative-PATH")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; output=%q", err, output.String())
	}
	if got, want := output.String(), "long-exec-ok\nlong-script-ok\n"; got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
}

func TestDefaultExecHandlerDoesNotLeakRetainedCwdForAbsoluteProgram(t *testing.T) {
	t.Parallel()

	file, err := syntax.NewParser().Parse(strings.NewReader(
		"PATH=/bin command sh -c 'test ! -e /proc/self/fd/3'",
	), "absolute-program-fds")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner, err := interp.New(interp.StdIO(nil, &output, &output), interp.Params("-o", "posix"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("absolute external command inherited private cwd fd: %v; output=%q", err, output.String())
	}
}
