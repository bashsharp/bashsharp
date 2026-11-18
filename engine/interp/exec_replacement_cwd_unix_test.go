// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const execNonSearchableCWDHelper = "GOSH_EXEC_NONSEARCHABLE_CWD_HELPER"

// TestExecReplacementNonSearchableCwd covers the launch boundary exercised by
// pathchk TP35: a shell enters a directory, removes its search permission, and
// then uses the exec special builtin. The command's absolute path is runnable,
// but entering the shell's logical cwd is not, so exec must diagnose the launch
// failure and exit 126. It must not run from the Go process's stale cwd.
func TestExecReplacementNonSearchableCwd(t *testing.T) {
	if os.Getenv(execNonSearchableCWDHelper) == "1" {
		runExecNonSearchableCWDHelper(t)
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("root can search a directory after all execute bits are removed")
	}

	root := t.TempDir()
	nosearch := filepath.Join(root, "nosearch")
	if err := os.Mkdir(nosearch, 0o755); err != nil {
		t.Fatal(err)
	}
	target, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestExecReplacementNonSearchableCwd$")
	cmd.Dir = root
	childEnv := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOSH_PROG=") || strings.HasPrefix(entry, "GOSH_CMD=") {
			continue
		}
		childEnv = append(childEnv, entry)
	}
	cmd.Env = append(childEnv,
		execNonSearchableCWDHelper+"=1",
		"GOSH_EXEC_NONSEARCHABLE_ROOT="+root,
		"GOSH_EXEC_NONSEARCHABLE_TARGET="+target,
	)
	output, runErr := cmd.CombinedOutput()
	// The helper can be replaced by the target in the regressed case, so the
	// parent owns cleanup of the deliberately non-searchable directory.
	if err := os.Chmod(nosearch, 0o755); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("helper failed: %v\n%s", runErr, output)
	}
	got := string(output)
	if !strings.Contains(got, "exec-status=126") {
		t.Fatalf("exec launch failure was lost; output=%q", got)
	}
	if !strings.Contains(strings.ToLower(got), "permission denied") {
		t.Fatalf("exec launch failure lacked a diagnostic; output=%q", got)
	}
}

func runExecNonSearchableCWDHelper(t *testing.T) {
	root := os.Getenv("GOSH_EXEC_NONSEARCHABLE_ROOT")
	target := os.Getenv("GOSH_EXEC_NONSEARCHABLE_TARGET")
	if root == "" || target == "" {
		t.Fatal("missing exec cwd helper environment")
	}
	src := "cd nosearch\nchmod a-x .\nexec \"$GOSH_EXEC_NONSEARCHABLE_TARGET\"\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "exec-nonsearchable-cwd")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(
		interp.Dir(root),
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
		interp.Params("-o", "posix"),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	runErr := runner.Run(context.Background(), file)
	var status interp.ExitStatus
	if !errors.As(runErr, &status) || status != 126 {
		t.Fatalf("exec status=%v, want 126", runErr)
	}
	fmt.Printf("exec-status=%d\n", status)
}
