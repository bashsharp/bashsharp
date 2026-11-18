//go:build linux

package interp_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
	"golang.org/x/sys/unix"
)

func TestRunnerColdStartBeyondPathMax(t *testing.T) {
	const helperEnv = "SH_COLD_LONG_CWD_HELPER"
	const component = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const depth = 60
	if root := os.Getenv(helperEnv); root != "" {
		fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		qt.Assert(t, qt.IsNil(err))
		for range depth {
			if err := unix.Mkdirat(fd, component, 0o755); err != nil && err != unix.EEXIST {
				t.Fatal(err)
			}
			next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.IsNil(unix.Close(fd)))
			fd = next
		}
		qt.Assert(t, qt.IsNil(unix.Fchdir(fd)))
		qt.Assert(t, qt.IsNil(unix.Close(fd)))
		qt.Assert(t, qt.IsNil(os.Unsetenv("PWD")))
		runner, err := interp.New()
		qt.Assert(t, qt.IsNil(err))
		if len(runner.Dir) <= unix.PathMax {
			t.Fatalf("cold-start cwd length = %d, want over PATH_MAX %d", len(runner.Dir), unix.PathMax)
		}
		return
	}

	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunnerColdStartBeyondPathMax$")
	cmd.Env = append(os.Environ(), "GOSH_PROG=", helperEnv+"="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cold-start helper: %v\n%s", err, output)
	}
}

func TestPwdBeyondPathMax(t *testing.T) {
	root := t.TempDir()
	const component = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const depth = 60

	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	qt.Assert(t, qt.IsNil(err))
	defer func() { _ = unix.Close(fd) }()
	for range depth {
		if err := unix.Mkdirat(fd, component, 0o755); err != nil {
			t.Fatal(err)
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNil(unix.Close(fd)))
		fd = next
	}
	qt.Assert(t, qt.IsNil(unix.Mkdirat(fd, "target", 0o755)))
	qt.Assert(t, qt.IsNil(unix.Symlinkat("target", fd, "link")))
	qt.Assert(t, qt.IsNil(unix.Symlinkat(os.Getenv("GOSH_PROG"), fd, "local-gosh")))
	fileFD, err := unix.Openat(fd, "regular", unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, 0o644)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(unix.Close(fileFD)))

	want := root + strings.Repeat(string(filepath.Separator)+component, depth)
	wantTarget := want + string(filepath.Separator) + "target"
	info, err := interp.DefaultStatHandler()(context.Background(), want+string(filepath.Separator)+"regular", true)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(info.Mode().IsRegular()))
	src := fmt.Sprintf(`
i=0
while [ "$i" -lt %d ]; do
	cd %s || exit 70
	i=$((i + 1))
done
pwd -L
pwd -P
printf 'PWD=%%s\n' "$PWD"
GOSH_CMD=getwd "$GOSH_PROG"
GOSH_CMD=getwd ./local-gosh
printf 'GLOB=<%%s>\n' *
printf 'redirect\n' >out.stdout
IFS= read -r line <out.stdout || exit 74
[ "$line" = redirect ] || exit 75
[ -r regular ] || exit 79
[ -w regular ] || exit 80
[ ! -x regular ] || exit 81
./missing-command 2>missing.err
[ "$?" -eq 127 ] || exit 76
IFS= read -r missing <missing.err || exit 77
case $missing in
	*"No such file or directory"*) ;;
	*) exit 78 ;;
esac
cd .. || exit 71
cd %s || exit 72
cd -P link || exit 73
pwd -L
pwd -P
printf 'PWD=%%s\n' "$PWD"
GOSH_CMD=getwd "$GOSH_PROG"
`, depth, component, component)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(src), "")
	qt.Assert(t, qt.IsNil(err))
	var output bytes.Buffer
	runner, err := interp.New(interp.Dir(root), interp.StdIO(nil, &output, &output), interp.WithPosixMode(true), interp.WithBashCompatErrors(true))
	qt.Assert(t, qt.IsNil(err))
	err = runner.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("output: %s", output.String()))
	qt.Assert(t, qt.Equals(output.String(), want+"\n"+want+"\nPWD="+want+"\n"+want+"\n"+want+"\nGLOB=<link>\nGLOB=<local-gosh>\nGLOB=<regular>\nGLOB=<target>\n"+wantTarget+"\n"+wantTarget+"\nPWD="+wantTarget+"\n"+wantTarget+"\n"))
	_, err = os.Stat(want)
	qt.Assert(t, qt.ErrorMatches(err, `.*file name too long.*`))
}
