// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestCommandPrefixPreservesExternalCommandIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "identity-program")
	if err := os.Symlink(self, program); err != nil {
		t.Fatal(err)
	}
	src := "PATH=" + dir + ";" +
		" GOSH_PROG=1 GOSH_CMD=relative_exec_identity identity-program;" +
		" GOSH_PROG=1 GOSH_CMD=relative_exec_identity identity-program;" +
		" GOSH_PROG=1 GOSH_CMD=relative_exec_identity command identity-program"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "command-identity")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner, err := interp.New(interp.StdIO(nil, &output, &output), interp.Params("-o", "posix"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; output=%q", err, output.String())
	}
	want := "argv0=identity-program\nunderscore=" + program + "\n"
	if got := output.String(); got != want+want+want {
		t.Fatalf("output=%q, want three identical command results %q", got, want)
	}
}
