//go:build windows

package interp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The scenario S253.0 measured, across a real exec boundary: a parent shell
// exporting both a=bcde and A=AVAR spawns a child shell through os/exec,
// whose Windows path dedupes the environment case-insensitively. The
// BASHY_CASED_ENV bridge must make the child's $a and $A deterministic —
// before it, the dedupe survivor (and so $a) was a coin flip on the
// parent's map iteration order.
func TestCasedEnvAcrossRealExec(t *testing.T) {
	if os.Getenv("INTERP_CASED_ENV_CHILD") == "1" {
		r, err := New(Env(expand.ListEnviron(os.Environ()...)), StdIO(nil, os.Stdout, os.Stderr))
		if err == nil {
			if file, perr := syntax.NewParser().Parse(strings.NewReader("echo probe:$a:$A"), ""); perr == nil {
				r.Run(context.Background(), file)
			}
		}
		os.Exit(0)
	}
	r, err := New(Env(expand.ListEnviron(os.Environ()...)))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("export a=bcde; export A=AVAR"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The pre-bridge failure depended on random ordering; repeat the spawn
	// so a lucky order cannot pass.
	for range 100 {
		env := nativeExecEnv(execEnv(r.writeEnv))
		// TestMain normally turns a re-exec into the interpreter helper.
		// Disable that mode so this process reaches the test child branch.
		env = append(env, "GOSH_PROG=", "GOSH_CMD=", "INTERP_CASED_ENV_CHILD=1")
		cmd := exec.Command(exe, "-test.run", "TestCasedEnvAcrossRealExec$")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child test process: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "probe:bcde:AVAR") {
			t.Fatalf("child shell saw %q; want probe:bcde:AVAR", out)
		}
	}
}
