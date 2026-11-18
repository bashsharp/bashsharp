// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func blocklistOneExec(name string) func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] == name {
				return fmt.Errorf("%s: blocklisted program", name)
			}
			return next(ctx, args)
		}
	}
}

func blocklistAllExec(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		return fmt.Errorf("blocklisted: %s", args[0])
	}
}

func blocklistNondevOpen(ctx context.Context, path string, flags int, mode os.FileMode) (io.ReadWriteCloser, error) {
	if path != "/dev/null" {
		return nil, fmt.Errorf("non-dev: %s", path)
	}

	return interp.DefaultOpenHandler()(ctx, path, flags, mode)
}

func mockFileOpen(ctx context.Context, path string, flags int, mode os.FileMode) (io.ReadWriteCloser, error) {
	return nopWriterCloser{strings.NewReader(fmt.Sprintf("body of %s", path))}, nil
}

func unavailableTTYOpen(ctx context.Context, path string, flags int, mode os.FileMode) (io.ReadWriteCloser, error) {
	if path == "/dev/tty" {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ENXIO}
	}
	return interp.DefaultOpenHandler()(ctx, path, flags, mode)
}

var errTTYUnavailable = errors.New("unavailable")

func unavailableTTYOpenFixedErr(ctx context.Context, path string, flags int, mode os.FileMode) (io.ReadWriteCloser, error) {
	if path == "/dev/tty" {
		return nil, &os.PathError{Op: "open", Path: path, Err: errTTYUnavailable}
	}
	return interp.DefaultOpenHandler()(ctx, path, flags, mode)
}

func blocklistGlob(ctx context.Context, path string) ([]fs.FileInfo, error) {
	return nil, fmt.Errorf("blocklisted: glob")
}

func execPrint(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintf(hc.Stdout, "would run: %s\n", args)
		return nil
	}
}

func TestHandlerContextUmaskTracksBuiltin(t *testing.T) {
	var got []os.FileMode
	capture := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] == "capture-umask" {
				got = append(got, interp.HandlerCtx(ctx).Umask())
				return nil
			}
			return next(ctx, args)
		}
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(
		"capture-umask; umask 077; capture-umask; umask 006; capture-umask"), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.ExecHandlers(capture))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("captured %d masks, want 3", len(got))
	}
	if got[1] != 0o077 || got[2] != 0o006 {
		t.Fatalf("masks after builtin = %#o, %#o; want 0077, 0006", got[1], got[2])
	}
}

// signalIgnoredProbe records one [interp.HandlerContext.SignalIgnored]
// snapshot: the trap-disposition spellings for PIPE alongside the always-off
// cases (an invalid name and the uncatchable/pseudo signals) that must read
// false regardless of trap state.
type signalIgnoredProbe struct {
	bare, sigPrefixed, numeric bool
	invalid, exit, kill, stop  bool
}

func recordSignalIgnoredProbe(hc interp.HandlerContext) signalIgnoredProbe {
	return signalIgnoredProbe{
		bare:        hc.SignalIgnored("PIPE"),
		sigPrefixed: hc.SignalIgnored("SIGPIPE"),
		numeric:     hc.SignalIgnored(strconv.Itoa(int(syscall.SIGPIPE))),
		invalid:     hc.SignalIgnored("NOSUCHSIG"),
		exit:        hc.SignalIgnored("EXIT"),
		kill:        hc.SignalIgnored("KILL"),
		stop:        hc.SignalIgnored("STOP"),
	}
}

// TestHandlerContextSignalIgnored exercises [interp.HandlerContext.SignalIgnored]
// across the trap-disposition states for SIGPIPE: default (no trap),
// explicitly ignored via an empty trap action (checked via the bare,
// SIG-prefixed, and numeric spellings), caught via a non-empty handler, and
// reset via trap-dash. Alongside every probe it also confirms an invalid
// name and the always-uncatchable/pseudo signals (KILL, STOP, EXIT) read
// false regardless of PIPE's own state. It then checks that a subshell
// inherits an ignored disposition, and that the subshell can independently
// reset it.
func TestHandlerContextSignalIgnored(t *testing.T) {
	var got []signalIgnoredProbe
	mw := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] != "probe" {
				return next(ctx, args)
			}
			got = append(got, recordSignalIgnoredProbe(interp.HandlerCtx(ctx)))
			return nil
		}
	}
	script := `
probe                         # 0: default
trap '' PIPE
probe                         # 1: ignored via empty trap action
trap 'echo caught' PIPE
probe                         # 2: caught
trap - PIPE
probe                         # 3: reset
trap '' PIPE
( probe )                     # 4: subshell inherits ignored
( trap - PIPE; probe )        # 5: subshell resets
`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.ExecHandlers(mw))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	ignored := signalIgnoredProbe{bare: true, sigPrefixed: true, numeric: true}
	notIgnored := signalIgnoredProbe{}
	want := []signalIgnoredProbe{notIgnored, ignored, notIgnored, notIgnored, ignored, notIgnored}
	if len(got) != len(want) {
		t.Fatalf("captured %d probes, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("probe[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPipelineDupOutputToInputPreservesPipeType(t *testing.T) {
	t.Parallel()

	pipeTypes := make(chan bool, 1)
	mw := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			switch args[0] {
			case "probe-pipe-stdin":
				f, ok := hc.Stdin.(*os.File)
				if !ok {
					pipeTypes <- false
					return nil
				}
				info, err := f.Stat()
				pipeTypes <- err == nil && info.Mode()&os.ModeNamedPipe != 0
				return nil
			case "drain-stdin":
				_, err := io.Copy(io.Discard, hc.Stdin)
				return err
			}
			return next(ctx, args)
		}
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(
		"(probe-pipe-stdin <&1 >left) | drain-stdin"), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.Dir(t.TempDir()), interp.ExecHandlers(mw))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	select {
	case isPipe := <-pipeTypes:
		if isPipe {
			return
		}
		t.Fatal("probe stdin is not the duplicated pipeline output")
	default:
		t.Fatal("probe was not run after duplicating pipeline output to stdin")
	}
}

// TestHandlerContextSignalIgnoredHardIgnore checks that a signal carried in
// via [interp.BashyHardIgnoreEnv] (the SIG_HARD_IGNORE bridge across an exec
// of our own shell binary, see [interp.Runner.isStartupIgnored]) reports
// ignored from the very start — with no trap installed — and that the
// disposition is inherited by a subshell.
func TestHandlerContextSignalIgnoredHardIgnore(t *testing.T) {
	var got []bool
	mw := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] != "probe" {
				return next(ctx, args)
			}
			got = append(got, interp.HandlerCtx(ctx).SignalIgnored("PIPE"))
			return nil
		}
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("probe; ( probe )"), "")
	if err != nil {
		t.Fatal(err)
	}
	env := expand.ListEnviron(interp.BashyHardIgnoreEnv + "=PIPE")
	runner, err := interp.New(interp.ExecHandlers(mw), interp.Env(env))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	want := []bool{true, true}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hard-ignore probes = %v, want %v", got, want)
	}
}

func execExitStatus5(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		return interp.ExitStatus(5)
	}
}

func execCustomError(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		return fmt.Errorf("custom error")
	}
}

func execCustomExitStatus5(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		return fmt.Errorf("custom error: %w", interp.ExitStatus(5))
	}
}

func execDotRunnerBuiltin(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if name, ok := strings.CutPrefix(args[0], "."); ok {
			hc := interp.HandlerCtx(ctx)
			args[0] = name
			err := hc.Builtin(ctx, args)
			fmt.Fprintf(hc.Stdout, "ran builtin: %s\n", args)
			return err
		}
		return next(ctx, args)
	}
}

// runnerCtx allows us to give handler functions access to the Runner, if needed.
var runnerCtx = new(int)

func execPrintWouldExec(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		runner, ok := ctx.Value(runnerCtx).(*interp.Runner)
		if ok && runner.Exited() {
			return fmt.Errorf("would exec via builtin: %s", args)
		}
		return nil
	}
}

// TODO: join with TestRunnerOpts?
var modCases = []struct {
	name string
	opts []interp.RunnerOption
	src  string
	want string
}{
	{
		name: "ExecBlocklistOne",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(blocklistOneExec("sleep")),
		},
		src:  "echo foo; sleep 1",
		want: "foo\nRunner.Run error: sleep: blocklisted program",
	},
	{
		name: "ExecBlocklistOneSubshell",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(
				blocklistOneExec("faa"),
				testExecHandler, // sed is used below
			),
		},
		src:  "a=$(echo foo | sed 's/o/a/g'); echo $a; $a args",
		want: "faa\nRunner.Run error: faa: blocklisted program",
	},
	{
		name: "ExecBlocklistAllSubshell",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(blocklistAllExec),
		},
		src:  "(malicious)",
		want: "Runner.Run error: blocklisted: malicious",
	},
	{
		name: "ExecPipe",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(blocklistAllExec),
		},
		src:  "malicious | echo foo",
		want: "foo\nRunner.Run error: blocklisted: malicious",
	},
	{
		name: "ExecPipeFail",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(blocklistAllExec),
		},
		src:  "set -o pipefail; malicious | echo foo",
		want: "foo\nRunner.Run error: blocklisted: malicious",
	},
	{
		name: "ExecCmdSubst",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(blocklistAllExec),
		},
		src:  "a=$(malicious)",
		want: "blocklisted: malicious\nRunner.Run error: blocklisted: malicious",
	},
	{
		name: "ExecBackground",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(blocklistAllExec),
		},
		src: "{ malicious; true; } & { malicious; true; } & wait",
		// Note that "wait" with no arguments always succeeds.
		want: "",
	},
	{
		name: "ExecPrintWouldExec",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execPrintWouldExec),
		},
		src:  "exec /bin/sh",
		want: "Runner.Run error: would exec via builtin: [/bin/sh]",
	},
	{
		name: "ExecPrintAndBlocklist",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(
				execPrint,
				blocklistOneExec("foo"),
			),
		},
		src:  "foo",
		want: "would run: [foo]\n",
	},
	{
		name: "ExecPrintAndBlocklistSeparate",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execPrint),
			interp.ExecHandlers(blocklistOneExec("foo")),
		},
		src:  "foo",
		want: "would run: [foo]\n",
	},
	{
		name: "ExecBlocklistAndPrint",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(
				blocklistOneExec("foo"),
				execPrint,
			),
		},
		src:  "foo",
		want: "Runner.Run error: foo: blocklisted program",
	},
	{
		name: "ExecExitStatus5",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execExitStatus5),
		},
		src:  "foo",
		want: "Runner.Run error: exit status 5",
	},
	{
		name: "ExecCustomError",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomError),
		},
		src:  "foo",
		want: "Runner.Run error: custom error",
	},
	{
		name: "ExecCustomErrorContinuation",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomError),
		},
		src:  "foo; echo next",
		want: "Runner.Run error: custom error",
	},
	{
		name: "ExecCustomExitStatus5",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "foo",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Continuation",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "foo; echo next",
		want: "next\n",
	},
	{
		name: "ExecCustomExitStatus5Pipefail",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "set -o pipefail; foo | true",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5ErrExit",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "set -o errexit; foo",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Exec",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "exec foo; echo never-run",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Negated",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "! foo; echo custom-error-wiped; exit 1",
		want: "custom-error-wiped\nRunner.Run error: exit status 1",
	},
	{
		name: "ExecCustomExitStatus5CmdSubst",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "x=$(foo)",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Subshell",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "(foo)",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Wait",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "foo & bg=$!; wait $bg",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Exit",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "foo; exit",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecCustomExitStatus5Source",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execCustomExitStatus5),
		},
		src:  "echo 'foo' >a; source ./a",
		want: "Runner.Run error: custom error: exit status 5",
	},
	{
		name: "ExecDotRunnerBuiltin",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execDotRunnerBuiltin, execExitStatus5),
		},
		src:  ".true; foo; echo $?; .false",
		want: "ran builtin: [true]\n5\nran builtin: [false]\nRunner.Run error: exit status 1",
	},
	{
		name: "ExecDotRunnerBuiltinExiting",
		opts: []interp.RunnerOption{
			interp.ExecHandlers(execDotRunnerBuiltin, execExitStatus5),
		},
		src:  "echo before; .exit 0; echo after",
		want: "before\nran builtin: [exit 0]\n",
	},
	{
		name: "NonExecBuiltin",
		opts: []interp.RunnerOption{
			interp.CallHandler(func(ctx context.Context, args []string) ([]string, error) {
				hc := interp.HandlerCtx(ctx)
				err := hc.Builtin(ctx, append([]string{"echo"}, args...))
				return nil, err
			}),
		},
		src:  "foo; bar",
		want: "Runner.Run error: HandlerContext.Builtin can only be called via an ExecHandlerFunc",
	},
	{
		name: "OpenForbidNonDev",
		opts: []interp.RunnerOption{
			interp.OpenHandler(blocklistNondevOpen),
		},
		src:  "echo foo >/dev/null; echo bar >/tmp/x",
		want: "Runner.Run error: non-dev: /tmp/x",
	},
	{
		name: "OpenMockFile",
		opts: []interp.RunnerOption{
			interp.OpenHandler(mockFileOpen),
		},
		src:  "echo $(<foo); echo $(< <(echo bar))",
		want: "body of foo\nbar\n",
	},
	{
		name: "OpenDevTTYNoTerminalFailsRedirect",
		opts: []interp.RunnerOption{
			interp.OpenHandler(unavailableTTYOpenFixedErr),
		},
		src:  "test -t 0 < /dev/tty; echo $?",
		want: "open /dev/tty: unavailable\n1\n",
	},
	{
		name: "OpenDevTTYNoTerminalFailsRead",
		opts: []interp.RunnerOption{
			interp.OpenHandler(unavailableTTYOpenFixedErr),
		},
		src:  "a=4; read -t 0.000001 a < /dev/tty; echo $? ${a:-unset}",
		want: "open /dev/tty: unavailable\n1 4\n",
	},
	{
		name: "OpenDevTTYNoTerminalLeavesExecFdClosed",
		opts: []interp.RunnerOption{
			interp.OpenHandler(unavailableTTYOpenFixedErr),
		},
		src:  "exec 4</dev/tty; echo exec=$?; read a <&4; echo read=$?",
		want: "open /dev/tty: unavailable\nexec=1\n4: Bad file descriptor\nread=1\n",
	},
	{
		name: "CallReplaceWithBlank",
		opts: []interp.RunnerOption{
			interp.OpenHandler(blocklistNondevOpen),
			interp.CallHandler(func(ctx context.Context, args []string) ([]string, error) {
				return []string{"echo", "blank"}, nil
			}),
		},
		src:  "echo foo >/dev/null; { bar; } && baz",
		want: "blank\nblank\n",
	},
	{
		name: "CallDryRun",
		opts: []interp.RunnerOption{
			interp.CallHandler(func(ctx context.Context, args []string) ([]string, error) {
				return append([]string{"echo", "run:"}, args...), nil
			}),
		},
		src:  "cd some-dir; cat foo; exit 1",
		want: "run: cd some-dir\nrun: cat foo\nrun: exit 1\n",
	},
	{
		name: "CallError",
		opts: []interp.RunnerOption{
			interp.CallHandler(func(ctx context.Context, args []string) ([]string, error) {
				if args[0] == "echo" && len(args) > 2 {
					return nil, fmt.Errorf("refusing to run echo builtin with multiple args")
				}
				return args, nil
			}),
		},
		src:  "echo foo; echo foo bar",
		want: "foo\nRunner.Run error: refusing to run echo builtin with multiple args",
	},
	{
		name: "GlobForbid",
		opts: []interp.RunnerOption{
			interp.ReadDirHandler(blocklistGlob),
		},
		src:  "echo *",
		want: "blocklisted: glob\n",
	},
}

func TestRunnerHandlers(t *testing.T) {
	t.Parallel()

	p := syntax.NewParser()
	for _, tc := range modCases {
		t.Run(tc.name, func(t *testing.T) {
			skipIfUnsupported(t, tc.src)
			file := parse(t, p, tc.src)
			tdir := t.TempDir()
			var cb concBuffer
			r, err := interp.New(interp.Dir(tdir), interp.StdIO(nil, &cb, &cb))
			if err != nil {
				t.Fatal(err)
			}
			for _, opt := range tc.opts {
				opt(r)
			}
			ctx := context.WithValue(context.Background(), runnerCtx, r)
			if err := r.Run(ctx, file); err != nil {
				fmt.Fprintf(&cb, "Runner.Run error: %v", err)
			}
			got := cb.String()
			if got != tc.want {
				t.Fatalf("want:\n%q\ngot:\n%q", tc.want, got)
			}
		})
	}
}

type readyBuffer struct {
	buf       bytes.Buffer
	seenReady sync.WaitGroup
}

func (b *readyBuffer) Write(p []byte) (n int, err error) {
	if string(p) == "ready\n" {
		b.seenReady.Done()
		return len(p), nil
	}
	return b.buf.Write(p)
}

func TestKillTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps and timeouts are slow")
	}
	if runtime.GOOS == "windows" {
		t.Skip("skipping trap tests on windows")
	}
	t.Parallel()

	tests := []struct {
		src         string
		want        string
		killTimeout time.Duration
		forcedKill  bool
	}{
		// killed immediately
		{
			`sh -c "trap 'echo trapped; exit 0' INT; echo ready; i=0; while [ \$i -lt 100 ]; do sleep 0.01; i=\$((i+1)); done"`,
			"",
			-1,
			true,
		},
		// interrupted first, and stops itself in time
		{
			`sh -c "trap 'echo trapped; exit 0' INT; echo ready; i=0; while [ \$i -lt 100 ]; do sleep 0.01; i=\$((i+1)); done"`,
			"trapped\n",
			time.Second,
			false,
		},
		// interrupted first, but does not stop itself in time
		{
			`sh -c "trap 'echo trapped; i=0; while [ \$i -lt 100 ]; do sleep 0.01; i=\$((i+1)); done' INT; echo ready; i=0; while [ \$i -lt 100 ]; do sleep 0.01; i=\$((i+1)); done"`,
			"trapped\n",
			20 * time.Millisecond,
			true,
		},
	}

	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			file := parse(t, nil, test.src)
			attempt := 0
			for {
				var rbuf readyBuffer
				rbuf.seenReady.Add(1)
				ctx, cancel := context.WithCancel(context.Background())
				r, err := interp.New(
					interp.StdIO(nil, &rbuf, &rbuf),
					interp.ExecHandler(interp.DefaultExecHandler(test.killTimeout)),
				)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					rbuf.seenReady.Wait()
					cancel()
				}()
				err = r.Run(ctx, file)
				if test.forcedKill {
					if errors.As(err, new(interp.ExitStatus)) || err == nil {
						t.Error("command was not force-killed")
					}
				} else {
					if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
						t.Errorf("execution errored: %v", err)
					}
				}
				got := rbuf.buf.String()
				if got != test.want {
					if attempt < 3 && got == "" && test.killTimeout > 0 {
						attempt++
						test.killTimeout *= 2
						continue
					}
					t.Fatalf("want:\n%s\ngot:\n%s", test.want, got)
				}
				break
			}
		})
	}
}

func TestKillSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping signal tests on windows")
	}
	tests := []struct {
		signal os.Signal
		want   error
	}{
		{syscall.SIGINT, interp.ExitStatus(130)},  // 128 + 2
		{syscall.SIGKILL, interp.ExitStatus(137)}, // 128 + 9
		{syscall.SIGTERM, interp.ExitStatus(143)}, // 128 + 15
	}

	// pid_and_hang is implemented in TestMain; we use it to have the
	// interpreter spawn a process, and easily grab its PID to send it a
	// signal directly. The program prints its PID and hangs forever.
	file := parse(t, nil, "GOSH_CMD=pid_and_hang $GOSH_PROG")
	for _, test := range tests {
		t.Run(fmt.Sprintf("signal-%d", test.signal), func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			outReader, outWriter := io.Pipe()
			stderr := new(bytes.Buffer)
			r, _ := interp.New(interp.StdIO(nil, outWriter, stderr))
			errch := make(chan error, 1)
			go func() {
				errch <- r.Run(ctx, file)
				outWriter.Close()
			}()

			br := bufio.NewReader(outReader)
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatal(err)
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := proc.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			if got := <-errch; got != test.want {
				t.Fatalf("want error %v, got %v. stderr: %s", test.want, got, stderr)
			}
		})
	}
}

// bracketingOutput mimics a host middleware that holds a command's output
// until its handler returns (an output reducer), with writes made outside
// any command passing straight through. respectAsync makes it skip the
// window for asynchronous-list commands via [interp.HandlerContext.Async].
type bracketingOutput struct {
	respectAsync bool

	mu     sync.Mutex
	active int
	held   bytes.Buffer
	out    bytes.Buffer
	async  map[string]bool
}

func (b *bracketingOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active > 0 {
		return b.held.Write(p)
	}
	return b.out.Write(p)
}

func (b *bracketingOutput) middleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		async := interp.HandlerCtx(ctx).Async()
		b.mu.Lock()
		b.async[args[0]] = async
		b.mu.Unlock()
		if async && b.respectAsync {
			return next(ctx, args)
		}
		b.mu.Lock()
		b.active++
		b.mu.Unlock()
		err := next(ctx, args)
		b.mu.Lock()
		if b.active--; b.active == 0 {
			b.out.Write(b.held.Bytes())
			b.held.Reset()
		}
		b.mu.Unlock()
		return err
	}
}

// TestHandlerContextAsyncOutputOutlivesScript is the regression for output
// written after `$!` going missing when an external background child outlives
// the script: `$!` waits for that child to start, after which a host's output
// window opened for it swallowed every later foreground write. The script
// must print immediately, without the shell waiting for the job at exit,
// exactly like bash in POSIX mode.
func TestHandlerContextAsyncOutputOutlivesScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an external sleep")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no external sleep")
	}
	src := fmt.Sprintf("set -o posix; %s 3 >/dev/null 2>&1 & /usr/bin/env true; echo D $!", sleepPath)
	want := regexp.MustCompile(`^D [0-9]+\n$`)

	if bash, err := exec.LookPath("bash"); err == nil {
		start := time.Now()
		out, err := exec.Command(bash, "--posix", "-c", src).Output()
		if err != nil {
			t.Fatalf("system bash: %v", err)
		}
		if !want.Match(out) || time.Since(start) > 2*time.Second {
			t.Fatalf("system bash printed %q after %v", out, time.Since(start))
		}
	}

	run := func(t *testing.T, respectAsync bool) string {
		b := &bracketingOutput{respectAsync: respectAsync, async: map[string]bool{}}
		file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New(interp.StdIO(nil, b, b), interp.ExecHandlers(b.middleware))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := runner.Run(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Run waited %v for the background job at exit", elapsed)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.async[sleepPath] || b.async["/usr/bin/env"] {
			t.Fatalf("Async() = %v, want true only for the background command", b.async)
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(b.out.String()+b.held.String(), "D "))); err == nil {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
		}
		// Snapshot while holding the lock: killing the job lets its handler
		// finish concurrently and flush the held buffer.
		return b.out.String()
	}
	t.Run("Bracketed", func(t *testing.T) {
		// Control: a window opened for the background command holds the
		// foreground echo, which is what lost the output.
		if out := run(t, false); out != "" {
			t.Fatalf("window did not hold foreground output: %q", out)
		}
	})
	t.Run("RespectAsync", func(t *testing.T) {
		if out := run(t, true); !want.MatchString(out) {
			t.Fatalf("got %q, want D <pid>", out)
		}
	})
}
