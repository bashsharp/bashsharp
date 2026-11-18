//go:build unix

package interp_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestNativeChildShellParentIdentity(t *testing.T) {
	if mode := os.Getenv("PARENT_PID_TEST_MODE"); mode != "" {
		source := `printf '%s' "$PPID"`
		if mode == "owner" {
			source = `(
parent=$(GOSH_PROG= PARENT_PID_TEST_MODE=child "$PARENT_PID_TEST_PROG" -test.run='^TestNativeChildShellParentIdentity$')
kill -s INT "$parent"
kill -s QUIT "$parent"
printf 'sender-survived\n'
) &
wait "$!"
printf 'background-status=%s\n' "$?"
`
		} else if mode == "direct" || mode == "nested" || mode == "primary" || mode == "exec" || mode == "relative" {
			command := `GOSH_PROG= PARENT_PID_TEST_MODE=child "$PARENT_PID_TEST_PROG" -test.run='^TestNativeChildShellParentIdentity$' > child-parent`
			job := "( " + command + "; : ) &"
			want := `"$!"`
			switch mode {
			case "primary":
				job = command + " &"
				want = `"$$"`
			case "exec":
				job = `( GOSH_PROG= PARENT_PID_TEST_MODE=child exec "$PARENT_PID_TEST_PROG" -test.run='^TestNativeChildShellParentIdentity$' > child-parent ) &`
				want = `"$$"`
			case "relative":
				if err := os.Mkdir("subdir", 0o700); err != nil {
					panic(err)
				}
				if err := os.Symlink(os.Getenv("PARENT_PID_TEST_PROG"), "subdir/sh"); err != nil {
					panic(err)
				}
				job = `( cd subdir; GOSH_PROG= PARENT_PID_TEST_MODE=child ./sh -test.run='^TestNativeChildShellParentIdentity$' > ../child-parent; : ) &`
			}
			source = job + "\nwant=" + want + `
wait "$!" || exit
read actual < child-parent || :
test "$actual" = "$want" || { printf 'parent=%s want=%s\n' "$actual" "$want"; exit 1; }
printf 'parent-matches\n'
`
			if mode == "nested" {
				source = "( " + source + " ) &\nwait \"$!\"\n"
			}
		}
		file, err := syntax.NewParser().Parse(strings.NewReader(source), "")
		if err != nil {
			panic(err)
		}
		carrier := &testCarrier{configure: func(cmd *exec.Cmd) {
			// This job ignores INT/QUIT. Keep the native carrier alive for
			// both deliveries, matching the standalone CLI carrier, instead
			// of racing the generic test helper's signal-exit relay.
			cmd.Path = "/bin/sh"
			cmd.Args = []string{"/bin/sh", "-c", `trap '' INT QUIT; printf R; exec cat >/dev/null`}
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
		}}
		r, err := interp.New(interp.StdIO(nil, os.Stdout, os.Stderr), interp.WithJobCarrier(carrier), interp.WithSignalResetter(interp.OSSignalResetter{}))
		if err != nil {
			panic(err)
		}
		err = r.Run(context.Background(), file)
		r.Reset()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"owner", "direct", "nested", "primary", "exec", "relative"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dir := t.TempDir()
			shell := dir + "/sh"
			if err := os.Symlink(exe, shell); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestNativeChildShellParentIdentity$")
			cmd.Dir = dir
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + cmd.Dir, "GOSH_PROG=", "PARENT_PID_TEST_PROG=" + shell, "PARENT_PID_TEST_MODE=" + mode}
			out, err := cmd.CombinedOutput()
			want := "parent-matches\n"
			if mode == "owner" {
				want = "sender-survived\nbackground-status=0\n"
			}
			if err != nil || string(out) != want {
				t.Fatalf("native child parent identity: err=%v output=%q want=%q", err, out, want)
			}
		})
	}
}
