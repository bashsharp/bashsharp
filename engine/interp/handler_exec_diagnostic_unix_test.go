// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !windows

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestDefaultExecHandlerBadInterpreterRelativeToRunnerDir(t *testing.T) {
	working := t.TempDir()
	program := filepath.Join(working, "bad-interpreter")
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
	file, err := syntax.NewParser().Parse(strings.NewReader("./bad-interpreter"), "invocation.tests")
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), file)
	var status interp.ExitStatus
	if !errors.As(err, &status) || status != 126 {
		t.Fatalf("status = %v, want 126; output=%q", err, output.String())
	}
	want := "invocation.tests: ./bad-interpreter: /definitely/missing/interpreter: bad interpreter: No such file or directory\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
