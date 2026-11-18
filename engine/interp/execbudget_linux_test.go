// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build linux

package interp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Linux normally permits more than Bashy's 1 MiB combined budget. This
// subprocess test proves that the true execve replacement path enforces the
// Bashy budget before replacing the test helper with /bin/true.
func TestExecReplacementHonorsBashyArgMax(t *testing.T) {
	const helper = "BASHY_ARGMAX_EXEC_HELPER"
	if os.Getenv(helper) == "1" {
		entries := []string{"PATH=/bin:/usr/bin"}
		for i := range 11 {
			entries = append(entries, fmt.Sprintf("BIG%d=%s", i, strings.Repeat("x", 100_000)))
		}
		file, err := syntax.NewParser().Parse(strings.NewReader("exec /bin/true\n"), "")
		if err != nil {
			t.Fatal(err)
		}
		r, err := interp.New(interp.Env(expand.ListEnviron(entries...)), interp.StdIO(os.Stdin, os.Stdout, os.Stderr))
		if err != nil {
			t.Fatal(err)
		}
		err = r.Run(context.Background(), file)
		var status interp.ExitStatus
		if !errors.As(err, &status) || status != 126 {
			t.Fatalf("exec status=%v, want 126", err)
		}
		fmt.Println("budget-refused")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecReplacementHonorsBashyArgMax$")
	// Clear GOSH_PROG: TestMain sets it, and a child that inherits it takes
	// the gosh re-exec path instead of running this test.
	cmd.Env = append(os.Environ(), "GOSH_PROG=", helper+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "budget-refused") || !strings.Contains(string(out), "argument list too long") {
		t.Fatalf("exec helper = %v, output=%q", err, out)
	}
}
