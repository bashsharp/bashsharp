// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/bits"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
	"golang.org/x/term"
)

// runnerRunTimeout is the context timeout used by any tests calling [Runner.Run].
// The timeout saves us from hangs or burning too much CPU if there are bugs.
// All the test cases are designed to be inexpensive and stop in a very short
// amount of time, so 5s should be plenty even for busy machines.
const runnerRunTimeout = 5 * time.Second

// Some program which should be in $PATH. Needs to run before runTests is
// initialized (so an init function wouldn't work), because runTest uses it.
var pathProg = func() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sh"
}()

func parse(tb testing.TB, parser *syntax.Parser, src string) *syntax.File {
	if parser == nil {
		parser = syntax.NewParser()
	}
	file, err := parser.Parse(strings.NewReader(src), "")
	if err != nil {
		tb.Fatal(err)
	}
	return file
}

func TestRunnerPosixVerboseSource(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "short",
			src:  "set -v\n",
		},
		{
			name: "long",
			src:  "set -o verbose\n",
		},
	}
	body := "echo 1\necho 2\nif true; then\necho 3\nfi\n"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src + body
			file := parse(t, syntax.NewParser(syntax.Variant(syntax.LangPOSIX)), src)
			var stdout, stderr bytes.Buffer
			r, err := interp.New(
				interp.StdIO(nil, &stdout, &stderr),
				interp.WithBashSource([]byte(src)),
				interp.WithPosixMode(true),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				t.Fatal(err)
			}
			if got, want := stdout.String(), "1\n2\n3\n"; got != want {
				t.Fatalf("stdout mismatch:\n got: %q\nwant: %q", got, want)
			}
			if got := stderr.String(); got != body {
				t.Fatalf("stderr mismatch:\n got: %q\nwant: %q", got, body)
			}
		})
	}
}

func BenchmarkRun(b *testing.B) {
	b.ReportAllocs()

	src := `
echo a b c d
echo ./$foo/etc $(echo foo bar)
foo="bar"
x=y :
fn() {
	local a=b
	for i in 1 2 3; do
		echo $i | cat
	done
}
[[ $foo == bar ]] && fn
echo a{b,c}d *.go
let i=(2 + 3)
`
	file := parse(b, nil, src)
	r, _ := interp.New()
	ctx := context.Background()

	for b.Loop() {
		r.Reset()
		if err := r.Run(ctx, file); err != nil {
			b.Fatal(err)
		}
	}
}

var gnuBash53 string

// isFuzzWorker reports whether this process is a `go test -fuzz` worker. The
// fuzz coordinator re-executes the test binary AFTER TestMain has exported
// GOSH_PROG into the environment, so without this check a worker would take
// the gosh re-exec path below, try to parse its -test.* flags as a shell
// script, and die before fuzzing ("fuzzing process terminated without
// fuzzing").
func isFuzzWorker() bool {
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-test.fuzzworker") {
			return true
		}
	}
	return false
}

func TestMain(m *testing.M) {
	if os.Getenv("GOSH_PROG") != "" && !isFuzzWorker() {
		switch os.Getenv("GOSH_CMD") {
		case "exit_0":
			os.Exit(0)
		case "burn_cpu":
			// A real external child that consumes measurable user CPU, then
			// exits cleanly. Drives TestTimeIssue7ExternalChild, which folds
			// this process's ProcessState CPU through the same seam the exec
			// handler uses for the `time` keyword.
			deadline := time.Now().Add(50 * time.Millisecond)
			x := 1
			for time.Now().Before(deadline) {
				for i := 0; i < 100000; i++ {
					x = (x*1664525 + 1013904223) & 0x7fffffff
				}
			}
			fmt.Fprint(io.Discard, x)
			os.Exit(0)
		case "exit_5":
			os.Exit(5)
		case "print_ok":
			fmt.Printf("exec ok\n")
			os.Exit(0)
		case "print_fail":
			fmt.Printf("exec fail\n")
			os.Exit(1)
		case "relative_exec_identity":
			fmt.Printf("argv0=%s\nunderscore=%s\n", os.Args[0], os.Getenv("_"))
			os.Exit(0)
		case "relative_exec_long_identity":
			if os.Args[0] != "long-binary" || !strings.HasSuffix(os.Getenv("_"), "/long-binary") {
				os.Exit(1)
			}
			fmt.Println("long-exec-ok")
			os.Exit(0)
		case "relative_exec_script":
			if len(os.Args) < 2 {
				os.Exit(2)
			}
			src, err := os.ReadFile(os.Args[1])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			file, err := syntax.NewParser().Parse(bytes.NewReader(src), os.Args[1])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			runner, err := interp.New(interp.StdIO(os.Stdin, os.Stdout, os.Stderr))
			if err != nil || runner.Run(context.Background(), file) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "pid_and_hang":
			fmt.Println(os.Getpid())
			time.Sleep(time.Hour)
			os.Exit(0)
		case "carrier":
			// Job-carrier stand-in for testCarrier: block until stdin
			// reaches EOF (or a signal kills us), then exit cleanly. The
			// stdin pipe ties our lifetime to the test binary's. Mirror the
			// production carrier's explicit INT/QUIT relay and readiness byte.
			carrierSignals := make(chan os.Signal, 1)
			signal.Notify(carrierSignals, syscall.SIGINT, syscall.SIGQUIT)
			go func() {
				sig := <-carrierSignals
				os.Exit(128 + int(sig.(syscall.Signal)))
			}()
			fmt.Print("R")
			io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		case "reflect":
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				fmt.Println(scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "fd8_is_terminal":
			if term.IsTerminal(8) {
				fmt.Println("terminal")
			} else {
				fmt.Println("not-terminal")
			}
			os.Exit(0)
		case "sig_reset_default":
			// Standalone-shell repro for VSC-PCTS TP712: ignore then reset
			// SIGUSR2 with the OS-default reset seam opted in, announce
			// readiness, then block on read. A SIGUSR2 delivered now must
			// terminate us under the restored default disposition. Driven by
			// TestTrapResetRestoresOSDefault.
			const src = "trap '' USR2\ntrap - USR2\necho ready\nread x\n"
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			runner, _ := interp.New(
				interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
				interp.WithSignalResetter(interp.OSSignalResetter{}),
			)
			if err := runner.Run(context.Background(), file); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "sig_default":
			// A separate process group makes terminal-stop defaults observable;
			// an orphaned group is required by POSIX to ignore these signals.
			setTestProcessGroup()
			_, _ = interp.New(interp.WithSignalResetter(interp.OSSignalResetter{}))
			fmt.Println("ready")
			time.Sleep(time.Hour)
			os.Exit(0)
		case "foreground_job_child":
			// Keep running after terminal INTR/QUIT. The PTY job-control tests
			// must distinguish delivery to this foreground command from
			// delivery to the shell which is waiting for it.
			interrupt := make(chan os.Signal, 1)
			signal.Notify(interrupt, syscall.SIGINT, syscall.SIGQUIT)
			fmt.Println("ready")
			<-interrupt
			fmt.Println("caught")
			time.Sleep(300 * time.Millisecond)
			fmt.Println("done")
			os.Exit(0)
		case "foreground_job_shell":
			file, err := syntax.NewParser().Parse(strings.NewReader(
				`set -m; GOSH_CMD=foreground_job_child "$GOSH_PROG"; printf 'PROMPT\n'`), "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			runner, err := interp.New(
				interp.Interactive(true),
				interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
			)
			if err != nil || runner.Run(context.Background(), file) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "job_control_stop_child":
			jobControlStopChild()
			os.Exit(0)
		case "job_control_tty_child":
			jobControlTTYChild()
			os.Exit(0)
		case "job_control_delay":
			time.Sleep(250 * time.Millisecond)
			os.Exit(0)
		case "job_control_bg_shell":
			runJobControlTestShell(`set -m
GOSH_CMD=job_control_stop_child "$GOSH_PROG" &
GOSH_CMD=job_control_delay "$GOSH_PROG"
printf 'JOBS_P='; jobs -p
printf 'JOBS_L='; jobs -l
bg %1
wait %1
printf 'BG_STATUS=%s\n' "$?"
`)
			os.Exit(0)
		case "job_control_fg_shell":
			runJobControlTestShell(`set -m
GOSH_CMD=job_control_tty_child "$GOSH_PROG" &
GOSH_CMD=job_control_delay "$GOSH_PROG"
printf 'JOBS_P='; jobs -p
printf 'JOBS_L='; jobs -l
fg %1
printf 'FG_STATUS=%s\n' "$?"
`)
			jobControlReportShellForeground()
			os.Exit(0)
		case "job_control_redirected_parent":
			jobControlRunRedirectedShell()
			os.Exit(0)
		case "job_control_redirected_shell":
			runJobControlTestShell(`set -m
GOSH_CMD=job_control_delay "$GOSH_PROG" &
fg %%
printf 'REDIRECTED_FG_STATUS=%s\n' "$?"
`)
			os.Exit(0)
		case "foreground_job_shell_unmonitored":
			// First complete an unmonitored asynchronous external job. Its
			// temporary POSIX INT/QUIT ignores must not poison Go's signal
			// bookkeeping before the following foreground command or pipeline.
			foreground := `GOSH_CMD=foreground_job_child "$GOSH_PROG"`
			if os.Getenv("GOSH_PIPELINE") != "" {
				foreground = `/usr/bin/true | ` + foreground
			}
			src := `set +m; GOSH_CMD=exit_0 "$GOSH_PROG" & wait $!; ` +
				foreground + `; printf 'PROMPT\n'`
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			runner, err := interp.New(
				interp.Interactive(true),
				interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
			)
			if err != nil || runner.Run(context.Background(), file) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "bg_self_signal_exec":
			marker := os.Getenv("GOSH_MARKER")
			sig := os.Getenv("GOSH_SIGNAL")
			killCommand := "kill"
			if os.Getenv("GOSH_EXTERNAL_KILL") != "" {
				killCommand = "/bin/kill"
			}
			delay := os.Getenv("GOSH_DELAY")
			if delay != "" {
				delay = "/bin/sleep " + delay + "; "
			}
			duration := "5"
			execCommand := "/bin/sleep " + duration
			if sig == "0" {
				duration = "0.2"
				execCommand = "/bin/sleep " + duration
			} else if sig == "HANDLED" {
				sig = "TERM"
				execCommand = `/bin/sh -c 'trap "exit 7" TERM; while :; do /bin/sleep 1; done'`
			}
			if readyFIFO := os.Getenv("GOSH_READY_FIFO"); readyFIFO != "" {
				delay = fmt.Sprintf("read ready < %q; ", readyFIFO)
				execCommand = fmt.Sprintf(`/bin/sh -c 'printf "ready\n" > "$GOSH_READY_FIFO"; exec /bin/sleep %s'`, duration)
			}
			src := fmt.Sprintf(`(printf S > %q; %s%s -s %s $$; printf '%%s' "$?" >> %q) & exec %s`, marker, delay, killCommand, sig, marker, execCommand)
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			opts := []interp.RunnerOption{
				interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
				interp.WithSignalResetter(interp.OSSignalResetter{}),
			}
			if os.Getenv("GOSH_JOB_CARRIER") != "" {
				opts = append(opts, interp.WithJobCarrier(new(testCarrier)))
			}
			runner, _ := interp.New(opts...)
			if err := runner.Run(context.Background(), file); err != nil {
				var status interp.ExitStatus
				if errors.As(err, &status) {
					os.Exit(int(status))
				}
				os.Exit(1)
			}
			os.Exit(0)
		case "bg_self_signal_before_exec":
			// No replacement exists: terminate the owner promptly even if
			// the asynchronous sender has unbounded work after its kill.
			source := `trap 'echo EXIT-CLEANUP; exit 7' EXIT; (kill -TERM $$; while :; do :; done) & wait`
			if os.Getenv("GOSH_OWNER_MODE") == "alias" {
				source = "trap 'echo EXIT-CLEANUP; exit 7' EXIT\nshopt -s expand_aliases\nalias recovered='(kill -TERM $$; while :; do :; done) & wait'\nrecovered\n"
			}
			file, err := syntax.NewParser().Parse(strings.NewReader(source), "")
			if err != nil {
				os.Exit(2)
			}
			runner, err := interp.New(interp.StdIO(os.Stdin, os.Stdout, os.Stderr), interp.WithSignalResetter(interp.OSSignalResetter{}), interp.WithBashSource([]byte(source)))
			if err != nil {
				os.Exit(2)
			}
			switch os.Getenv("GOSH_OWNER_MODE") {
			case "incremental":
				for _, stmt := range file.Stmts {
					_ = runner.Run(context.Background(), stmt)
				}
			case "alias":
				for _, stmt := range file.Stmts[:3] {
					_ = runner.Run(context.Background(), stmt)
				}
				runner.RunAliasExpandedSourceLine(context.Background(), 4)
			default:
				_ = runner.Run(context.Background(), file)
			}
			os.Exit(1)
		case "bg_unrelated_exec":
			file, err := syntax.NewParser().Parse(strings.NewReader(
				`/bin/sleep 5 & exec /usr/bin/true`), "")
			if err != nil {
				os.Exit(1)
			}
			runner, _ := interp.New(interp.StdIO(os.Stdin, os.Stdout, os.Stderr))
			if err := runner.Run(context.Background(), file); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "proxy_external_signal":
			marker := os.Getenv("GOSH_MARKER")
			src := fmt.Sprintf(`/bin/sleep 1 >/dev/null 2>&1 & exec /usr/bin/env GOSH_CMD=signal_child GOSH_MARKER=%q %q`, marker, os.Args[0])
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				os.Exit(1)
			}
			runner, _ := interp.New(
				interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
				interp.WithSignalResetter(interp.OSSignalResetter{}),
			)
			if err := runner.Run(context.Background(), file); err != nil {
				var status interp.ExitStatus
				if errors.As(err, &status) {
					os.Exit(int(status))
				}
				os.Exit(1)
			}
			os.Exit(0)
		case "signal_child":
			ch := make(chan os.Signal, 1)
			notifyTestUserSignal(ch)
			fmt.Print("ready")
			<-ch
			if err := os.WriteFile(os.Getenv("GOSH_MARKER"), []byte("handled"), 0o600); err != nil {
				os.Exit(1)
			}
			os.Exit(7)
		case "foo_null_bar":
			fmt.Println("foo\x00bar")
			os.Exit(0)
		case "lookpath":
			_, err := exec.LookPath(pathProg)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			fmt.Printf("%s found\n", pathProg)
			os.Exit(0)
		case "getwd":
			wd, err := os.Getwd()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println(wd)
			os.Exit(0)
		}
		r := strings.NewReader(os.Args[1])
		file, err := syntax.NewParser().Parse(r, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		runner, _ := interp.New(
			interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
			interp.ExecHandlers(testExecHandler),
			// TestRunnerTerminalExecInheritedSparseFD passes FD 8 through
			// this re-exec boundary, as the standalone shell does.
			interp.WithInheritedFds(func() []int {
				if os.Getenv(interp.BashyInheritedFdsEnv) == "8" {
					return []int{8}
				}
				return nil
			}()),
		)
		ctx := context.Background()
		if err := runner.Run(ctx, file); err != nil {
			var es interp.ExitStatus
			if errors.As(err, &es) {
				os.Exit(int(es))
			}

			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	prog, err := os.Executable()
	if err != nil {
		panic(err)
	}
	os.Setenv("GOSH_PROG", prog)

	internal.TestMainSetup()

	gnuBash53 = findGNUBash53()

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	os.Setenv("GO_TEST_DIR", wd)

	os.Setenv("INTERP_GLOBAL", "value")
	os.Setenv("MULTILINE_INTERP_GLOBAL", "\nwith\nnewlines\n\n")

	// Double check that env vars on Windows are case insensitive.
	if runtime.GOOS == "windows" {
		os.Setenv("mixedCase_INTERP_GLOBAL", "value")
	} else {
		os.Setenv("MIXEDCASE_INTERP_GLOBAL", "value")
	}

	os.Setenv("PATH_PROG", pathProg)

	// To print env vars. Only a builtin on Windows.
	if runtime.GOOS == "windows" {
		os.Setenv("ENV_PROG", "cmd /c set")
	} else {
		os.Setenv("ENV_PROG", "env")
	}

	// Clean up any env pollution from the parent process that might
	// leak into tests (e.g. a=(1 2 3 4 5) from a prior shell session).
	os.Unsetenv("a")

	m.Run()
}

func runJobControlTestShell(src string) {
	file, err := syntax.NewParser(syntax.PosixMode(true)).Parse(strings.NewReader(src), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runner, err := interp.New(
		interp.Interactive(true),
		interp.WithPosixMode(true),
		interp.WithStrictPosix(true),
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		var status interp.ExitStatus
		if errors.As(err, &status) {
			os.Exit(int(status))
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func findGNUBash53() string {
	seen := make(map[string]bool)
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs,
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	)
	for _, dir := range dirs {
		for _, name := range []string{"bash", "bash.exe"} {
			path := filepath.Join(dir, name)
			if seen[path] {
				continue
			}
			seen[path] = true
			out, err := exec.Command(path, "--version").CombinedOutput()
			if err != nil {
				continue
			}
			version := string(out)
			if strings.Contains(version, "GNU bash, version 5.3") && !strings.Contains(version, "bashy") {
				return path
			}
		}
	}
	return ""
}

// concBuffer wraps a [bytes.Buffer] in a mutex so that concurrent writes
// to it don't upset the race detector.
type concBuffer struct {
	buf bytes.Buffer
	sync.Mutex
}

func (c *concBuffer) Write(p []byte) (int, error) {
	c.Lock()
	n, err := c.buf.Write(p)
	c.Unlock()
	return n, err
}

func (c *concBuffer) WriteString(s string) (int, error) {
	c.Lock()
	n, err := c.buf.WriteString(s)
	c.Unlock()
	return n, err
}

func (c *concBuffer) String() string {
	c.Lock()
	s := c.buf.String()
	c.Unlock()
	return s
}

func (c *concBuffer) Reset() {
	c.Lock()
	c.buf.Reset()
	c.Unlock()
}

type runTest struct {
	in, want string
}

var runTests = []runTest{
	// no-op programs
	{"", ""},
	{"true", ""},
	{":", ""},
	{"exit", ""},
	{"exit 0", ""},
	{"{ :; }", ""},
	{"(:)", ""},

	// exit status codes
	{"exit 1", "exit status 1"},
	{"exit -1", "exit status 255"},
	{"exit 300", "exit status 44"},
	{"false", "exit status 1"},
	{"false foo", "exit status 1"},
	{"! false", ""},
	{"true foo", ""},
	{": foo", ""},
	{"! true", "exit status 1"},
	{"false; true", ""},
	{"false; exit", "exit status 1"},
	{"exit; echo foo", ""},
	{"exit 0; echo foo", ""},
	{"exit -- 56", "exit status 56"},
	{"exit --", ""},
	{"trap '(exit 1); exit' EXIT; (exit 2); exit", "exit status 2"},
	{"trap '(exit 1)' EXIT; (exit 2)", "exit status 2"},
	{"set -e; ! false | false | true ; ! true | true | false ; echo reached", "reached\n"},
	// A return in a pipeline's subshell controls that pipeline's status, but
	// must not return from the enclosing function in the parent shell.
	{"f() { echo foo | return 5; echo pipeline=$?; }; f; echo alive", "pipeline=5\nalive\n"},
	// With lastpipe enabled, bash runs the final pipeline component in the
	// current shell, so an assignment-only component persists afterwards.
	{"shopt -s lastpipe; unset bar; echo g h i | bar=7; echo after: $bar", "after: 7\n"},
	{"shopt -s lastpipe; unset foo bar; echo a b c | read foo; echo after 1: foo = $foo; unset tot; declare -i tot; printf '%d\\n' 1 2 3 | while read foo; do tot+=$foo; done; echo after 2: tot = $tot; unset bar; echo g h i | bar=7; echo after: $bar", "after 1: foo = a b c\nafter 2: tot = 6\nafter: 7\n"},
	{"printf", "usage: printf [-v var] format [arguments]\nexit status 2 #JUSTERR"},
	{"break", "break: only meaningful in a `for', `while', or `until' loop\n #JUSTERR"},
	{"continue", "continue: only meaningful in a `for', `while', or `until' loop\n #JUSTERR"},
	// break/continue used in a loop CONDITION act on the loop (not an error).
	{"while break; do echo x; done; echo done", "done\n"},
	{"until break; do echo x; done; echo done", "done\n"},
	{"for i in 1 2 3; do echo i=$i; while break; do echo no; done; done", "i=1\ni=2\ni=3\n"},
	{"set -o posix; for i in 1; do break && echo bad; echo bad; done; echo after", "after\n"},
	{"set -o posix; for i in 1; do if break; then echo bad; fi; echo bad; done; echo after", "after\n"},
	{"set -o posix; for i in 1; do if true; then break; echo bad; fi; echo bad; done; echo after", "after\n"},
	{"set -o posix; for i in 1; do if false; then :; else break; echo bad; fi; echo bad; done; echo after", "after\n"},
	{"set -o posix; for i in 1; do case x in x) break; echo bad;; esac; echo bad; done; echo after", "after\n"},
	{"set -o posix; for i in 1 2; do echo $i; continue && echo bad; echo bad; done; echo after", "1\n2\nafter\n"},
	{"set -o posix; for i in 1 2; do echo $i; if continue; then echo bad; fi; echo bad; done; echo after", "1\n2\nafter\n"},
	{"set -o posix; for i in 1 2; do echo $i; if true; then continue; echo bad; fi; echo bad; done; echo after", "1\n2\nafter\n"},
	{"set -o posix; for i in 1 2; do echo $i; if false; then :; else continue; echo bad; fi; echo bad; done; echo after", "1\n2\nafter\n"},
	{"set -o posix; for i in 1 2; do echo $i; case x in x) continue; echo bad;; esac; echo bad; done; echo after", "1\n2\nafter\n"},
	{"cd a b", "cd: too many arguments\nexit status 2 #JUSTERR"},
	// `cd --` is an end-of-options marker, not an extra operand.
	{"cd -- /; echo $PWD", "/\n"},
	// printf: '#' flag adds no 0x/0X prefix for a zero value; lone quote is 0.
	{"printf '[%#x][%#x][%#X][%#o]' 0 42 0 0", "[0][0x2a][0][0]"},
	{"printf '%d %d' \\' \\\"", "0 0"},
	{"printf '%d' \\'A", "65"},
	// the `0` flag is ignored for string conversions (%s/%b): space-pad,
	// never zero-pad and never before a sign; numeric keeps the zero flag.
	{"printf '[%06s][%06s][%06.2s][%06b]' 42 -42 abcd hi", "[    42][   -42][    ab][    hi]"},
	{"printf '[%06d][%06d]' 42 -42", "[000042][-00042]"},
	// printf %d strtol-style parsing: longest prefix, leading-ws skip, trailing
	// or no-digits -> "invalid number" (value = prefix or 0), overflow -> clamp.
	{"printf '%d' 3abc", "printf: 3abc: invalid number\n3exit status 1 #JUSTERR"},
	{"printf '%d' ' -42'", "-42"},
	{"printf '%d' abc", "printf: abc: invalid number\n0exit status 1 #JUSTERR"},
	{"printf '%d' 18446744073709551615", "printf: 18446744073709551615: Result not representable\n9223372036854775807exit status 1 #JUSTERR"},
	// A `*` width/precision argument is read like getint in bash's printf.def:
	// strtoimax base 0, a quote is the character's code, trailing junk is
	// "invalid number" keeping the converted prefix (status 1), a value
	// outside int is strerror(ERANGE) and the width is dropped, a negative
	// precision is as if omitted (printf7.sub).
	{"printf '[%*s]\\n' 12abc X", "printf: 12abc: invalid number\n[           X]\nexit status 1 #JUSTERR"},
	{"printf '[%.*s]\\n' abc XYZ", "printf: abc: invalid number\n[]\nexit status 1 #JUSTERR"},
	{"printf '[%*s]\\n' '' X", "printf: : invalid number\n[X]\nexit status 1 #JUSTERR"},
	{"printf '[%*s][%*s][%*s]\\n' 0x10 X 010 Y \"'A\" Z", "[               X][       Y][                                                                Z]\n"},
	{"printf '[%*s]\\n' 21474836470 X", "printf: 21474836470: Numerical result out of range\n[X]\nexit status 1 #JUSTERR"},
	{"printf '[%.*s]\\n' 99999999999999999999 X", "printf: 99999999999999999999: Numerical result out of range\n[X]\nexit status 1 #JUSTERR"},
	{"printf '[%.*s][%*s][%-*s]\\n' -3 XYZ -5 X 5 X", "[XYZ][X    ][X    ]\n"},
	// The fixture's own path to line 62: INT_MAX read as a non-number
	// ("undefined") makes $(( INT_MAX * 10 )) zero, and zero is no overflow.
	{"INT_MAX=undefined; TOOBIG=$(( $INT_MAX * 10 )); printf '[%.*s]\\n' \"$TOOBIG\" X; echo $?", "[]\n0\n"},
	// getopts: missing required arg reports '?' in verbose mode (no leading ':'
	// in optstring), ':' only in silent mode.
	{"set -- -a; getopts 'a:' o 2>/dev/null; echo \"$o/$OPTARG\"", "?/\n"},
	{"set -- -a; getopts ':a:' o; echo \"$o/$OPTARG\"", ":/a\n"},
	// backslash-newline line continuation splicing a $? (special-param) token.
	{"echo $\\\n?", "0\n"},
	{"x=hi; echo $\\\n{x}", "hi\n"},
	// test/[ : a lone `(` operand is the 1-arg form (literal non-empty string,
	// true), not an unterminated group.
	{"[ '(' ] && echo t", "t\n"},
	{"[ '(' -e / ')' ] && echo grp", "grp\n"},
	// set -e: the &&/|| exemption (failing LEFT operand) propagates through a
	// brace group `{ }` (same env), so these don't exit — matches bash.
	{"set -e; { test x = y && echo hi; }; echo after", "after\n"},
	// a lone `+` ends options like `-` (must not panic on the 1-char flag).
	{"set a b c; set + x y; echo \"$@\"", "x y\n"},
	{"set + -; echo ok", "ok\n"},
	// declare/typeset string-parsed `+=` append form (`typeset 'sx+=bar'`).
	{"sx=foo; typeset 'sx+=bar'; echo $sx", "foobar\n"},
	{"declare -i k=5; declare 'k+=3'; echo $k", "8\n"},
	// subscript-unsetting a declared (set or unset) scalar is an error;
	// a truly undeclared name is a no-op.
	{"declare undef; unset -v 'undef[1]'", "unset: undef: not an array variable\nexit status 1 #JUSTERR"},
	{"unset -v 'never[1]'; echo $?", "0\n"},
	{"set -e; { false && true; }; echo after", "after\n"},
	{"set -e; { { false && true; }; }; echo nested", "nested\n"},
	{"set -e; if true; then false && true; fi; echo reached", "reached\n"},
	// ...and through for/while/until/case compound commands whose final
	// body command is the errexit-exempt left operand of `&&`/`||`.
	{"set -e; for i in a; do false && :; done; echo reached", "reached\n"},
	{"set -e; while :; do false && :; break; done; echo reached", "reached\n"},
	{"set -e; case x in x) false && : ;; esac; echo reached", "reached\n"},
	// but a genuinely-failing final body command still exits under set -e.
	{"set -e; for i in a; do false; done; echo reached", "exit status 1"},
	{"set -e; case x in x) false ;; esac; echo reached", "exit status 1"},
	// $? inside a case/for body reflects the last command substitution in
	// the subject / `in` word until the body runs a command (VSC-PCTS #352).
	{"case $(echo a; exit 99) in a) echo $? ;; esac", "99\n"},
	{`case $(exit 5) in "") echo $? ;; esac`, "5\n"},
	{"for i in $(echo x; exit 33); do echo $?; break; done", "33\n"},
	{"for i in $(echo x y; exit 5); do echo $?; done", "5\n0\n"},
	{"false; case x in x) echo $? ;; esac", "1\n"}, // no cmdsubst: prior $?
	// tilde expansion in a ${param:+word} alternate word (like the :- default).
	{"HOME=/h; echo ${HOME:+~/z}", "/h/z\n"},
	{"HOME=/h; echo \"${HOME:+~/z}\"", "~/z\n"},
	// tilde login-name terminates at ':' too (bash extension), word + assign;
	// a ':' after the '/' stays in the path.
	{"HOME=/h; echo ~:x", "/h:x\n"},
	{"HOME=/h; v=~:x; echo \"$v\"", "/h:x\n"},
	{"HOME=/h; echo ~/p:q", "/h/p:q\n"},
	// declare-family string-parsed arg: a ~ (or glob) from a parameter expansion
	// must NOT be re-expanded; only a literal name=value assignment tilde-expands.
	{"HOME=/h; b='c=~/src'; readonly \"$b\"; echo $c", "~/src\n"},
	{"HOME=/h; b='c=~/src'; export \"$b\"; echo $c", "~/src\n"},
	{"HOME=/h; readonly d=~/x; echo $d", "/h/x\n"},
	{"HOME=/h; b='x=g*'; declare \"$b\"; echo \"$x\"", "g*\n"},
	// the substitute word of a default/alternate/assign expansion is evaluated
	// ONLY when used — no side effects leak when the parameter is set.
	{"i=0; x=x; echo ${x:-$((i++))}; echo $i; echo ${undef:-$((i++))}; echo $i", "x\n0\n0\n1\n"},
	{"i=0; v=set; echo ${v:+$((i++))}; echo $i", "0\n1\n"},
	{"shift a", "shift: a: numeric argument required\nexit status 2 #JUSTERR"},
	{"shift 1 2", "shift: too many arguments\nexit status 2 #JUSTERR"},
	{"shift -1", "shift: -1: shift count out of range\nexit status 1 #JUSTERR"},
	{"shift -- -4", "shift: -4: shift count out of range\nexit status 1 #JUSTERR"},
	{"shopt -s shift_verbose; shift -1", "shift: -1: shift count out of range\nexit status 1 #JUSTERR"},
	{"history abcde", "history: abcde: numeric argument required\nexit status 2 #JUSTERR"},
	{"history 10 42", "history: too many arguments\nexit status 2 #JUSTERR"},
	{"eval 'a=(1 \"two words\")'; printf '<%s>\\n' \"${a[@]}\"", "<1>\n<two words>\n"},
	{
		"shouldnotexist",
		"\"shouldnotexist\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	{
		"for i in 1; do continue a; done",
		"continue: a: numeric argument required\nexit status 128 #JUSTERR",
	},
	{
		"for i in 1; do break a; done",
		"break: a: numeric argument required\nexit status 128 #JUSTERR",
	},
	{
		"for i in 1; do break 1 2; done",
		"break: too many arguments\nexit status 2 #JUSTERR",
	},
	{
		"for i in 1; do continue 1 2; done",
		"continue: too many arguments\nexit status 2 #JUSTERR",
	},
	{
		"for i in a b c; do echo $i; continue 1 2 3; done; echo --",
		"a\nbash: line 1: continue: too many arguments\nexit status 2",
	},
	{
		"echo pipestatus ${PIPESTATUS[@]}\nfalse\necho pipestatus ${PIPESTATUS[@]}\nexit 55 | (exit 44)\necho pipestatus ${PIPESTATUS[@]}\ntrue\necho pipestatus ${PIPESTATUS[@]}",
		"pipestatus\npipestatus 1\npipestatus 55 44\npipestatus 0\n",
	},
	{"false; a=b", ""},
	{"false; false &", ""},
	{
		"GOSH_CMD=exit_0 $GOSH_PROG; echo next",
		"next\n",
	},
	{
		"GOSH_CMD=exit_5 $GOSH_PROG; echo next",
		"next\n",
	},
	{
		"! GOSH_CMD=exit_0 $GOSH_PROG",
		"exit status 1",
	},
	{
		"! GOSH_CMD=exit_5 $GOSH_PROG",
		"",
	},

	// we don't need to follow bash error strings
	{"exit a", "exit: a: numeric argument required\nexit status 2 #JUSTERR"},
	{"exit 1 2", "exit: too many arguments\nexit status 2 #JUSTERR"},
	{"f() { return a; }; f", "return: a: numeric argument required\nexit status 2 #JUSTERR"},
	{"f() { return a; echo bad; }; f; echo after:$?", "return: a: numeric argument required\nafter:2\n #JUSTERR"},
	{"f() { return -- 56; echo bad; }; f", "exit status 56"},
	{"return 1 2", "return: too many arguments\nexit status 2 #JUSTERR"},

	// echo
	{"echo", "\n"},
	{"echo a b c", "a b c\n"},
	{"echo -n foo", "foo"},
	{`echo -e '\t'`, "\t\n"},
	{`echo -E '\t'`, "\\t\n"},
	{`echo -e 'before\x00after'`, "before\x00after\n"},
	{`echo -e '\x'`, "\\x\n"},
	{"echo -x foo", "-x foo\n"},
	{"echo -e -x -e foo", "-x -e foo\n"},

	// printf
	{"printf foo", "foo"},
	{"printf %%", "%"},
	{"printf %", "printf: `%': missing format character\nexit status 1 #JUSTERR"},
	{"printf '%z'", "printf: `%z': missing format character\nexit status 1 #JUSTERR"},
	{"printf %; echo foo", "printf: `%': missing format character\nfoo\n #IGNORE"},

	// printf -v: assign formatted output to the named variable instead
	// of writing to stdout.
	{"printf -v out 'x=%d' 7; echo $out", "x=7\n"},
	{"printf -v out '%s\\n' hi; echo \"$out\" | wc -c | tr -d ' '", "4\n"},
	{"declare -A A; printf -v 'A[ ]' '%s' X; declare -p A", "declare -A A=([\" \"]=\"X\" )\n"},
	{"printf -v 1bad 'x'", "printf: \"1bad\": not a valid identifier\nexit status 1 #JUSTERR"},
	{"printf -v", "printf: -v: option requires an argument\nexit status 2 #JUSTERR"},

	// printf %b: interpret backslash escapes inside the *argument*
	// (not the format string), so `printf '%b\n' 'a\tb'` outputs a
	// literal tab.
	{`printf '%b' 'foo\nbar'`, "foo\nbar"},
	{`printf '%b\n' 'a\tb'`, "a\tb\n"},
	{`printf '\0007'`, "\x007"},
	{`printf '%b' '\0007'`, "\a"},
	{`printf '\0200'`, "\x100"},
	{`printf '%b' '\0200'`, "\x80"},

	// printf %(fmt)T: strftime-style datetime. Year and Unix-time
	// specifiers are timezone-stable for fixed timestamps.
	{`printf '%(%Y)T\n' 1700000000`, "2023\n"},
	{`printf '%(%s)T\n' 1700000000`, "1700000000\n"},
	{`TZ=EST5EDT,M3.2.0/2,M11.1.0/2 printf '%()T %(%x %X)T\n' 1275250155 1275246555`, "16:09:15 05/30/10 15:09:15\n"},
	{`TZ=EST5EDT,M3.2.0/2,M11.1.0/2 printf '%-12.20(%H:%M:%S)T!\n' 1275250155`, "16:09:15    !\n"},
	{`TZ=EST5EDT,M3.2.0/2,M11.1.0/2 printf '%.50(%x (foo) %X)T\n' 1275250155`, "05/30/10 (foo) 16:09:15\n"},
	{`printf '%(abde)Z\n'`, "printf: warning: `Z': invalid time format specification\n%(abde)Z\nexit status 1 #JUSTERR"},
	{`printf '%(%%)T' 1700000000`, "%"},
	// Unknown specifier passes through verbatim, matching bash.
	{`printf '%(%q)T\n' 1700000000`, "%q\n"},

	// printf -- ends option parsing so a format starting with - works.
	{`printf -- '-x: %s\n' world`, "-x: world\n"},

	// printf: a format operand beginning with '+' is not an option.
	{"printf +3", "+3"},
	{"printf +2", "+2"},
	{"printf +1", "+1"},
	{`printf +\`, `+\`},
	{`printf '+3: %s\n' world`, "+3: world\n"},
	{"printf +%s foo", "+foo"},
	{"printf -v out +3; echo \"$out\"", "+3\n"},
	{`printf -v out '+3: %s\n' world; echo "$out"`, "+3: world\n\n"},
	{`printf -- '+3: %s\n' world`, "+3: world\n"},
	{"printf -v out -- +3; echo \"$out\"", "+3\n"},
	// Invalid minus options are still rejected with the usual diagnostic.
	{"printf -x", "printf: invalid option \"-x\"\nexit status 2 #JUSTERR"},
	{"printf -3", "printf: invalid option \"-3\"\nexit status 2 #JUSTERR"},

	// bash 5.3 funsub ${ cmd; }: runs body in caller's scope (no
	// subshell), captures stdout. Distinct from $(...) which subshells.
	{`v=${ echo hi; }; echo "$v"`, "hi\n"},
	{`v=${ echo a; echo b; }; echo "[$v]"`, "[a\nb]\n"},
	// Same process and caller scope for ordinary assignments. Note the
	// contrast with $(...) on the line after, which subshells.
	{`x=before; v=${ x=after; echo cap; }; echo "$v $x"`, "cap after\n"},
	{`x=before; v=$(x=after; echo cap); echo "$v $x"`, "cap before\n"},
	// Fresh variables assigned inside the funsub also persist.
	{`v=${ newvar=hello; echo cap; }; echo "v=$v newvar=${newvar-unset}"`, "v=cap newvar=hello\n"},
	// `local` inside the funsub body is legal (the body is function-scoped).
	{`x=outer; v=${ local x=inner; echo "in=$x"; }; echo "$v out=$x"`, "in=inner out=outer\n"},
	// Multiple unrelated assignments in the body leak unless local.
	{`a=1; b=2; v=${ a=99; b=99; c=99; echo cap; }; echo "$v $a $b ${c-u}"`, "cap 99 99 99\n"},
	// bash 5.3 valsub ${|cmd;}: stdout passes through; the expansion
	// value comes from REPLY.
	{`v=${| echo hello; REPLY=value; }; echo "v=$v REPLY=$REPLY"`, "hello\nv=value REPLY=\n"},
	{`v=${| REPLY=$'a\n\n'; }; printf '<%s>\n' "$v"`, "<a\n\n>\n"},
	// `return` inside funsub is local to the body (like a function);
	// `exit` propagates out (kills the shell). Mirrors bash 5.3.
	{`v=${ echo a; return; echo b; }; echo "[$v]"`, "[a]\n"},
	{`v=${ echo a; exit 2; echo b; }; echo never`, "exit status 2"},
	{`set -e; v=${ echo a; false; echo b; }; echo "[$v]"`, "[a\nb]\n"},
	{`set -e -o posix; v=${ echo a; false; echo b; }; echo "[$v]"`, "exit status 1"},
	// `exit 0` from a funsub also propagates — without preserving the
	// funsub's exit status as lastExpandExit, the assignment path's
	// "restore lastExpandExit on success" recovery would silently
	// clear the exiting flag.
	{`v=${ exit 0; }; echo never`, ""},
	// `break` inside funsub breaks the enclosing loop.
	{`for i in 1 2 3; do v=${ break; }; echo "i=$i"; done; echo done`, "done\n"},
	// positional-parameter changes (shift, set --) are NOT variable
	// assignments per bash 5.3 funsub spec, so they DO leak from a funsub
	// body. Wrapping the body in (...) subshells the call and isolates
	// them. Mirrors bash 5.3 comsub2.tests lines 117-130.
	{`set -- 1 2; : "${ shift; }"; echo "$@"`, "2\n"},
	{`set -- 1 2; : "${| shift; }"; echo "$@"`, "2\n"},
	{`set -- 1 2; : "${ ( shift ); }"; echo "$@"`, "1 2\n"},

	// runner-state introspection builtin emits JSON; check it round-trips
	// by extracting a known key via grep -q.
	{`f(){ :; }; runner-state funcs | grep -q '"funcs":\[.*"f"' && echo ok`, "ok\n #IGNORE S275 decision: runner-state is a bashy builtin with no GNU Bash counterpart"},
	{`runner-state bogus`, "runner-state: unknown section \"bogus\" (try: vars opts traps fds funcs callstack all)\nexit status 2 #JUSTERR"},
	{"printf %1", "printf: `%1': missing format character\nexit status 1 #JUSTERR"},
	{"printf %+", "printf: `%+': missing format character\nexit status 1 #JUSTERR"},
	{"printf %B foo", "printf: `B': invalid format character\nexit status 1 #JUSTERR"},
	{"printf 'ab%Mcd\n'; printf '%y' 0", "printf: `M': invalid format character\nabprintf: `y': invalid format character\nexit status 1 #JUSTERR"},
	{"printf %12-s foo", "printf: `-': invalid format character\nexit status 1 #JUSTERR"},
	{"printf ' %s \n' bar", " bar \n"},
	{"printf '\\A'", "\\A"},
	{"printf %s foo", "foo"},
	{"printf %s", ""},
	{"printf %d,%i 3 4", "3,4"},
	{"printf %d", "0"},
	{"printf '%d' ''", "printf: : invalid number\n0exit status 1 #JUSTERR"},
	{"printf '%d' 2>/dev/null", "0"},
	{"printf %d,%d 010 0x10", "8,16"},
	{"printf '%u' 18446744073709551615", "18446744073709551615"},
	{"printf '%u %o %x %X' -1 -1 -1 -1", "18446744073709551615 1777777777777777777777 ffffffffffffffff FFFFFFFFFFFFFFFF"},
	{"printf %c,%c,%c foo àa", "f,\xc3,\x00"}, // TODO: use a rune?
	{"printf '%2c\\n' 65", " 6\n"},
	{"printf '%-2c--\\n' 65", "6 --\n"},
	{"printf %3s a", "  a"},
	{"printf '%#q\\n' no-quotes-needed 'quotes;needed'", "'no-quotes-needed'\n'quotes;needed'\n"},
	{"printf -v out '%#q\\n' \"a'b\"; printf '%s' \"$out\"", "'a'\\''b'\n"},
	{"printf %3i 1", "  1"},
	{"printf %+i%+d 1 -3", "+1-3"},
	{"printf 'x%-+10.0fx\\n' 123", "x+123      x\n"},
	{"printf 'x%-+10.0dx\\n' 123", "x+123      x\n"},
	{"printf 'x%+010.0xx\\n' 123", "x        7bx\n"},
	{"printf '%.2ls\\n' 'ಇಳಿಕೆಗಳು'", "ಇಳ\n"},
	{"printf '%4.2lc---\\n' 'ಇ'", "   ಇ---\n"},
	{"printf '%S %C\\n' 'ಇಳ' 'ಇ'", "ಇಳ ಇ\n"},
	{"printf %-5x 10", "a    "},
	{"printf '[%*s]\\n' 9223372036854775825 X 2>/dev/null", "[X]\nexit status 1"},
	{"printf '[%.*s]\\n' 9223372036854775825 X 2>/dev/null", "[X]\nexit status 1"},
	{"printf '%.9223372036854775825s\\n' XY", "XY\n"},
	{"printf '%.9223372036854775825Q\\n' XY 2>/dev/null", "XY\nexit status 1"},
	{"printf %02x 1", "01"},
	{"printf 'a% 5s' a", "a    a"},
	{"printf 'nofmt' 1 2 3", "nofmt"},
	{"printf '%d_' 1 2 3", "1_2_3_"},
	{"printf '%02d %02d\n' 1 2 3", "01 02\n03 00\n"},
	{`printf '0%s1' 'a\bc'`, `0a\bc1`},
	{`printf '0%b1' 'a\bc'`, "0a\bc1"},
	{"printf 'a%bc'", "ac"},
	{"printf 'before\\x00after'", "before\x00after"},

	// printf escape sequences at end of format string (must not panic)
	{"printf '\\0'", "\x00"},
	{"printf '\\01'", "\x01"},
	{"printf '\\x'", "bash: line 1: printf: missing hex digit for \\x\n\\x"},
	{"printf 'a\\0'", "a\x00"},
	{"printf '\\\\'", "\\"},

	// words and quotes
	{"echo  foo ", "foo\n"},
	{"echo ' foo '", " foo \n"},
	{`echo " foo "`, " foo \n"},
	{`echo a'b'c"d"e`, "abcde\n"},
	{`a=" b c "; echo $a`, "b c\n"},
	{`a=" b c "; echo "$a"`, " b c \n"},
	{`a=" b c "; echo foo${a}bar`, "foo b c bar\n"},
	{`a="b    c"; echo foo${a}bar`, "foob cbar\n"},
	{`echo "$(echo ' b c ')"`, " b c \n"},
	{"echo ''", "\n"},
	{`$(echo)`, ""},
	{`printf '<%s>\n' $(printf hello)`, "<hello>\n"},
	{`printf '<%s>\n' $(printf 'a b')`, "<a>\n<b>\n"},
	{`set -- $(printf 'foo bar'); echo $#:$1,$2`, "2:foo,bar\n"},
	{`echo -n '\\'`, `\\`},
	{`echo -n "\\"`, `\`},
	{`set -- a b c; x="$@"; echo "$x"`, "a b c\n"},
	{`set -- b c; echo a"$@"d`, "ab cd\n"},
	{`count() { echo $#; }; set --; count "$@"`, "0\n"},
	{`count() { echo $#; }; set -- ""; count "$@"`, "1\n"},
	{`count() { echo $#; }; set -- ""; shift; count "$@"`, "0\n"},
	{`count() { echo $#; }; a=(); count "${a[@]}"`, "0\n"},
	{`count() { echo $#; }; count "${unset_var[@]}"`, "0\n"},
	{`count() { echo $#; }; a=(""); count "${a[@]}"`, "1\n"},
	{`echo $1 $3; set -- a b c; echo $1 $3`, "\na c\n"},
	{`[[ $0 == "bash" || $0 == "gosh" || $0 == "bashy" ]]`, ""},

	// dollar quotes
	{`echo $'foo\nbar'`, "foo\nbar\n"},
	{`echo $'\r\t\\'`, "\r\t\\\n"},
	{`echo $"foo\nbar"`, "foo\\nbar\n"},
	{`echo $'%s'`, "%s\n"},
	{`a=$'\r\t\\'; echo "$a"`, "\r\t\\\n"},
	{`a=$"foo\nbar"; echo "$a"`, "foo\\nbar\n"},
	{`echo $'\a\b\e\E\f\v'`, "\a\b\x1b\x1b\f\v\n"},
	{`echo $'\\\'\"\?'`, "\\'\"?\n"},
	{`echo $'\1\45\12345\777\9'`, "\x01%S45\xff\\9\n"},
	{`echo $'\x\xf\x09\xAB'`, "\\x\x0f\x09\xab\n"},
	{`echo $'\u\uf\u09\uABCD\u00051234'`, "\\u\u000f\u0009\uabcd\u00051234\n"},
	{`echo $'\U\Uf\U09\UABCD\U00051234'`, "\\U\u000f\u0009\uabcd\U00051234\n"},
	{
		"echo 'before\x00after'",
		"beforeafter\n",
	},
	{
		"echo \"before\x00after\"",
		"beforeafter\n",
	},
	{
		"echo $'before\x00after'",
		"beforeafter\n",
	},
	{
		"echo $'before\\x00after'",
		"before\n",
	},
	{
		"echo $'before\\xafter'",
		"before\xafter\n",
	},
	{
		"a='before\x00after'; eval \"echo -n ${a} ${a@Q}\";",
		"beforeafter beforeafter",
	},
	{
		"a=$'before\\x00after'; eval \"echo -n ${a} ${a@Q}\";",
		"before before",
	},
	{
		"i\x00f true; then echo before\x00; \x00fi",
		"before\n",
	},
	{
		"echo $(GOSH_CMD=foo_null_bar $GOSH_PROG)",
		"foobar\n #IGNORE",
	},
	// See the TODO where foo_NULL_BAR is set.
	// {
	// 	"echo $foo_NULL_BAR \"${foo_NULL_BAR}\"",
	// 	"foo\n",
	// },

	// escaped chars
	{"echo a\\b", "ab\n"},
	{"echo a\\ b", "a b\n"},
	{"echo \\$a", "$a\n"},
	{"echo \"a\\b\"", "a\\b\n"},
	{"echo 'a\\b'", "a\\b\n"},
	{"echo \"a\\\nb\"", "ab\n"},
	{"echo 'a\\\nb'", "a\\\nb\n"},
	{`echo "\""`, "\"\n"},
	{`echo \\`, "\\\n"},
	{`echo \\\\`, "\\\\\n"},
	{`echo \`, "\\\n"},

	// escape characters in double quote literal
	{`echo "\\"`, "\\\n"},     // special character is preserved
	{`echo "\b"`, "\\b\n"},    // non-special character has both characters preserved
	{`echo "\\\\"`, "\\\\\n"}, // sequential backslashes (escape characters repeated sequentially)

	// vars
	{"foo=bar; echo $foo", "bar\n"},
	{"foo=bar foo=etc; echo $foo", "etc\n"},
	{"foo=bar; foo=etc; echo $foo", "etc\n"},
	{"foo=bar; foo=; echo $foo", "\n"},
	{"unset foo; echo $foo", "\n"},
	{"foo=bar; unset foo; echo $foo", "\n"},
	{"echo $INTERP_GLOBAL", "value\n"},
	{"INTERP_GLOBAL=; echo $INTERP_GLOBAL", "\n"},
	{"unset INTERP_GLOBAL; echo $INTERP_GLOBAL", "\n"},
	{"echo $MIXEDCASE_INTERP_GLOBAL", "value\n"},
	{"foo=bar; foo=x true; echo $foo", "bar\n"},
	{"foo=bar; foo=x true; echo $foo", "bar\n"},
	{"foo=bar; $ENV_PROG | grep '^foo='", "exit status 1"},
	{"foo=bar $ENV_PROG | grep '^foo='", "foo=bar\n"},
	{"foo=a foo=b $ENV_PROG | grep '^foo='", "foo=b\n"},
	{"$ENV_PROG | grep -i '^interp_global='", "INTERP_GLOBAL=value\n"},
	{"INTERP_GLOBAL=new; $ENV_PROG | grep -i '^interp_global='", "INTERP_GLOBAL=new\n"},
	{"INTERP_GLOBAL=; $ENV_PROG | grep -i '^interp_global='", "INTERP_GLOBAL=\n"},
	{"unset INTERP_GLOBAL; $ENV_PROG | grep -i '^interp_global='", "exit status 1"},
	{"a=b; a+=c x+=y; echo $a $x", "bc y\n"},
	{`a=" x  y"; b=$a c="$a"; echo $b; echo $c`, "x y\nx y\n"},
	{`a=" x  y"; b=$a c="$a"; echo "$b"; echo "$c"`, " x  y\n x  y\n"},
	{`arr=("foo" "bar" "lala" "foobar"); echo ${arr[@]:2}; echo ${arr[*]:2}`, "lala foobar\nlala foobar\n"},
	{`arr=("foo" "bar" "lala" "foobar"); echo ${arr[@]:2:4}; echo ${arr[*]:1:4}`, "lala foobar\nbar lala foobar\n"},
	{`a=(x y z); echo ${a[@]:0:-2}`, "-2: substring expression < 0\nexit status 1 #JUSTERR"},
	{`arr=("foo" "bar"); echo ${arr[@]}; echo ${arr[*]}`, "foo bar\nfoo bar\n"},
	{`arr=("foo"); echo ${arr[@]:99}`, "\n"},
	{`echo ${arr[@]:1:99}; echo ${arr[*]:1:99}`, "\n\n"},
	{`arr=(0 1 2 3 4 5 6 7 8 9 0 a b c d e f g h); echo ${arr[@]:3:4}`, "3 4 5 6\n"},
	{`a=([1]=one [3]=three [7]=seven); printf '<%s>\n' "${a[@]:2:2}" "${a[@]: -5}" "${a[*]:2:2}"`, "<three>\n<seven>\n<three>\n<seven>\n<three seven>\n"},
	{`n=0; (( (a[n]=++n)<7&&a[0] )); printf '<%s>\n' "${a[@]:1}"`, "<1>\n"},
	// negative array subscript in arithmetic counts from the end (like ${a[-1]}).
	{"a=(10 20 30); echo $((a[-1] + a[-2]))", "50\n"},
	{`v=hello; echo ${v:1:-2}`, "el\n"},
	{`v=hello; echo ${v: -3:2}`, "ll\n"},
	{`v=hello; echo ${v:2:}; echo ${v:2}`, "\nllo\n"},
	{`v=ಇಳಿಕೆಗಳು; printf '<%s> <%s>\n' "${v:0:2}" "${v:0:1}"`, "<ಇಳ> <ಇ>\n"},
	{`shopt -s nocasematch; v=abcd; printf '%s\n' "${v//A/z}" "${v//BC/x}" "${v//[BC]/x}"`, "zbcd\naxd\naxxd\n"},
	// In the C locale the byte matcher must honour POSIX character
	// classes inside brackets, so `${z//[[:alnum:]_]}` strips everything
	// but the separators.
	{`LC_ALL=C; z='a1;b2;c3'; echo "${z//[[:alnum:]_]}"`, ";;\n"},

	// an empty array has no element[0], so the non-colon ${a-w}/${a+w}
	// (and [@]/[*]) forms treat it as unset, like bash 5.3
	{`a=(); echo "${a-NO}"`, "NO\n"},
	{`a=(); echo "${a+yes}"`, "\n"},
	{`a=(); echo "${a[@]-NO}"`, "NO\n"},
	{`a=(); echo ${a[@]+yes}`, "\n"},
	{`a=(x y); echo "${a-NO}":"${a+yes}"`, "x:yes\n"},
	{`a=([5]=v); echo "${a-NO}":"${a[@]+yes}"`, "NO:yes\n"},
	{`declare -A m=([k]=v); echo "${m[@]+yes}":"${m-NO}"`, "yes:NO\n"},
	// an unterminated bracket class (`[]`) is a literal `[` in bash
	{`v='[]foo[]'; echo ${v#[]}`, "foo[]\n"},

	// quoted array slicing
	{`a=(1 2 3 4 5); echo "${a[@]:2:2}"`, "3 4\n"},
	{`a=(1 2 3 4 5); echo "${a[*]:2:2}"`, "3 4\n"},
	{`a=(1 2 3 4 5); b=("${a[@]:2:2}"); echo ${#b[@]}`, "2\n"},
	{`a=(1 2 3 4 5); echo "${a[@]:3}"`, "4 5\n"},
	{`a=(1 2 3 4 5); echo "${a[@]: -2}"`, "4 5\n"},
	{`a=(1 2 3 4 5); echo "${a[@]: -99}"`, "\n"},

	// positional parameter slicing (1-based offset, $0 at offset 0)
	{`f() { echo "${@:2:2}"; }; f a b c d e`, "b c\n"},
	{`f() { echo ${@:2:2}; }; f a b c d e`, "b c\n"},
	{`set -- a b c; echo ${@:2:2}`, "b c\n"},
	{`set -- a; echo ${@:1:$(($# - 2))}`, "$(($# - 2)): substring expression < 0\nexit status 1 #JUSTERR"},
	{`set -- a b c d e; echo ${@: -3:-2}`, "-2: substring expression < 0\nexit status 1 #JUSTERR"},
	{`f() { echo "${@:1}"; }; f a b c`, "a b c\n"},
	{`f() { echo "${*:2:2}"; }; f a b c d e`, "b c\n"},
	{`f() { echo "${@: -2}"; }; f a b c d e`, "d e\n"},
	{`f() { echo "${@: -3:2}"; }; f a b c d e`, "c d\n"},
	{`f() { echo "${@:1:0}"; }; f a b c`, "\n"},
	{`f() { echo "${@:99}"; }; f a b c`, "\n"},
	{`set -- a b c; v=("${@:0:2}"); echo "${#v[@]}"`, "2\n"},
	{`f() { for x in "${@:2:2}"; do echo "$x"; done; }; f a b c d e`, "b\nc\n"},
	{`set --; v=("${@:0}"); echo "${#v[@]}"`, "1\n"},
	{`f() { echo "${@: -10}"; }; f a b c`, "\n"},

	{`echo ${foo[@]}; echo ${foo[*]}`, "\n\n"},
	// TODO: reenable once we figure out the broken pipe error
	//{`$ENV_PROG | while read line; do if test -z "$line"; then echo empty; fi; break; done`, ""}, // never begin with an empty element

	// inline variables have special scoping
	{
		"f() { echo $inline; inline=bar true; echo $inline; }; inline=foo f",
		"foo\nfoo\n",
	},
	{"v=x; read v <<< 'y'; echo $v", "y\n"},
	{"v=x; v=inline read v <<< 'y'; echo $v", "x\n"},
	{"v=x; v=inline unset v; echo $v", "x\n"},
	{"v=x; echo 'v=y' >f; v=inline source ./f; echo $v", "x\n"},
	{"declare -n v=v2; v=inline true; echo $v $v2", "\n"},
	{"f() { echo $v; }; v=x; v=y f; f", "y\nx\n"},
	{"f() { echo $v; }; v=x; v+=y f; f", "xy\nx\n"},
	{"f() { echo $v; }; declare -n v=v2; v2=x; v=y f; f", "y\nx\n"},
	{"f() { echo ${v[@]}; }; v=(e1 e2); v=y f; f", "y\ne1 e2\n"},
	// A value leaked by a nested POSIX special builtin belongs to the
	// outer function call's temporary binding. An unrelated inline function
	// call in between must restore its own value rather than consuming the
	// stale leak marker.
	{
		"set -o posix; inner() { x=inner export x; }; g() { :; }; " +
			"outer() { inner; x=next g; printf 'inside=%s\\n' \"$x\"; }; " +
			"x=outer outer; printf 'after=%s\\n' \"$x\"",
		"inside=inner\nafter=inner\n",
	},
	// ENOTDIR text comes from the OS locale; this spelling is GNU Bash 5.3
	// with the confirm harness's C.UTF-8 locale on macOS and Linux.
	{"f() { echo bad; } 3>/dev/null/abc && f; echo status:$?", "bash: line 1: /dev/null/abc: Not a directory\nstatus:1\n"},
	{"f() { echo hi; } >out; echo def:$?; f; echo after; printf 'file:'; cat out", "def:0\nafter\nfile:hi\n"},
	{`f() { local v=x; unset v; declare -p v; }; v=g; f; v=t f`, "declare -- v\ndeclare -x v\n"},
	{`a=bcde; f1() { a=3 readonly a; echo f1:$a; }; a=7 f1; echo "global:$a"; set -o posix; a=7 f1; echo "global:$a"`, "f1:3\nglobal:bcde\nf1:3\nglobal:3\n"},

	// special vars
	{"echo $?; false; echo $?", "0\n1\n"},
	{"for i in 1 2; do\necho $LINENO\necho $LINENO\ndone", "2\n3\n2\n3\n"},
	{"[[ -n $$ && $$ -gt 0 ]]", ""},
	{"[[ $$ -eq $PPID ]]", "exit status 1"},
	// [[ ]] && / || must short-circuit so the rhs (and its expansions) is
	// never evaluated when the lhs settles the result. Mirrors bash's
	// cond.tests `[[ -n $TDIR || $HOME -ef ${H*} ]]` — the unevaluatable
	// ${H*} on the rhs must never run.
	{`TDIR=set; [[ -n $TDIR || $(echo SIDE >&2; echo x) ]] && echo ok 2>&1`, "ok\n"},
	{`unset TDIR; [[ -n $TDIR && $(echo SIDE >&2; echo x) ]] 2>&1 || echo no`, "no\n"},
	{"[[ $RANDOM -eq $RANDOM ]]", "exit status 1"},   // 1 in 32k chance of a collision, 0.003%
	{"[[ $SRANDOM -eq $SRANDOM ]]", "exit status 1"}, // 1 in 2**32 chance of a collision,
	{"RANDOM=42; echo $RANDOM $RANDOM", "17772 26794\n"},
	{"RANDOM=42; for i in 1 2 3 4 5 6; do echo -n \"$RANDOM \"; done; echo; RANDOM=42; for i in 1 2 3 4 5 6; do echo -n \"$((RANDOM)) \"; done; echo", "17772 26794 1435 24388 11074 32198 \n17772 26794 1435 24388 11074 32198 \n"},
	{"RANDOM=42; for i in {1..40}; do ((d1[$RANDOM%6+1 + $RANDOM%6+1]++)); done; RANDOM=42; for i in {1..40}; do ((d2[RANDOM%6+1 + RANDOM%6+1]++)); done; [[ ${d1[*]} == ${d2[*]} ]] && echo ok", "ok\n"},
	{"RANDOM=42; echo $RANDOM ${ echo $RANDOM; }", "17772 26794\n"},

	// 64-bit-value arithmetic comparisons. The lexical `<` works regardless of
	// int width; the arithmetic forms need 64-bit ints and so live in
	// runTests64bit (the evaluator uses native int until the int64 arith API
	// migration — see expand/arith.go TODO(v4)).
	{"[[ 1000000000123 < 100 ]]", "exit status 1"},
	{"(( array[0]++ )); echo ${array[0]}; (( array[0] ++ )); echo ${array[0]}", "1\n2\n"},
	{"(( ++array[1] )); echo ${array[1]}", "1\n"},
	{"v=4; DIND=20; (( dice[DIND/v]+=2 )); echo ${dice[5]}", "2\n"},

	// var manipulation
	{"echo ${#a} ${#a[@]}", "0 0\n"},
	{"a=bar; echo ${#a} ${#a[@]}", "3 1\n"},
	{"a=世界; echo ${#a}", "2\n"},
	{"a=(a bcd); echo ${#a} ${#a[@]} ${#a[*]} ${#a[1]}", "1 2 2 3\n"},
	{
		"a=($(echo a bcd)); echo ${#a} ${#a[@]} ${#a[*]} ${#a[1]}",
		"1 2 2 3\n",
	},
	{
		"a=([0]=$(echo a b) $(echo c d)); echo ${#a} ${#a[@]} ${#a[*]} ${#a[0]}",
		"3 3 3 3\n",
	},
	{"set -- a bc; echo ${#@} ${#*} $#", "2 2 2\n"},
	{
		"echo ${!a}; echo more",
		"a: invalid indirect expansion\nexit status 1 #JUSTERR",
	},
	{
		"a=b; echo ${!a}; b=c; echo ${!a}",
		"\nc\n",
	},
	{
		"set -- a b c d e f g h; z=abcdefghijklmnop; echo ${!9:-$z}",
		"abcdefghijklmnop\n",
	},
	{
		"a=foo_very_long; echo ${a:1}; echo ${a: -1}; echo ${a: -10}; echo ${a:5}",
		"oo_very_long\ng\n_very_long\nery_long\n",
	},
	{
		"a=foo_very_long; echo ${a::2}; echo ${a::-1}; echo ${a: -10}; echo ${a::5}",
		"fo\nfoo_very_lon\n_very_long\nfoo_v\n",
	},
	{
		"a=abc; echo ${a:1:1}",
		"b\n",
	},
	{
		"a=foo; echo ${a/no/x} ${a/o/i} ${a//o/i} ${a/fo/}",
		"foo fio fii o\n",
	},
	{
		"a=foo; echo ${a/*/xx} ${a//?/na} ${a/o*}",
		"xx nanana f\n",
	},
	{
		"a=12345; echo ${a//[42]} ${a//[^42]} ${a//[!42]}",
		"135 24 24\n",
	},
	{"a=0123456789; echo ${a//[1-35-8]}", "049\n"},
	{"a=]abc]; echo ${a//[]b]}", "ac\n"},
	{"a=-abc-; echo ${a//[-b]}", "ac\n"},
	{`a='x\y'; echo ${a//\\}`, "xy\n"},
	{"a=']'; echo ${a//[}", "]\n"},
	{"a=']'; echo ${a//[]}", "]\n"},
	{"a=']'; echo ${a//[]]}", "\n"},
	{"a='['; echo ${a//[[]}", "\n"},
	{"a=']'; echo ${a//[xy}", "]\n"},
	{"a='abc123'; echo ${a//[[:digit:]]}", "abc\n"},
	{"a='[[:wrong:]]'; echo ${a//[[:wrong:]]}", "[[:wrong:]]\n"},
	{"a='[[:wrong:]]'; echo ${a//[[:}", "[[:wrong:]]\n"},
	{"a='abcx1y'; echo ${a//x[[:digit:]]y}", "abc\n"},
	{`a=xyz; echo "${a/y/a  b}"`, "xa  bz\n"},
	{"a='foo/bar'; echo ${a//o*a/}", "fr\n"},
	{"a=foobar; echo ${a//a/} ${a///b} ${a///}", "foobr foobar foobar\n"},
	{
		"echo ${a:-b}; echo $a; a=; echo ${a:-b}; a=c; echo ${a:-b}",
		"b\n\nb\nc\n",
	},
	{
		"echo ${#:-never} ${?:-never} ${LINENO:-never}",
		"0 0 1\n",
	},
	{
		"echo ${1-one} ${2-two} ${3-three}",
		"one two three\n",
	},
	{
		"set -u; echo ${1}",
		"1: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"echo ${a-b}; echo $a; a=; echo ${a-b}; a=c; echo ${a-b}",
		"b\n\n\nc\n",
	},
	{
		"echo ${a:=b}; echo $a; a=; echo ${a:=b}; a=c; echo ${a:=b}",
		"b\nb\nb\nc\n",
	},
	{
		"echo ${a=b}; echo $a; a=; echo ${a=b}; a=c; echo ${a=b}",
		"b\nb\n\nc\n",
	},
	{
		"echo ${a:+b}; echo $a; a=; echo ${a:+b}; a=c; echo ${a:+b}",
		"\n\n\nb\n",
	},
	{
		"echo ${a+b}; echo $a; a=; echo ${a+b}; a=c; echo ${a+b}",
		"\n\nb\nb\n",
	},
	{
		"a=b; echo ${a:?err1}; a=; echo ${a:?err2}; unset a; echo ${a:?err3}",
		"b\na: err2\nexit status 1 #JUSTERR",
	},
	{
		"a=b; echo ${a?err1}; a=; echo ${a?err2}; unset a; echo ${a?err3}",
		"b\n\na: err3\nexit status 1 #JUSTERR",
	},
	{
		"echo ${a:?%s}",
		"a: %s\nexit status 1 #JUSTERR",
	},
	// Bash substitutes default messages when no word follows ? / :?.
	{
		"echo ${a?}",
		"a: parameter not set\nexit status 1 #JUSTERR",
	},
	{
		"echo ${a:?}",
		"a: parameter null or not set\nexit status 1 #JUSTERR",
	},
	{
		"a=; echo ${a:?}",
		"a: parameter null or not set\nexit status 1 #JUSTERR",
	},
	{
		"x=aaabccc; echo ${x#*a}; echo ${x##*a}",
		"aabccc\nbccc\n",
	},
	{
		// a `[` with no closing `]` is a literal `[` in strip patterns
		// (bash); valid bracket expressions still match.
		"v='[foo]'; echo ${v#[}; echo ${v#[f}; echo ${v#[a-z]}",
		"foo]\noo]\n[foo]\n",
	},
	{
		"shopt -s extglob; x=000987; echo ${x##*(0)}",
		"987\n",
	},
	{
		"shopt -s extglob; x=abcdef; echo ${x#+(a|abc)}; echo ${x##+(a|abc)}",
		"bcdef\ndef\n",
	},
	{
		"shopt -s extglob; TEST='a , b'; echo ${TEST//*([[:space:]]),*([[:space:]])/,}",
		"a,b\n",
	},
	{
		"x=(__a _b c_); echo ${x[@]#_}",
		"_a b c_\n",
	},
	{
		"shopt -s extglob; x=(000987 00123); printf '<%s>\\n' \"${x[@]##*(0)}\"",
		"<987>\n<123>\n",
	},
	{
		"x=(a__ b_ _c); echo ${x[@]%%_}",
		"a_ b _c\n",
	},
	{
		"x=aaabccc; echo ${x%c*}; echo ${x%%c*}",
		"aaabcc\naaab\n",
	},
	{
		"x=aaabccc; echo ${x%%[bc}",
		"aaabccc\n",
	},
	{
		"a='àÉñ bAr'; echo ${a^}; echo ${a^^}",
		"ÀÉñ bAr\nÀÉÑ BAR\n",
	},
	{
		"a='àÉñ bAr'; echo ${a,}; echo ${a,,}",
		"àÉñ bAr\nàéñ bar\n",
	},
	{
		"a='àÉñ bAr'; echo ${a^?}; echo ${a^^[br]}",
		"ÀÉñ bAr\nàÉñ BAR\n",
	},
	{
		"a='àÉñ bAr'; echo ${a,?}; echo ${a,,[br]}",
		"àÉñ bAr\nàÉñ bAr\n",
	},
	{
		"empty=; x=x; echo ${x@K} ${empty@K} ${undef@K} ${x@K}; echo ${x@k} ${empty@k} ${undef@k} ${x@k}",
		"'x' '' 'x'\n'x' '' 'x'\n",
	},
	{
		"a=(àÉñ bAr); echo ${a[@]^}; echo ${a[*],,}",
		"ÀÉñ BAr\nàéñ bar\n",
	},
	{
		`declare -A A=([x]=y); echo x=${!A[@]@a}; echo invalid=${!A@a}`,
		"x=\ninvalid=\n",
	},
	{
		`set -- "" ""; IFS=; echo argv=${*:-minus}; echo argv=${*:+plus}; echo "argv=${*:-minus}"; echo "argv=${*:+plus}"`,
		"argv=\nargv=plus\nargv=minus\nargv=\n",
	},
	{
		`a=(); echo "${a[0]-no-colon}"; ref=a; echo "${!ref-no-colon}"; ref='a[@]'; echo "${!ref-no-colon}"`,
		"no-colon\nno-colon\nno-colon\n",
	},
	{
		`LC_ALL=C; s='_μ_ and _μ_'; echo ${s//_?_/foo}; LC_ALL=en_US.UTF-8; echo ${s//_?_/foo}`,
		"_μ_ and _μ_\nfoo and foo\n",
	},
	{
		`LC_ALL=en_US.UTF-8; s=$'\U00010000'; echo ${#s}; LC_ALL=C; echo ${#s}`,
		"1\n4\n",
	},
	{
		`program='^++--hello.,world<>[]'; program=${program//[^'><+-.,[]']}; echo "$program"; pat='[^]]'; s='ab^cd^'; echo ${s//$pat/z}`,
		"++--.,<>[]\nab^cd^\n",
	},
	{
		"INTERP_X_1=a INTERP_X_2=b; echo ${!INTERP_X_*}",
		"INTERP_X_1 INTERP_X_2\n",
	},
	{
		"INTERP_X_2=b INTERP_X_1=a; echo ${!INTERP_*}",
		"INTERP_GLOBAL INTERP_X_1 INTERP_X_2\n",
	},
	{
		`INTERP_X_2=b INTERP_X_1=a; set -- ${!INTERP_*}; echo $#`,
		"3\n",
	},
	{
		`INTERP_X_2=b INTERP_X_1=a; set -- "${!INTERP_*}"; echo $#`,
		"1\n",
	},
	{
		`INTERP_X_2=b INTERP_X_1=a; set -- ${!INTERP_@}; echo $#`,
		"3\n",
	},
	{
		`INTERP_X_2=b INTERP_X_1=a; set -- "${!INTERP_@}"; echo $#`,
		"3\n",
	},
	{
		`a='b  c'; eval "echo -n ${a} ${a@Q}"`,
		`b c b  c`,
	},
	{
		`a='"\n'; printf "%s %s" "${a}" "${a@E}"`,
		"\"\\n \"\n",
	},

	// ${var@a} and ${var@A}
	{
		`a=foo; echo "<${a@a}>"`,
		"<>\n",
	},
	{
		`declare -a arr=(1 2 3); echo "${arr@a}"`,
		"a\n",
	},
	{
		`declare -A map=([k]=v); echo "${map@a}"`,
		"A\n",
	},
	{
		`export e=1; echo "${e@a}"`,
		"x\n",
	},
	{
		`readonly ro=1; echo "${ro@a}"`,
		"r\n",
	},
	{
		`declare -a arr=(1); export arr; echo "${arr@a}"`,
		"ax\n",
	},
	{
		`a=hello; echo "${a@A}"`,
		"a='hello'\n",
	},
	{
		`export e=1; echo "${e@A}"`,
		"declare -x e='1'\n",
	},
	{
		`var=VAR2 VAR2=zzz; printf '<%s>\n' "${!var@Q}" "${!var@A}" "${!var@a}"`,
		"<'zzz'>\n<VAR2='zzz'>\n<>\n",
	},
	// ${arr@A} reproduces the full reusable declaration for arrays.
	{
		`A=(ab 'cd ef' '' gh); echo "${A[@]@A}"`,
		`declare -a A=([0]="ab" [1]="cd ef" [2]="" [3]="gh")` + "\n",
	},
	{
		`A=(ab 'cd ef' '' gh); echo "${A@A}"`,
		"declare -a A='ab'\n",
	},
	{
		// Empty array with explicit [@] keeps the `=()` literal.
		`B=(); echo "${B[@]@A}"`,
		"declare -a B=()\n",
	},
	{
		// Empty array without a subscript reproduces as a bare declaration.
		`declare -ia foo=(); echo "${foo@A}"`,
		"declare -ai foo\n",
	},
	{
		// Associative arrays emit -A and iterate in bash hash-bucket order.
		`declare -A A=([one]=1 [two]='b c' [three]='' [four]=de); echo "${A[@]@A}"`,
		`declare -A A=([four]="de" [two]="b c" [three]="" [one]="1" )` + "\n",
	},
	{
		`declare -a arr=(1 2 3); echo "${arr[@]@a}"`,
		"a a a\n",
	},
	{
		// Attribute order: integer/readonly/export before case mods.
		`declare -lr s=ABC; echo "${s@a}"`,
		"rl\n",
	},
	{
		// Positional parameters reproduce via `set --`.
		`set -- ab 'cd ef' '' gh; echo "${@@A}"`,
		"set -- 'ab' 'cd ef' '' 'gh'\n",
	},
	{
		`a=Hello; echo "${a@U}"`,
		"HELLO\n",
	},
	{
		`a=hello; echo "${a@u}"`,
		"Hello\n",
	},
	{
		`a=HELLO; echo "${a@L}"`,
		"hello\n",
	},
	{
		`a=foo; echo "<${a@K}><${a@k}>"`,
		"<'foo'><'foo'>\n #IGNORE not implemented; must not panic",
	},
	{
		`x=abcdef; echo ${x@C}; echo ${x@}; echo ${x@bar}; echo ${x@'Q'}`,
		"bad substitution\nbad substitution\nbad substitution\nbad substitution\nexit status 1 #JUSTERR",
	},
	{
		"declare a; a+=(b); echo ${a[@]} ${#a[@]}",
		"b 1\n",
	},
	{
		`a=""; a+=(b); echo ${a[@]} ${#a[@]}`,
		"b 2\n",
	},
	{
		"f() { local a; a=bad; a=good; echo $a; }; f",
		"good\n",
	},
	{
		`declare x; [[ -v x ]] && echo set || echo unset`,
		"unset\n",
	},
	{
		`declare x=; [[ -v x ]] && echo set || echo unset`,
		"set\n",
	},
	{
		`declare -a x; [[ -v x ]] && echo set || echo unset`,
		"unset\n",
	},
	{
		`declare -A x; [[ -v x ]] && echo set || echo unset`,
		"unset\n",
	},
	{
		`declare -a a; a[1]=1; [[ -v a ]] && echo set || echo unset; [[ -v a[@] ]] && echo set || echo unset`,
		"unset\nset\n",
	},
	{
		`declare -A A; A[a]=1; [[ -v A ]] && echo set || echo unset; [[ -v A[@] ]] && echo at || echo no-at; A[@]=2; [[ -v A[@] ]] && echo at || echo no-at`,
		"unset\nno-at\nat\n",
	},
	{
		`declare -A a=([foo]=bleh [bar]=qux [baz]=z); set | grep '^a='`,
		"a=([foo]=\"bleh\" [bar]=\"qux\" [baz]=\"z\" )\n",
	},
	{
		`declare -A A; key='$(echo foo)'; A[$key]=1; test -v A["$key"]; echo $?`,
		"1\n",
	},
	// Regressions from bash 5.3 quotearray2-5.sub: special assoc array keys
	// (']', '*', '@', whitespace) through arith, read, printf -v, test -v,
	// and single-expansion unset -v.
	{
		`declare -A A; for k in ']' '*' '@'; do (( A[$k]=2 )); done; declare -p A`,
		"declare -A A=([\"*\"]=\"2\" [\"]\"]=\"2\" [\"@\"]=\"2\" )\n",
	},
	{
		`declare -A A; for k in ']' '*' '@'; do declare A[$k]=X; done; declare -p A`,
		"bash: line 1: declare: `A[]]=X': not a valid identifier\ndeclare -A A=([\"*\"]=\"X\" [\"@\"]=\"X\" )\n",
	},
	{
		`declare -A A; for k in ']' '*' '@'; do declare "A[$k]=X"; done; declare -p A`,
		"bash: line 1: declare: `A[]]=X': not a valid identifier\ndeclare -A A=([\"*\"]=\"X\" [\"@\"]=\"X\" )\n",
	},
	{
		`declare -A A; declare A["foo[bar]"]=X; echo ${!A[@]} ${A["foo[bar]"]}`,
		"foo[bar] X\n",
	},
	{
		`declare -A A["foo[bar]"]=X; echo ${!A[@]} ${A["foo[bar]"]}`,
		"foo[bar] X\n",
	},
	{
		`declare -A A; A["foo[bar]"]=x; A["foobar]"]=y; A[foo]=z; echo ${!A[@]}`,
		"foobar] foo foo[bar]\n",
	},
	{
		`declare -A foo; foo["bar\]bie"]=doll; echo "${!foo[@]}"; declare -p foo`,
		"bar\\]bie\ndeclare -A foo=([\"bar\\\\]bie\"]=\"doll\" )\n",
	},
	{
		`declare -A a; a[0]=0 a[1]=1; let "a[\" \"]=11"; declare -p a`,
		"declare -A a=([1]=\"1\" [0]=\"0\" [\" \"]=\"11\" )\n",
	},
	{
		`declare -A assoc; key='x],b[$(echo uname >&2)'; let assoc[$key]++; declare -p assoc`,
		"bash: line 1: let: assoc[x],b[$(echo: bad array subscript (error token is \"b[$(echo\")\ndeclare -A assoc\n",
	},
	{
		`declare -A A; for k in $'\t' ' ' '*' '@'; do read "A[$k]" <<< X; done; declare -p A`,
		"declare -A A=([$'\\t']=\"X\" [\"*\"]=\"X\" [\" \"]=\"X\" [\"@\"]=\"X\" )\n",
	},
	{
		`declare -A A; read 'A[]]' <<< X`,
		"read: `A[]]': not a valid identifier\nexit status 1 #JUSTERR",
	},
	{
		`declare -A A; for k in $'\t' ' ' '*' '@'; do printf -v "A[$k]" %s X; done; declare -p A`,
		"declare -A A=([$'\\t']=\"X\" [\"*\"]=\"X\" [\" \"]=\"X\" [\"@\"]=\"X\" )\n",
	},
	{
		`declare -A A; printf -v 'A[]]' %s X`,
		"printf: \"A[]]\": not a valid identifier\nexit status 1 #JUSTERR",
	},
	{
		`declare -A A; A[']']=x; test -v 'A[]]' && echo yes`,
		"exit status 1",
	},
	{
		`declare -A a; key='$(echo foo)'; a[$key]=1; unset -v a["$key"]; declare -p a`,
		"declare -A a=()\n",
	},
	{
		`declare -A a; a['$key']=2; unset -v a['$key']; declare -p a`,
		"declare -A a=()\n",
	},
	{
		`scalar=; [[ -v scalar[@] ]] && echo set || echo unset`,
		"set\n",
	},
	{
		`declare -r -x x; [[ -v x ]] && echo set || echo unset`,
		"unset\n",
	},
	{
		`declare -n x; [[ -v x ]] && echo set || echo unset`,
		"unset\n",
	},
	// Additional regressions from bash 5.3 quotearray2-5.sub (round-10)
	{
		`declare -A A; k='['; echo $(( A[$k]=7 ))`,
		"7\n",
	},
	{
		`declare -A A; k=']'; echo $(( A[$k]=7 ))`,
		"7\n",
	},
	{
		`declare -A A; A[@]=at; A[!]=bang; key=@; test -v A[$key]; echo $?`,
		"0\n",
	},
	{
		`declare -A A; A[@]=at; A[!]=bang; key=@; [[ -v A[$key] ]]; echo $?`,
		"0\n",
	},
	{
		`declare -A A; A[@]=at; A[!]=bang; unset -v A[@]; declare -p A`,
		"declare -A A=([\"!\"]=\"bang\" )\n",
	},
	{
		`declare -A A; A[*]=star; A[!]=bang; unset -v A[*]; declare -p A`,
		"declare -A A=([\"!\"]=\"bang\" )\n",
	},
	{
		`declare -A A; A[@]=at; A[!]=bang; key=@; unset A[$key]; declare -p A`,
		"declare -A A=([\"!\"]=\"bang\" )\n",
	},
	{
		`declare -A assoc; assoc[@]=at assoc[*]=star assoc[!]=bang; aref='assoc[@]'; declare -n nref=$aref; echo $nref`,
		"star bang at\n",
	},
	{
		`array=(1 2 3); iref='array[@]'; declare -n nref=$iref; echo $nref; unset array[@]; declare -p array; unset array[@]; declare -p array`,
		"1 2 3\ndeclare -a array=()\ndeclare -a array=()\n",
	},

	// declare -f and declare -p
	{
		`f() { echo hello; }; declare -f f`,
		"f () \n{ \n    echo hello\n}\n",
	},
	{
		`f() { echo hello; }; declare -f -p f`,
		"f () \n{ \n    echo hello\n}\n",
	},
	{
		`declare -f nonexistent 2>/dev/null; echo "exit: $?"`,
		"exit: 1\n",
	},
	{
		`declare -f -p nonexistent 2>/dev/null; echo "exit: $?"`,
		"exit: 1\n",
	},
	{
		`f() { :; }; declare -f -a f; declare -f -i f g`,
		"declare: -a: invalid option\ndeclare: -i: invalid option\nexit status 2 #JUSTERR",
	},
	{
		`f() { :; }; readonly -f f; declare -f +r f`,
		"declare: f: readonly function\nexit status 1 #JUSTERR",
	},
	{
		`f() { :; }; declare -fr f; declare -F -r`,
		"declare -fr f\n",
	},
	{
		`declare -f f='() { :; }'`,
		"declare: cannot use `-f' to make functions\nexit status 1 #JUSTERR",
	},
	{
		`x=1; export -f x`,
		"export: x: not a function\nexit status 1 #JUSTERR",
	},
	{
		`f() { echo hello; }; declare -f f >/dev/null && echo "f exists"`,
		"f exists\n",
	},
	{
		`a=hello; declare -p a`,
		"declare -- a=\"hello\"\n",
	},
	{
		`declare a; declare -p a`,
		"declare -- a\n",
	},
	{
		`declare -a arr=(1 2 3); declare -p arr`,
		"declare -a arr=([0]=\"1\" [1]=\"2\" [2]=\"3\")\n",
	},
	{
		`b='[0]=bar'; declare -a arr="$b"; declare -p arr`,
		"declare -a arr=([0]=\"[0]=bar\")\n",
	},
	{
		`>a.x >b.x; array2=(grep [ 123 ] \*); declare -p array2; printf '<%s>\n' "${array2[@]}"; rm a.x b.x`,
		"declare -a array2=([0]=\"grep\" [1]=\"[ 123 ]\" [2]=\"*\")\n<grep>\n<[ 123 ]>\n<*>\n",
	},
	{
		`declare -a arr; declare -p arr; arr=(); declare -p arr`,
		"declare -a arr\ndeclare -a arr=()\n",
	},
	{
		`declare -a | grep -E '^(declare -a BASH_ARGC|declare -a BASH_ARGV|declare -a FUNCNAME)'`,
		"declare -a BASH_ARGC=()\ndeclare -a BASH_ARGV=()\ndeclare -a FUNCNAME\n",
	},
	{
		`declare -A assoc; declare -p assoc; assoc=(); declare -p assoc`,
		"declare -A assoc\ndeclare -A assoc=()\n",
	},
	{
		`declare -A assoc=([foo]=bar); declare -p assoc`,
		"declare -A assoc=([foo]=\"bar\" )\n",
	},
	{
		`declare -A assoc; key='x],b[$(echo uname >&2)'; (( assoc[$key]++ )); assoc[!]=bang; assoc[%]=pct; declare -p assoc`,
		"declare -A assoc=([%]=\"pct\" [\"!\"]=\"bang\" [\"x],b[\\$(echo uname >&2)\"]=\"1\" )\n",
	},
	{
		`declare -A assoc; key=abc; (( 'assoc[$key]++' )); declare -p assoc`,
		"bash: line 1: ((: 'assoc[abc]++' : arithmetic syntax error: operand expected (error token is \"'assoc[abc]++' \")\ndeclare -A assoc\n",
	},
	{
		`declare -A assoc; key='x],b[$(echo uname >&2)'; (( 'assoc[$key]++' )); echo status:$?; declare -p assoc`,
		"bash: line 1: ((: 'assoc[x\\],b\\[\\$(echo uname >&2)]++' : arithmetic syntax error: operand expected (error token is \"'assoc[x\\],b\\[\\$(echo uname >&2)]++' \")\nstatus:1\ndeclare -A assoc\n",
	},
	{
		`declare -A assoc; key='x],b[$(echo uname >&2)'; (( assoc[$key]++ )); (( assoc["$key"]++ )); (( "assoc[$key]++" )); declare -p assoc`,
		"declare -A assoc=([\"x],b[\\$(echo uname >&2)\"]=\"3\" )\n",
	},
	{
		`declare -ai arr=(1+1); declare -p arr`,
		"declare -ai arr=([0]=\"2\")\n",
	},
	{
		`readonly -a arr=(); declare -p arr`,
		"declare -ar arr=()\n",
	},
	{
		`arr=(x); readonly arr; set -o posix; readonly -a`,
		"readonly -a BASH_VERSINFO=([0]=\"5\" [1]=\"3\" [2]=\"0\" [3]=\"1\" [4]=\"release\" [5]=\"bashy\")\nreadonly -a arr=([0]=\"x\")\n",
	},
	{
		`declare -r c[100]; declare -p c`,
		"declare -ar c\n",
	},
	{
		`readonly a[5]`,
		"readonly: `a[5]': not a valid identifier\nexit status 1 #JUSTERR",
	},
	{
		`readonly invalid-name`,
		"readonly: invalid name \"invalid-name\"\nexit status 1 #JUSTERR",
	},
	{
		`export invalid-name`,
		"export: invalid name \"invalid-name\"\nexit status 1 #JUSTERR",
	},
	{
		`export a[5]`,
		"export: `a[5]': not a valid identifier\nexit status 1 #JUSTERR",
	},
	{
		`readonly non-identifier invalid+ident`,
		"readonly: invalid name \"non-identifier\"\nreadonly: invalid name \"invalid+ident\"\nexit status 1 #JUSTERR",
	},
	{
		`export non-identifier invalid+ident`,
		"export: invalid name \"non-identifier\"\nexport: invalid name \"invalid+ident\"\nexit status 1 #JUSTERR",
	},
	{
		`command readonly invalid-name; echo status:$?`,
		"bash: line 1: readonly: `invalid-name': not a valid identifier\nstatus:1\n",
	},
	{
		`command export invalid-name; echo status:$?`,
		"bash: line 1: export: `invalid-name': not a valid identifier\nstatus:1\n",
	},
	{
		`command readonly non-identifier invalid+ident; echo status:$?`,
		"bash: line 1: readonly: `non-identifier': not a valid identifier\nbash: line 1: readonly: `invalid+ident': not a valid identifier\nstatus:1\n",
	},
	{
		`command export non-identifier invalid+ident; echo status:$?`,
		"bash: line 1: export: `non-identifier': not a valid identifier\nbash: line 1: export: `invalid+ident': not a valid identifier\nstatus:1\n",
	},
	{
		`export e=1; declare -p e`,
		"declare -x e=\"1\"\n",
	},
	{
		`set -o posix; export EE=1; export | grep '^export EE='`,
		"export EE=\"1\"\n",
	},
	{
		`readonly c=immutable; declare -p c`,
		"declare -r c=\"immutable\"\n",
	},
	{
		`set -o posix; readonly RR=2; readonly | grep '^readonly RR='`,
		"readonly RR=\"2\"\n",
	},
	{
		`declare -p nonexistent 2>/dev/null; echo "exit: $?"`,
		"exit: 1\n",
	},

	// if
	{
		"if true; then echo foo; fi",
		"foo\n",
	},
	{
		"if false; then echo foo; fi",
		"",
	},
	{
		"if GOSH_CMD=print_fail $GOSH_PROG; then echo foo; fi",
		"exec fail\n",
	},
	{
		"if true; then echo foo; else echo bar; fi",
		"foo\n",
	},
	{
		"if false; then echo foo; else echo bar; fi",
		"bar\n",
	},
	{
		"if true; then false; fi",
		"exit status 1",
	},
	{
		"if false; then :; else false; fi",
		"exit status 1",
	},
	{
		"if false; then :; elif true; then echo foo; fi",
		"foo\n",
	},
	{
		"if false; then :; elif false; then :; elif true; then echo foo; fi",
		"foo\n",
	},
	{
		"if false; then :; elif false; then :; else echo foo; fi",
		"foo\n",
	},

	// while
	{
		"while false; do echo foo; done",
		"",
	},
	{
		"while false; do :; done; echo $?",
		"0\n",
	},
	{
		"i=0; while [ $i -lt 1 ]; do i=$((i + 1)); false; done; echo $?",
		"1\n",
	},
	{
		"i=0; while [ $i -lt 2 ]; do i=$((i + 1)); [ $i -eq 1 ]; done; echo $?",
		"1\n",
	},
	{
		"while GOSH_CMD=print_fail $GOSH_PROG; do echo foo; done",
		"exec fail\n",
	},
	{
		"while true; do exit 1; done",
		"exit status 1",
	},
	{
		"while true; do break; done",
		"",
	},
	{
		"while true; do while true; do break 2; done; done",
		"",
	},

	// until
	{
		"until true; do echo foo; done",
		"",
	},
	{
		"until true; do :; done; echo $?",
		"0\n",
	},
	{
		"i=0; until [ $i -ge 1 ]; do i=$((i + 1)); false; done; echo $?",
		"1\n",
	},
	{
		"i=0; until [ $i -ge 2 ]; do i=$((i + 1)); [ $i -eq 1 ]; done; echo $?",
		"1\n",
	},
	{
		"until false; do exit 1; done",
		"exit status 1",
	},
	{
		"until false; do break; done",
		"",
	},

	// for
	{
		"for i in 1 2 3; do echo $i; done",
		"1\n2\n3\n",
	},
	{
		"for i in 1 2 3; do echo $i; exit; done",
		"1\n",
	},
	{
		"for i in 1 2 3; do echo $i; false; done",
		"1\n2\n3\nexit status 1",
	},
	{
		"for i in 1 2 3; do echo $i; break; done",
		"1\n",
	},
	{
		"for i in 1 2 3; do echo $i; continue; echo foo; done",
		"1\n2\n3\n",
	},
	{
		"for i in 1 2; do for j in a b; do echo $i $j; continue 2; done; done",
		"1 a\n2 a\n",
	},
	{
		"for i in 1 2 3; do continue -1; done",
		"continue: -1: loop count out of range\nexit status 1 #JUSTERR",
	},
	{
		"for i in 1 2 3; do continue 0; done",
		"continue: 0: loop count out of range\nexit status 1 #JUSTERR",
	},
	{
		"for i in 1 2 3; do continue -- -5; done",
		"continue: -5: loop count out of range\nexit status 1 #JUSTERR",
	},
	{
		"for ((i=0; i<3; i++)); do echo $i; done",
		"0\n1\n2\n",
	},
	// for, with missing Init, Cond, Post
	{
		"i=0; for ((; i<3; i++)); do echo $i; done",
		"0\n1\n2\n",
	},
	{
		"for ((i=0;; i++)); do if [ $i -ge 3 ]; then break; fi; echo $i; done",
		"0\n1\n2\n",
	},
	{
		"for ((i=0; i<3;)); do echo $i; i=$((i+1)); done",
		"0\n1\n2\n",
	},
	{
		"i=0; for ((;;)); do if [ $i -ge 3 ]; then break; fi; echo $i; i=$((i+1)); done",
		"0\n1\n2\n",
	},
	// TODO: uncomment once expandEnv.Set starts returning errors
	// {
	// 	"readonly i; for ((i=0; i<3; i++)); do echo $i; done",
	// 	"0\n1\n2\n",
	// },
	{
		"for ((i=5; i>0; i--)); do echo $i; break; done",
		"5\n",
	},
	{
		"for i in 1 2; do for j in a b; do echo $i $j; done; break; done",
		"1 a\n1 b\n",
	},
	{
		"for i in 1 2 3; do :; done; echo $i",
		"3\n",
	},
	{
		"for ((i=0; i<3; i++)); do :; done; echo $i",
		"3\n",
	},
	{
		"set -- a 'b c'; for i in; do echo $i; done",
		"",
	},
	{
		"set -- a 'b c'; for i; do echo $i; done",
		"a\nb c\n",
	},

	// block
	{
		"{ echo foo; }",
		"foo\n",
	},
	{
		"{ false; }",
		"exit status 1",
	},

	// subshell
	{
		"(echo foo)",
		"foo\n",
	},
	{
		"(false)",
		"exit status 1",
	},
	{
		"(exit 1)",
		"exit status 1",
	},
	{
		"(false); echo foo",
		"foo\n",
	},
	{
		"(exit 0); echo foo",
		"foo\n",
	},
	{
		"(exit 1); echo foo",
		"foo\n",
	},
	{
		"(foo=bar; echo $foo); echo $foo",
		"bar\n\n",
	},
	{
		"(echo() { printf 'bar\n'; }; echo); echo",
		"bar\n\n",
	},
	{
		"unset INTERP_GLOBAL & echo $INTERP_GLOBAL",
		"value\n",
	},
	{
		"(fn() { :; }) & pwd >/dev/null",
		"",
	},
	{
		"x[0]=x; (echo ${x[0]}; x[0]=y; echo ${x[0]}); echo ${x[0]}",
		"x\ny\nx\n",
	},
	{
		`x[3]=x; (x[3]=y); echo ${x[3]}`,
		"x\n",
	},
	{
		"shopt -s expand_aliases; alias f='echo x'\nf\n(f\nalias f='echo y'\neval f\n)\nf\n",
		"x\nx\ny\nx\n",
	},
	{
		"set -- a; echo $1; (echo $1; set -- b; echo $1); echo $1",
		"a\na\nb\na\n",
	},
	{"false; ( echo $? )", "1\n"},

	// cd/pwd
	{"[[ fo~ == 'fo~' ]]", ""},
	{`[[ 'ab\c' == *\\* ]]`, ""},
	{`[[ foo/bar == foo* ]]`, ""},
	{"[[ a == [ab ]]", "exit status 1"},
	{`HOME='/*'; echo ~; echo "$HOME"`, "/*\n/*\n"},
	{`test -d ~`, ""},
	{
		`for flag in b c d e f g h k L p r s S u w x; do test -$flag ""; echo -n "$flag$? "; done`,
		`b1 c1 d1 e1 f1 g1 h1 k1 L1 p1 r1 s1 S1 u1 w1 x1 `,
	},
	{`foo=~; test -d $foo`, ""},
	{`foo=~; test -d "$foo"`, ""},
	{`foo='~'; test -d $foo`, "exit status 1"},
	{`foo='~'; [ $foo == '~' ]`, ""},
	{
		`[[ ~ == "$HOME" ]] && [[ ~/foo == "$HOME/foo" ]]`,
		"",
	},
	{
		`HOME=$PWD/home; mkdir home; touch home/f; [[ -e ~/f ]]`,
		"",
	},
	{
		`HOME=$PWD/home; mkdir home; touch home/f; [[ ~/f -ef $HOME/f ]]`,
		"",
	},
	{
		"[[ ~noexist == '~noexist' ]]",
		"",
	},
	{
		`w="$HOME"; cd; [[ $PWD == "$w" ]]`,
		"",
	},
	{
		`cd ''`,
		"cd: null directory\nexit status 1 #JUSTERR",
	},
	{
		`HOME=/foo; echo $HOME`,
		"/foo\n",
	},
	{
		"cd noexist",
		"cd: noexist: No such file or directory\nexit status 1 #JUSTERR",
	},
	{
		"mkdir -p a/b && cd a && cd b && cd ../..",
		"",
	},
	{
		">a && cd a",
		"cd: a: Not a directory\nexit status 1 #JUSTERR",
	},
	{
		`payload=$'\065\247\100\063\231\053\306\123\070\237\242\352\263'; cd "$payload"`,
		"cd: $'5\\247@3\\231+\\306S8\\237\\242\\352\\263': No such file or directory\nexit status 1 #JUSTERR",
	},
	{
		`[ "!" != "!" ]; echo bang:$?; [ "(" != "(" ]; echo paren:$?`,
		"bang:1\nparen:1\n",
	},
	{
		`[[ $PWD == "$(pwd)" ]]`,
		"",
	},
	{
		"PWD=changed; [[ $PWD == changed ]]",
		"",
	},
	{
		"PWD=changed; mkdir a; cd a; [[ $PWD == changed ]]",
		"exit status 1",
	},
	{
		`mkdir %s; old="$PWD"; cd %s; [[ $old == "$PWD" ]]`,
		"exit status 1",
	},
	{
		`old="$PWD"; mkdir a; cd a; cd ..; [[ $old == "$PWD" ]]`,
		"",
	},
	{
		`[[ $PWD == "$OLDPWD" ]]`,
		"exit status 1",
	},
	{
		`old="$PWD"; mkdir a; cd a; [[ $old == "$OLDPWD" ]]`,
		"",
	},
	{
		`old="$PWD"; mkdir a; mkdir parent parent/d; CDPATH=parent; cd d >/dev/null; [[ "$PWD" = "$old/parent/d" ]]; echo path:$?; [[ "$OLDPWD" = "$old" ]]; echo old:$?`,
		"path:0\nold:0\n",
	},
	{
		`old="$PWD"; mkdir d; CDPATH=.; cd d; [[ "$PWD" = "$old/d" ]]; echo path:$?`,
		"ABS_PATH_D\npath:0\n",
	},
	{
		`mkdir a; ln -s a b; [[ $(cd a && pwd) == "$(cd b && pwd)" ]]; echo $?`,
		"1\n",
	},
	{
		`pwd -a`,
		"pwd: -a: invalid option\nexit status 2 #JUSTERR",
	},
	{
		`pwd -L -P -a`,
		"pwd: -a: invalid option\nexit status 2 #JUSTERR",
	},
	{
		`actual=$(pwd -P); PWD=/; [[ $(pwd -P) == "$actual" ]]`,
		"",
	},
	{
		`actual=$(pwd -P); PWD="$actual/."; [[ $(pwd -L) == "$actual" ]]`,
		"",
	},
	{
		`actual=$(pwd -P); PWD="$actual/child/.."; [[ $(pwd -L) == "$actual" ]]`,
		"",
	},
	{
		`root=$PWD; mkdir real; ln -s real link; cd link; PWD=/; [[ $(pwd -L) == "$root/link" ]]`,
		"",
	},
	{
		`pwd -- ignored >/dev/null; [[ $? == 0 ]]`,
		"",
	},
	{
		`pwd ignored >/dev/null 2>&1; [[ $? == 0 ]] && pwd --invalid >/dev/null 2>&1; [[ $? == 2 ]]`,
		"",
	},
	{
		`mkdir real; ln -s real link; cd link; [[ $(pwd -LP) == "$(pwd -P)" && $(pwd -PL) == "$PWD" ]]`,
		"",
	},
	{
		`mkdir a; ln -s a b; [[ "$(cd a && pwd -P)" == "$(cd b && pwd -P)" ]]`,
		"",
	},
	{
		`mkdir a; ln -s a b; [[ "$(cd a && pwd -P)" == "$(cd b && pwd -L)" ]]; echo $?`,
		"1\n",
	},
	{
		`orig="$PWD"; mkdir a; cd a; cd - >/dev/null; [[ "$PWD" == "$orig" ]]`,
		"",
	},
	{
		`orig="$PWD"; mkdir a; cd a; [[ $(cd -) == "$orig" ]]`,
		"",
	},
	{
		`OLDPWD=/tmp/bashy-does-not-exist; cd -; echo status:$?`,
		"cd: /tmp/bashy-does-not-exist: No such file or directory\nstatus:1\n #JUSTERR",
	},
	{
		`old=$PWD; unset OLDPWD; cd -; echo status:$?; [[ $PWD == "$old" ]]`,
		"cd: OLDPWD not set\nstatus:1\n #JUSTERR",
	},
	{
		`readonly PWD; cd /; echo status:$?`,
		"PWD: readonly variable\nstatus:1\n #JUSTERR",
	},
	{
		`readonly OLDPWD; cd /; echo status:$?`,
		"OLDPWD: readonly variable\nstatus:1\n #JUSTERR",
	},

	// dirs/pushd/popd
	{"set -- $(dirs); echo $# ${#DIRSTACK[@]}", "1 1\n"},
	{"dirs -c; set -- $(dirs); echo $# ${#DIRSTACK[@]}", "1 1\n"},
	{"pushd", "pushd: no other directory\nexit status 1 #JUSTERR"},
	{"pushd -n", ""},
	{"pushd foo bar", "pushd: too many arguments\nexit status 2 #JUSTERR"},
	{"pushd does-not-exist; set -- $(dirs); echo $#", "pushd: does-not-exist: No such file or directory\n1\n #IGNORE"},
	{"mkdir a; pushd a >/dev/null; set -- $(dirs); echo $#", "2\n"},
	{"mkdir a; set -- $(pushd a); echo $#", "2\n"},
	{
		`mkdir a; pushd a >/dev/null; set -- $(dirs); [[ $1 == "$HOME" ]]`,
		"exit status 1",
	},
	{
		`mkdir a; pushd a >/dev/null; [[ ${DIRSTACK[0]} == "$HOME" ]]`,
		"exit status 1",
	},
	{
		`old=$(dirs); mkdir a; pushd a >/dev/null; pushd >/dev/null; set -- $(dirs); [[ $1 == "$old" ]]`,
		"",
	},
	{
		`old=$(dirs); mkdir a; pushd a >/dev/null; pushd -n >/dev/null; set -- $(dirs); [[ $1 == "$old" ]]`,
		"exit status 1",
	},
	{
		"mkdir a; pushd a >/dev/null; pushd >/dev/null; rm -r a; pushd",
		"pushd: ABS_PATH_A: No such file or directory\nexit status 1 #JUSTERR",
	},
	{
		`old=$(dirs); mkdir a; pushd -n a >/dev/null; set -- $(dirs); [[ $1 == "$old" ]]`,
		"",
	},
	{
		`old=$(dirs); mkdir a; pushd -n a >/dev/null; pushd >/dev/null; set -- $(dirs); [[ $1 == "$old" ]]`,
		"exit status 1",
	},
	{"popd", "popd: directory stack empty\nexit status 1 #JUSTERR"},
	{"popd -n", "popd: directory stack empty\nexit status 1 #JUSTERR"},
	{"popd foo", "popd: foo: invalid argument\npopd: usage: popd [-n] [+N | -N]\nexit status 2 #JUSTERR"},
	{"old=$(dirs); mkdir a; pushd a >/dev/null; set -- $(popd); echo $#", "1\n"},
	{
		`old=$(dirs); mkdir a; pushd a >/dev/null; popd >/dev/null; [[ $(dirs) == "$old" ]]`,
		"",
	},
	{"old=$(dirs); mkdir a; pushd a >/dev/null; set -- $(popd -n); echo $#", "1\n"},
	{
		`old=$(dirs); mkdir a; pushd a >/dev/null; popd -n >/dev/null; [[ $(dirs) == "$old" ]]`,
		"exit status 1",
	},
	{
		`root=$PWD; mkdir a b; pushd "$root/a" >/dev/null; pushd "$root/b" >/dev/null; pushd +1 >/dev/null; [[ ${DIRSTACK[0]} == */a ]]`,
		"",
	},
	{
		`root=$PWD; mkdir a b c; pushd "$root/a" >/dev/null; pushd "$root/b" >/dev/null; pushd "$root/c" >/dev/null; popd +2 >/dev/null; [[ ${DIRSTACK[0]} == */c && ${DIRSTACK[1]} == */b && ${DIRSTACK[2]} == "$root" ]]`,
		"",
	},
	{
		`root=$PWD; mkdir a b; pushd "$root/a" >/dev/null; pushd "$root/b" >/dev/null; DIRSTACK[1]=$root; [[ $(dirs) == "$root/b $root $root" ]]`,
		"",
	},
	{
		"mkdir a; pushd a >/dev/null; pushd >/dev/null; rm -r a; popd",
		"popd: ABS_PATH_A: No such file or directory\nexit status 1 #JUSTERR",
	},

	// binary cmd
	{
		"true && echo foo || echo bar",
		"foo\n",
	},
	{
		"false && echo foo || echo bar",
		"bar\n",
	},

	// func
	{
		"foo() { echo bar; }; foo",
		"bar\n",
	},
	{
		"foo() { echo $1; }; foo",
		"\n",
	},
	{
		"foo() { echo $1; }; foo a b",
		"a\n",
	},
	{
		"foo() { echo $1; bar c d; echo $2; }; bar() { echo $2; }; foo a b",
		"a\nd\nb\n",
	},
	{
		`foo() { echo $#; }; foo; foo 1 2 3; foo "a b"; echo $#`,
		"0\n3\n1\n0\n",
	},
	{
		`foo() { echo "<${*-x}> <${@-x}>"; }; foo; foo ""; foo "" ""`,
		"<x> <x>\n<> <>\n< > < >\n",
	},
	{
		`foo() { echo "${!@}-${!*}"; }; foo`,
		"-\n",
	},
	{
		`foo() { for a in $*; do echo "$a"; done }; foo 'a  1' 'b  2'`,
		"a\n1\nb\n2\n",
	},
	{
		`foo() { for a in "$*"; do echo "$a"; done }; foo 'a  1' 'b  2'`,
		"a  1 b  2\n",
	},
	{
		`foo() { for a in "foo$*"; do echo "$a"; done }; foo 'a  1' 'b  2'`,
		"fooa  1 b  2\n",
	},
	{
		`foo() { for a in $@; do echo "$a"; done }; foo 'a  1' 'b  2'`,
		"a\n1\nb\n2\n",
	},
	{
		`foo() { for a in "$@"; do echo "$a"; done }; foo 'a  1' 'b  2'`,
		"a  1\nb  2\n",
	},

	// alias (note the input newlines)
	{
		"alias foo; alias foo=echo; alias foo; alias foo=; alias foo",
		"alias: foo: not found\nalias foo='echo'\nalias foo=''\n #IGNORE",
	},
	{
		"set -o posix; alias zz=yy; alias; alias -p",
		"zz='yy'\nalias zz='yy'\n",
	},
	{
		"alias -p -x; echo status=$?",
		"alias: -x: invalid option\nalias: usage: alias [-p] [name[=value] ... ]\nstatus=2\n #JUSTERR",
	},
	{
		"alias -pp; echo status=$?",
		"status=0\n",
	},
	{
		"alias interp_alias=one; alias -p interp_alias; echo status=$?",
		"alias interp_alias='one'\nstatus=0\n",
	},
	{
		"alias -- -x=one; alias -- -x; alias -p; eval \"$(alias -- -x)\"; alias -- -x",
		"alias -- -x='one'\nalias -- -x='one'\nalias -- -x='one'\n",
	},
	{
		"shopt -s expand_aliases; alias foo=echo\nfoo foo; foo bar",
		"foo\nbar\n",
	},
	{
		"shopt -s expand_aliases; alias true=echo\ntrue foo; unalias true\ntrue bar",
		"foo\n",
	},
	{
		"alias interp_unalias=echo; unalias -- interp_unalias; alias interp_unalias; echo status=$?",
		"alias: interp_unalias: not found\nstatus=1\n #JUSTERR",
	},
	{
		"shopt -s expand_aliases; alias echo='echo a'\necho b c",
		"a b c\n",
	},
	{
		"shopt -s expand_aliases; alias foo='echo '\nfoo foo; foo bar",
		"echo\nbar\n",
	},
	{
		"shopt -s expand_aliases; alias foo=\"echo 'Error:\"\neval \"foo bar'\"",
		"Error: bar\n",
	},
	{
		"shopt -s expand_aliases; alias comment=#\ncomment text after\necho ok",
		"ok\n",
	},
	// Alias-definition timing: bash expands an alias only on input read
	// after the line that defined it. A definition and use on the same
	// line (same parse unit) are not affected; a use on a later line is.
	{
		"shopt -s expand_aliases; alias al_t1='echo HI'; al_t1",
		"\"al_t1\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	{
		"shopt -s expand_aliases; alias al_t2='echo HI'\nal_t2",
		"HI\n",
	},
	{
		"shopt -s expand_aliases; alias al_t3='echo HI'\nx=$(al_t3); echo \"[$x]\"",
		"[HI]\n",
	},
	// eval re-reads its argument as new input, so it sees an alias defined
	// earlier on the same outer line.
	{
		"shopt -s expand_aliases; alias al_t4='echo HI'; eval al_t4",
		"HI\n",
	},
	// An alias defined via eval becomes visible on the next outer line but
	// not on the same one (its tokens were already read).
	{
		"shopt -s expand_aliases; eval 'alias al_t5=\"echo HI\"'; al_t5",
		"\"al_t5\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	{
		"shopt -s expand_aliases; eval 'alias al_t6=\"echo HI\"'\nal_t6",
		"HI\n",
	},

	// case
	{
		"case b in x) echo foo ;; a|b) echo bar ;; esac",
		"bar\n",
	},
	{
		"case b in x) echo foo ;; y|z) echo bar ;; esac",
		"",
	},
	{
		"case foo in bar) echo foo ;; *) echo bar ;; esac",
		"bar\n",
	},
	{
		"case foo in *o*) echo bar ;; esac",
		"bar\n",
	},
	{
		"case foo in '*') echo x ;; f*) echo y ;; esac",
		"y\n",
	},
	{
		"euro=$'\\342\\202\\254'; b=$'\\202'; case $euro in *$b*) echo bytematch ;; *) echo mbchar ;; esac",
		"bytematch\n",
	},

	// exec
	{
		"$GOSH_PROG 'echo foo'",
		"foo\n",
	},
	{
		"$GOSH_PROG 'echo foo >&2' >/dev/null",
		"foo\n",
	},
	{
		"echo foo | $GOSH_PROG 'cat >&2' >/dev/null",
		"foo\n",
	},
	{
		"$GOSH_PROG 'exit 1'",
		"exit status 1",
	},
	{
		"exec >/dev/null; echo foo",
		"",
	},

	// return
	{"return", "return: can only `return' from a function or sourced script\nexit status 2 #JUSTERR"},
	{"f() { return; }; f", ""},
	{"f() { return 2; }; f", "exit status 2"},
	{"f() { echo foo; return; echo bar; }; f", "foo\n"},
	{"f1() { :; }; f2() { f1; return; }; f2", ""},
	{"echo 'return' >a; source ./a", ""},
	{"echo 'return' >a; source ./a; return", "return: can only `return' from a function or sourced script\nexit status 2 #JUSTERR"},
	{"echo 'return 2' >a; source ./a", "exit status 2"},
	{"echo 'echo foo; return; echo bar' >a; source ./a", "foo\n"},

	// command
	{"command", ""},
	{"command -o echo", "command: invalid option \"-o\"\nexit status 2 #JUSTERR"},
	{"command -vo echo", "command: invalid option \"-o\"\nexit status 2 #JUSTERR"},
	{"echo() { :; }; echo foo", ""},
	{"echo() { :; }; command echo foo", "foo\n"},
	{"command -v does-not-exist", "exit status 1"},
	{"foo() { :; }; command -v foo", "foo\n"},
	{"foo() { :; }; command -v does-not-exist foo", "foo\n"},
	{"command -v echo", "echo\n"},
	{"[[ $(command -v $PATH_PROG) == $PATH_PROG ]]", "exit status 1"},

	// cmd substitution
	{
		"echo foo $(printf bar)",
		"foo bar\n",
	},
	{
		"echo foo $(echo bar)",
		"foo bar\n",
	},
	{
		"$(echo echo foo bar)",
		"foo bar\n",
	},
	{
		"for i in 1 $(echo 2 3) 4; do echo $i; done",
		"1\n2\n3\n4\n",
	},
	{
		"echo 1$(echo 2 3)4",
		"12 34\n",
	},
	{
		// A `[[ ]]` test whose final `]]` abuts the closing backtick of
		// the enclosing backquote command substitution must still close
		// the test (bash 5.3). Single-operand form is the regressing one.
		"echo a `[[ -n x ]] && echo y` b",
		"a y b\n",
	},
	{
		`echo "a ` + "`[[ x ]]`" + ` b"`,
		"a  b\n",
	},
	{
		`mkdir d; [[ $(cd d && pwd) == "$(pwd)" ]]`,
		"exit status 1",
	},
	{
		"a=sub true & { a=main $ENV_PROG | grep '^a='; }",
		"a=main\n",
	},
	{
		"echo foo >f; echo $(cat f); echo $(<f)",
		"foo\nfoo\n",
	},
	{
		// POSIX order: a command's words (here a command substitution
		// reading f) are expanded BEFORE its redirection truncates f.
		"echo hi >f; echo $(cat f) x >f; cat f",
		"hi x\n",
	},
	{
		"echo foo >f; echo $(<f*)",
		"foo\n",
	},
	{
		"echo foo >f; p=f*; echo $(<$p)",
		"foo\n",
	},
	{
		"set -o posix; echo foo >f; echo $(<f*)",
		"f*: No such file or directory\n\n #JUSTERR",
	},
	{
		"echo foo >f1; echo foo >f2; echo $(<f*)",
		"f*: ambiguous redirect\n\n #JUSTERR",
	},
	{
		"echo foo >f1; echo foo >f2; p=f*; echo $(<$p)",
		"$p: ambiguous redirect\n\n #JUSTERR",
	},
	{
		"echo foo >f; echo $(<f; echo bar)",
		"bar\n",
	},
	{
		"$(false); echo $?; $(exit 3); echo $?; $(true); echo $?",
		"1\n3\n0\n",
	},
	{
		"foo=$(false); echo $?; echo foo $(false); echo $?",
		"1\nfoo\n0\n",
	},
	{
		"$(false) $(true); echo $?; $(true) $(false); echo $?",
		"0\n1\n",
	},
	{
		"foo=$(false) $(true); echo $?; foo=$(true) $(false); echo $?",
		"1\n0\n",
	},

	// pipes
	{
		"echo foo | sed 's/o/a/g'",
		"faa\n",
	},
	{
		"echo foo | false | true",
		"",
	},
	{
		"true $(true) | true", // used to panic
		"",
	},
	{
		// The first command in the block used to consume stdin, even
		// though it shouldn't be. We just want to run any arbitrary
		// non-builtin program that doesn't consume stdin.
		"echo foo | { $ENV_PROG >/dev/null; cat; }",
		"foo\n",
	},

	// redirects
	{
		"echo foo >&1 | sed 's/o/a/g'",
		"faa\n",
	},
	{
		"echo foo >&2 | sed 's/o/a/g'",
		"foo\n",
	},
	{
		// TODO: why does bash need a block here?
		"{ echo foo >&2; } |& sed 's/o/a/g'",
		"faa\n",
	},
	{
		"echo foo >/dev/null; echo bar",
		"bar\n",
	},
	{
		">a; echo foo >>b; wc -c <a >>b; cat b | tr -d ' '",
		"foo\n0\n",
	},
	{
		`set -o posix; x="a b"; : > $x; printf 'rc=%s file=%s\n' "$?" "$([ -f "a b" ] && echo yes || echo no)"`,
		"rc=0 file=yes\n",
	},
	{
		`set -o posix; touch f1 f2; : > f*; printf '<%s>\n' *`,
		"<f*>\n<f1>\n<f2>\n",
	},
	{
		`x="a b"; : > $x`,
		"$x: ambiguous redirect\nexit status 1 #JUSTERR",
	},
	{
		"echo foo >a; <a",
		"",
	},
	{
		"echo foo >a; mkdir b; cd b; cat <../a",
		"foo\n",
	},
	{
		"echo foo >a; wc -c <a | tr -d ' '",
		"4\n",
	},
	{
		"echo foo >>a; echo bar &>>a; wc -c <a | tr -d ' '",
		"8\n",
	},
	{
		"{ echo a; echo b >&2; } &>/dev/null",
		"",
	},
	{
		"exec 3>&1 4>&2; exec >&a; echo out; echo err >&2; exec 1>&3 2>&4; cat a",
		"out\nerr\n",
	},
	{
		// >| force-overwrite; equivalent to > when noclobber is unset.
		"echo foo >| a; cat a",
		"foo\n",
	},
	{
		// >| overwrites an existing file.
		"echo foo >a; echo bar >| a; cat a",
		"bar\n",
	},
	{
		"echo foo >a; set -C; echo bar >a; cat a",
		"bash: line 1: a: cannot overwrite existing file\nfoo\n",
	},
	{
		"echo foo >a; set -C; echo bar >| a; cat a",
		"bar\n",
	},
	{
		": 2>/dev/null <$((foo+=42)); echo $foo",
		"42\n",
	},
	{
		"echo foo >a; exec 3<a; echo bad 2>/dev/null >&3; echo ok",
		"ok\n",
	},
	{
		// <> opens for read-write; the file must be readable as stdin.
		"echo foo >a; cat <>a",
		"foo\n",
	},
	{
		// <> creates the target file if it does not exist.
		"cat <>missing; ls missing",
		"missing\n",
	},
	{
		"sed 's/o/a/g' <<EOF\nfoo$foo\nEOF",
		"faa\n",
	},
	{
		"sed 's/o/a/g' <<'EOF'\nfoo$foo\nEOF",
		"faa$faa\n",
	},
	{
		"sed 's/o/a/g' <<EOF\n\tfoo\nEOF",
		"\tfaa\n",
	},
	{
		"sed 's/o/a/g' <<EOF\nfoo\nEOF",
		"faa\n",
	},
	{
		"cat <<EOF\n~/foo\nEOF",
		"~/foo\n",
	},
	{
		"sed 's/o/a/g' <<<foo$foo",
		"faa\n",
	},
	{
		"cat <<-EOF\n\tfoo\nEOF",
		"foo\n",
	},
	// Empty heredoc delimiter — terminated by an empty line. Both
	// quoted forms (`<<''` and `<<""`) and the tab-stripping `<<-''`
	// variant should round-trip body content.
	{"cat <<''\nhi\n\n", "hi\n"},
	{"cat <<\"\"\nhi\n\n", "hi\n"},
	{"cat <<-''\n\tindented\n\n", "indented\n"},
	{
		"cat <<-EOF\n\tfoo\n\nEOF",
		"foo\n\n",
	},
	{
		"cat <<EOF\nfoo\\\nbar\nEOF",
		"foobar\n",
	},
	{
		"cat <<'EOF'\nfoo\\\nbar\nEOF",
		"foo\\\nbar\n",
	},
	{
		"cat <<\\EOF\n\\$FUNCNAME\nEOF",
		"\\$FUNCNAME\n",
	},
	{
		// POSIX: if ANY part of the delimiter word is quoted, the
		// here-document body is not expanded. A quoted part followed
		// by an unquoted one (`<<''EOF`, `<<E''OF`) must still count.
		"cat <<''EOF\n${x} $((1+1)) $(echo hi)\nEOF",
		"${x} $((1+1)) $(echo hi)\n",
	},
	{
		"cat <<E''OF\n${x} $((1+1))\nEOF",
		"${x} $((1+1))\n",
	},
	{
		"cat <<EOF\nfoo\\\"bar\\baz\nEOF",
		"foo\\\"bar\\baz\n",
	},
	{
		"cat <<EOF\n \\\\ \\$ \\` \nEOF",
		" \\ $ ` \n",
	},
	{
		"cat <<'PY'\nhe said “smart” quotes\nPY",
		"he said “smart” quotes\n",
	},
	{
		"cat <<PY\nhe said “smart” quotes\nPY",
		"he said “smart” quotes\n",
	},
	{
		"cat <<'PY'\nprint(f“key: {v}”)\nPY",
		"print(f“key: {v}”)\n",
	},
	{
		"mkdir a; echo foo >a |& grep -q 'is a directory'",
		" #IGNORE bash prints a warning",
	},
	{
		"echo foo 1>&1 | sed 's/o/a/g'",
		"faa\n",
	},
	{
		"echo foo 2>&2 |& sed 's/o/a/g'",
		"faa\n",
	},
	{
		"printf 2>&1 | sed 's/.*usage.*/foo/'",
		"foo\n",
	},
	{
		"mkdir a && cd a && echo foo >b && cd .. && cat a/b",
		"foo\n",
	},
	{
		"echo foo 2>&-; :",
		"foo\n",
	},
	{
		// `>&-` closes stdout or stderr. Note that any writes result in errors.
		"echo foo >&- 2>&-; :",
		"",
	},
	{
		"echo foo | sed $(read line 2>/dev/null; echo 's/o/a/g')",
		"",
	},
	{
		// `<&-` closes stdin, to e.g. ensure that a subshell does not consume
		// the standard input shared with the parent shell.
		// Note that any reads result in errors.
		"echo foo | sed $(exec <&-; read line 2>/dev/null; echo 's/o/a/g')",
		"faa\n",
	},
	{
		// Concurrent pipe commands used to cause races when modifying the environment.
		"a=1 b=2 c=3 d=4 e=5 : | a=1 b=2 c=3 d=4 e=5 : | a=1 b=2 c=3 d=4 e=5 : | a=1 b=2 c=3 d=4 e=5 :",
		"",
	},
	{
		// Windows hold: this all-in-process pipeline should not inherit an
		// already-closed pipe endpoint from the parent lifecycle candidate.
		"printf 'foo\\nbar\\n' >f; cat f | tr -s o | head -1",
		"fo\n",
	},

	// background/wait
	{"wait", ""},
	{"wait foo", "wait: `foo': not a pid or valid job spec\nexit status 1 #JUSTERR"},

	// disown reports an absent current job even without job control.
	{"disown", "bash: line 1: disown: current: no such job\nexit status 1"},
	{"disown -a", ""},
	{"disown -h -r", ""},
	{"disown 12345", "bash: line 1: disown: 12345: no such job\nexit status 1"},
	{"disown %1", "bash: line 1: disown: %1: no such job\nexit status 1"},
	{"true & disown; echo done", "done\n"},
	{"set -e; disown -a; echo ok", "ok\n"},
	{"disown -z", "disown: invalid option \"-z\"\nexit status 2 #JUSTERR"},

	// kill — argv parsing / -l listing / error paths
	{"kill", "kill: usage: kill [-s sigspec | -n signum | -sigspec] pid | jobspec ... or kill -l [sigspec]\nexit status 2 #JUSTERR"},
	{"kill foo", "kill: `foo': not a pid or valid job spec\nexit status 1 #JUSTERR"},
	{"kill %1", "kill: %1: no such job\nexit status 1 #JUSTERR"},
	{"kill -s NOSIG 1", "kill: NOSIG: invalid signal specification\nexit status 1 #JUSTERR"},
	{"kill -NOSIG 1", "kill: NOSIG: invalid signal specification\nexit status 1 #JUSTERR"},
	{"kill -s", "kill: -s: option requires an argument\nexit status 2 #JUSTERR"},
	{"kill -INT ''", "kill: `': not a pid or valid job spec\nexit status 1 #JUSTERR"},
	{"kill -INT", "kill: usage: kill [-s sigspec | -n signum | -sigspec] pid | jobspec ... or kill -l [sigspec]\nexit status 2 #JUSTERR"},
	{"kill -l BAD", "kill: BAD: invalid signal specification\nexit status 1 #JUSTERR"},
	{"kill -HUP @12", "kill: `@12': not a pid or valid job spec\nexit status 1 #JUSTERR"},
	{"kill -l TERM", "15\n"},
	{"kill -l SIGTERM", "15\n"},
	{"kill -l 15", "TERM\n"},
	{"kill -l 129", "HUP\n"},
	{"kill -l | head -1", killListFirstLine()},
	{"diff <(kill -l) <(trap -l)", ""},
	{"kill -l KILL INT", "9\n2\n"},
	{"set -o posix; set -- $(kill -l); echo \"$1 $2 $3 $4 $5\"; kill -SIGTERM 999999; echo rc=$?", "HUP INT QUIT ILL TRAP\nbash: line 1: kill: SIGTERM: invalid signal specification\nrc=1\n"},

	// setsid / nohup — usage / lookup errors. Real-subprocess delivery is
	// covered in builtin_proc_test.go (unix-only).
	{"setsid", "setsid: usage: setsid [-f] [-w] [-c] <program> [args...]\nexit status 2 #JUSTERR"},
	{"setsid -z foo", "setsid: invalid option: \"-z\"\nexit status 2 #JUSTERR"},
	{"setsid nonexistent-binary-xyz", "setsid: \"nonexistent-binary-xyz\": executable file not found in $PATH\nexit status 127 #JUSTERR"},
	{"nohup", "nohup: usage: nohup <program> [args...]\nexit status 125 #JUSTERR"},
	{"nohup nonexistent-binary-xyz", "nohup: \"nonexistent-binary-xyz\": executable file not found in $PATH\nexit status 127 #JUSTERR"},

	{"{ true; } & wait", ""},
	{"{ false; } & wait", ""},
	{"{ sleep 0.01; true; } & wait", ""},
	{"{ sleep 0.01; false; } & wait", ""},
	{
		"{ echo foo; } & wait; echo bar",
		"foo\nbar\n",
	},
	{
		"{ echo foo & wait; } & wait; echo bar",
		"foo\nbar\n",
	},
	{`mkdir d; old=$PWD; cd d & wait; [[ $old == "$PWD" ]]`, ""},
	{
		"f() { echo 1; }; { sleep 0.01; f; } & f() { echo 2; }; wait",
		"1\n",
	},
	{"[[ -n $! ]]", "exit status 1"},
	{"true & [[ -n $! ]]", ""},
	{"set -u; true & [[ -n $! ]]", ""},
	{"set -u; true & echo $! & wait", "g1\n #IGNORE S275 decision: background builtins run as goroutines, so $! is a job id (g1), not a kernel PID"},
	{"set -u; true & (echo $!) & wait", "g1\n #IGNORE S275 decision: background builtins run as goroutines, so $! is a job id (g1), not a kernel PID"},
	{"set -u; true & (wait $!; echo $?) & wait", "bash: line 1: wait: pid g1 is not a child of this shell\n127\n #IGNORE S275 decision: background builtins run as goroutines, so $! is a job id (g1), not a kernel PID"},
	{"true & true;  [[ -n $! ]]", ""},
	{"true & pid=$!; wait $pid", ""},
	{"false & pid=$!; wait $pid", "exit status 1"},
	{"{ sleep 0.01; true; } & pid=$!; wait $pid", ""},
	{"{ sleep 0.01; false; } & pid=$!; wait $pid", "exit status 1"},
	{"set -o posix; sleep 0.1 & pid=$!; kill -s INT $pid; kill -s QUIT $pid; wait $pid; echo status:$?", "status:0\n #IGNORE S275 decision: POSIX async lists ignore INT/QUIT (VSC-PCTS TP8); GNU Bash 5.3.0 on Linux reports 130"},
	{"set -o posix; trap 'echo foo' EXIT >/dev/null & wait $!", ""},
	{
		"set -o posix; kill() (trap 'sleep 0' EXIT; (trap 'sleep 0' EXIT; (trap 'sleep 0' EXIT; command kill \"$@\"))); trap X USR1; alias X='echo 1'; kill -s USR1 $$; alias X='echo 2'; kill -s USR1 $$",
		"1\n2\n",
	},
	// POSIX interp 1602 applies to the trap action itself, NOT to functions the
	// action calls: fn's bare `return` yields the status of `true` (0), not the
	// $? in effect when the trap fired (19). Verified against bash 5.3 in both
	// default and posix mode; this case previously asserted "trapped 19", which
	// bash does not produce, and the code was widened to match it — regressing
	// the bash-5.3 trap fixture (tests/trap9.sub). See the next case.
	{
		"set -o posix; fn() { true; return; }; trap 'fn; echo trapped $?' USR1; (exit 19); (kill -s USR1 $$; exit 19); :",
		"trapped 0\n",
	},
	// bash-5.3 tests/trap9.sub, posix interp 1602: the `return` in `check` does
	// not end execution of the trap action, and yields false's status (1), so
	// `check && echo B || echo A` prints A.
	{
		"check() { false; return; }; handle() { check && echo B || echo A; }; trap handle USR1; kill -USR1 $$",
		"A\n",
	},
	{
		"trap 'return 0' USR1; loop() { kill -s USR1 $$; while :; do :; done; }; get_loop_exit() { loop; echo exit=$?; }; get_loop_exit",
		"exit=0\n",
	},
	// An async subshell can replace INT's inherited ignore and reset QUIT.
	// Inspect that child's dispositions: $$ still names the parent, so
	// signaling $$ cannot test the child's callback or terminating status.
	{
		"set -o posix; (trap 'echo got INT' INT; trap -p INT; trap - QUIT; trap -p QUIT) & wait $!; echo status:$?",
		"trap -- 'echo got INT' INT\ntrap -- - QUIT\nstatus:0\n",
	},
	{"(true) & ok=$!; (false) & fail=$!; wait $ok $fail", "exit status 1"},
	{"(true) & ok=$!; (false) & ignore=$!; wait $ok", ""},
	{"echo foo | true | false & wait $!", "exit status 1"},
	{"echo foo | false | true & wait $!", ""},
	{"f() { false & true; }; f; wait $!", "exit status 1"},
	// The parent and child shells should not cause data races when setting env vars.
	// Note that we can't use `echo $var`, as it seems to write newlines separately,
	// which can cause them to get mixed up between concurrent subshells.
	{
		"{ for n in {0..9}; do { echo -n $n$'\n'; } & done; wait; } | sort",
		"0\n1\n2\n3\n4\n5\n6\n7\n8\n9\n",
	},
	{
		"outer=val; for n in {0..9}; do { echo -n $outer$'\n'; } & outer=val; done; wait",
		"val\nval\nval\nval\nval\nval\nval\nval\nval\nval\n",
	},
	{
		"for n in {0..9}; do { inner=val; } & echo $inner; done",
		"\n\n\n\n\n\n\n\n\n\n",
	},
	{
		"exit 2 & bg1=$!; exit 0 & bg2=$!; wait $bg1 $bg2; echo $?",
		"0\n",
	},
	{
		"exit 2 & bg1=$!; exit 4 & bg2=$!; wait $bg1 $bg2; echo $?",
		"4\n",
	},

	// bash test
	{
		"[[ a ]]",
		"",
	},
	{
		"[[ '' ]]",
		"exit status 1",
	},
	{
		"[[ '' ]]; [[ a ]]",
		"",
	},
	{
		"[[ ! (a == b) ]]",
		"",
	},
	{
		"[[ a != b ]]",
		"",
	},
	{
		"[[ a && '' ]]",
		"exit status 1",
	},
	{
		"[[ a || '' ]]",
		"",
	},
	{
		"[[ a > 3 ]]",
		"",
	},
	{
		"[[ a < 3 ]]",
		"exit status 1",
	},
	{
		"[[ 3 == 03 ]]",
		"exit status 1",
	},
	{
		"[[ a -eq b ]]",
		"",
	},
	{
		"[[ 3 -eq 03 ]]",
		"",
	},
	{
		"[[ 3 -ne 4 ]]",
		"",
	},
	{
		"[[ 3 -le 4 ]]",
		"",
	},
	{
		"[[ 3 -ge 4 ]]",
		"exit status 1",
	},
	{
		"[[ 3 -ge 3 ]]",
		"",
	},
	{
		"[[ 3 -lt 4 ]]",
		"",
	},
	{
		"[[ ' 3' -lt '4 ' ]]",
		"",
	},
	{
		"[[ 3 -gt 4 ]]",
		"exit status 1",
	},
	{
		"[[ 3 -gt 3 ]]",
		"exit status 1",
	},
	{
		"[[ a -nt a || a -ot a ]]",
		"exit status 1",
	},
	{
		"touch -t 202111050000.30 a b; [[ a -nt b || a -ot b ]]",
		"exit status 1",
	},
	{
		"touch -t 202111050200.00 a; touch -t 202111060100.00 b; [[ a -nt b ]]",
		"exit status 1",
	},
	{
		"touch -t 202111050000.00 a; touch -t 202111060000.00 b; [[ a -ot b ]]",
		"",
	},
	{
		"[[ a -ef b ]]",
		"exit status 1",
	},
	{
		">a >b; [[ a -ef b ]]",
		"exit status 1",
	},
	{
		">a; [[ a -ef a ]]",
		"",
	},
	{
		">a; ln a b; [[ a -ef b ]]",
		"",
	},
	{
		">a; ln -s a b; [[ a -ef b ]]",
		"",
	},
	{
		"[[ -z 'foo' || -n '' ]]",
		"exit status 1",
	},
	{
		"[[ -z '' && -n 'foo' ]]",
		"",
	},
	{
		"a=x b=''; [[ -v a && -v b && ! -v c ]]",
		"",
	},
	{
		"[[ abc == *b* ]]",
		"",
	},
	{
		"[[ abc != *b* ]]",
		"exit status 1",
	},
	{
		"[[ *b = '*b' ]]",
		"",
	},
	{
		"[[ ab == a. ]]",
		"exit status 1",
	},
	{
		`x='*b*'; [[ abc == $x ]]`,
		"",
	},
	{
		`x='*b*'; [[ abc == "$x" ]]`,
		"exit status 1",
	},
	{
		`[[ abc == \a\bc ]]`,
		"",
	},
	{
		"[[ abc != *b'*' ]]",
		"",
	},
	{
		"[[ a =~ b ]]",
		"exit status 1",
	},
	{
		"[[ foo =~ foo && foo =~ .* && foo =~ f.o ]]",
		"",
	},
	{
		"[[ foo =~ oo ]] && echo foo; [[ foo =~ ^oo$ ]] && echo bar || true",
		"foo\n",
	},
	{
		`[[ x =~ \x ]]; echo $?; [[ a-b =~ a\-b ]]; echo $?; c=$'\001'; [[ $c =~ \$c ]]; echo $?`,
		"0\n0\n1\n",
	},
	{
		`[[ dog =~ [[=d=]].. ]] && echo ok1; [[ dog =~ [[.d.][.D.]]o. ]] && echo ok2; [[ dog =~ ([[.d.][.D.]])o(.) ]] && echo "${BASH_REMATCH[1]} ${BASH_REMATCH[2]}"`,
		"ok1\nok2\nd g\n",
	},
	{
		`[[ ']' =~ [']'] ]] && echo rb; [[ a =~ ['a]'] ]] || echo no; [[ a] =~ ['a]'] ]] && echo lit`,
		"rb\nno\nlit\n",
	},
	{
		"[[ a =~ [ ]]",
		"[[: invalid regular expression `[': Unmatched [, [^, [:, [., or [=\nexit status 2 #JUSTERR",
	},
	{
		"[[ a__b__c =~ _*(b_*) ]]; echo ${BASH_REMATCH[0]}; echo ${BASH_REMATCH[1]}",
		"__b__\nb__\n",
	},
	{
		"[[ -e a ]] && echo x; >a; [[ -e a ]] && echo y",
		"y\n",
	},
	{
		"ln -s b a; [[ -e a ]] && echo x; >b; [[ -e a ]] && echo y",
		"y\n",
	},
	{
		"[[ -f a ]] && echo x; >a; [[ -f a ]] && echo y",
		"y\n",
	},
	{
		"[[ -e a ]] && echo x; mkdir a; [[ -e a ]] && echo y",
		"y\n",
	},
	{
		"[[ -d a ]] && echo x; mkdir a; [[ -d a ]] && echo y",
		"y\n",
	},
	{
		"[[ -r a ]] && echo x; >a; [[ -r a ]] && echo y",
		"y\n",
	},
	{
		"[[ -w a ]] && echo x; >a; [[ -w a ]] && echo y",
		"y\n",
	},
	{
		"[[ -s a ]] && echo x; echo body >a; [[ -s a ]] && echo y",
		"y\n",
	},
	{
		"[[ -L a ]] && echo x; ln -s b a; [[ -L a ]] && echo y;",
		"y\n",
	},
	{
		"[[ \"multiline\ntext\" == *text* ]] && echo x; [[ \"multiline\ntext\" == *multiline* ]] && echo y",
		"x\ny\n",
	},
	// * should match a newline
	{
		"[[ \"multiline\ntext\" == multiline*text ]] && echo x",
		"x\n",
	},
	{
		"[[ \"multiline\ntext\" == text ]]",
		"exit status 1",
	},
	{
		`case $'a\nb' in a*b) echo match ;; esac`,
		"match\n",
	},
	{
		`a=$'a\nb'; echo "${a/a*b/sub}"`,
		"sub\n",
	},
	{
		"mkdir a; cd a; test -f b && echo x; >b; test -f b && echo y",
		"y\n",
	},
	{
		">a; [[ -b a ]] && echo block; [[ -c a ]] && echo char; true",
		"",
	},
	{
		"[[ -e /dev/sda ]] || { echo block; exit; }; [[ -b /dev/sda ]] && echo block; [[ -c /dev/sda ]] && echo char; true",
		"block\n",
	},
	{
		"[[ -e /dev/nvme0n1 ]] || { echo block; exit; }; [[ -b /dev/nvme0n1 ]] && echo block; [[ -c /dev/nvme0n1 ]] && echo char; true",
		"block\n",
	},
	{
		"[[ -e /dev/tty ]] || { echo char; exit; }; [[ -b /dev/tty ]] && echo block; [[ -c /dev/tty ]] && echo char; true",
		"char\n",
	},
	{"[[ -t 1 ]]", "exit status 1"},
	{"[[ -t 1234 ]]", "exit status 1"},
	{">a; exec 3<a; [[ -t 3 ]]", "exit status 1"},
	{">a; exec 4>a; [[ -t 4 ]]", "exit status 1"},
	{">a; exec 3<a; exec 3<&-; [[ -t 3 ]]", "exit status 1"},
	{"[[ -o wrong ]]", "exit status 1"},
	{"[[ -o errexit ]]", "exit status 1"},
	{"set -e; [[ -o errexit ]]", ""},
	{"[[ -o noglob ]]", "exit status 1"},
	{"set -f; [[ -o noglob ]]", ""},
	{"[[ -o allexport ]]", "exit status 1"},
	{"set -a; [[ -o allexport ]]", ""},
	{"[[ -o nounset ]]", "exit status 1"},
	{"set -u; [[ -o nounset ]]", ""},
	{"[[ -o noexec ]]", "exit status 1"},
	{"set -n; [[ -o noexec ]]", ""}, // actually does nothing, but oh well
	{"[[ -o pipefail ]]", "exit status 1"},
	{"set -o pipefail; [[ -o pipefail ]]", ""},
	// TODO: we don't implement precedence of && over ||.
	// {"[[ a == x && b == x || c == c ]]", ""},
	{"[[ (a == x && b == x) || c == c ]]", ""},
	{"[[ a == x && (b == x || c == c) ]]", "exit status 1"},

	// classic test
	{
		"[",
		"[: missing `]'\nexit status 2 #JUSTERR",
	},
	{
		"[ a",
		"[: missing `]'\nexit status 2 #JUSTERR",
	},
	{
		"[ a b c ]",
		"[: b: binary operator expected\nexit status 2 #JUSTERR",
	},
	{
		"[ a -a ]",
		"[: argument expected\nexit status 2 #JUSTERR",
	},
	// Bash 5.3: when a unary operator is followed by -a/-o with more
	// tokens remaining, -a/-o is resolved as the binary AND/OR rather
	// than the operand of the unary op (Oils builtin-bracket__022).
	{"[ -z -a ] ]", ""},
	{"[ -z -a -a ]", ""},
	{"test -z -a ]", ""},
	{"test -z -a -a", ""},
	{"[ -z -o ] ]", ""},
	{"[ -z -o -o ]", ""},
	{"[ a ]", ""},
	{"[ -n ]", ""},
	{"[ '-n' ]", ""},
	{"[ -z ]", ""},
	{"[ ! ]", ""},
	{"[ a != b ]", ""},
	{"[ ! a '==' a ]", "exit status 1"},
	{"[ a -a 0 -gt 1 ]", "exit status 1"},
	{"[ 0 -gt 1 -o 1 -gt 0 ]", ""},
	{"[ 3 -gt 4 ]", "exit status 1"},
	{"[ 3 -lt 4 ]", ""},
	{"[ ' 3' -lt '4 ' ]", ""},
	{
		"[ -e a ] && echo x; >a; [ -e a ] && echo y",
		"y\n",
	},
	{
		"test 3 -gt 4",
		"exit status 1",
	},
	{
		"test 3 -lt 4",
		"",
	},
	{
		"test 3 -lt",
		"test: syntax error: `-lt' unexpected\nexit status 2 #JUSTERR",
	},
	{
		"touch -t 202111050000.00 a; touch -t 202111060000.00 b; [ a -nt b ]",
		"exit status 1",
	},
	{
		"touch -t 202111050000.00 a; touch -t 202111060000.00 b; [ a -ot b ]",
		"",
	},
	{
		">a; [ a -ef a ]",
		"",
	},
	{"[ 3 -eq 04 ]", "exit status 1"},
	{"[ 3 -eq 03 ]", ""},
	{"[ 3 -ne 03 ]", "exit status 1"},
	{"[ 3 -le 4 ]", ""},
	{"[ 3 -ge 4 ]", "exit status 1"},
	{
		"[ -d a ] && echo x; mkdir a; [ -d a ] && echo y",
		"y\n",
	},
	{
		"[ -r a ] && echo x; >a; [ -r a ] && echo y",
		"y\n",
	},
	{
		"[ -w a ] && echo x; >a; [ -w a ] && echo y",
		"y\n",
	},
	{
		// A directory is readable, writable, and executable.
		"mkdir d; [ -r d ] && echo r; [ -w d ] && echo w; [ -x d ] && echo x",
		"r\nw\nx\n",
	},
	{
		"test -N a",
		"exit status 1",
	},
	{
		"test -? a",
		// TODO: this error message should refer to `-?`
		"test: -?: unary operator expected\nexit status 2 #JUSTERR",
	},
	{
		"[ -s a ] && echo x; echo body >a; [ -s a ] && echo y",
		"y\n",
	},
	{
		"[ -L a ] && echo x; ln -s b a; [ -L a ] && echo y;",
		"y\n",
	},
	{
		">a; [ -b a ] && echo block; [ -c a ] && echo char; true",
		"",
	},
	{"[ -t 1 ]", "exit status 1"},
	{"[ -t 1234 ]", "exit status 1"},
	{">a; exec 3<a; [ -t 3 ]", "exit status 1"},
	{">a; exec 4>a; [ -t 4 ]", "exit status 1"},
	{"[ -o wrong ]", "exit status 1"},
	{"[ -o errexit ]", "exit status 1"},
	{"set -e; [ -o errexit ]", ""},
	{"a=x b=''; [ -v a -a -v b -a ! -v c ]", ""},
	{"[ a = a ]", ""},
	{"[ a != a ]", "exit status 1"},
	{"[ abc = ab* ]", "exit status 1"},
	{"[ abc != ab* ]", ""},
	// TODO: we don't implement precedence of -a over -o.
	// {"[ a = x -a b = x -o c = c ]", ""},
	{`[ \( a = x -a b = x \) -o c = c ]`, ""},
	{`[ a = x -a \( b = x -o c = c \) ]`, "exit status 1"},

	// arithm
	{
		"echo $((1 == +1))",
		"1\n",
	},
	{"echo $((4 ? 1 : 0))", "1\n"},
	{"A='3 + 5'; echo $((4 ? : $A)); echo after", "bash: line 1: 4 ? : 3 + 5: expression expected (error token is \": 3 + 5\")\nexit status 1"},
	{"echo $((1 ? 20)); echo after", "bash: line 1: 1 ? 20: `:' expected for conditional expression (error token is \"20\")\nexit status 1"},
	{"echo $((4 ? 20 :)); echo after", "bash: line 1: 4 ? 20 :: expression expected (error token is \":\")\nexit status 1"},
	{"echo $((2**-1)); echo after", "bash: line 1: 2**-1: exponent less than 0 (error token is \"1\")\nexit status 1"},
	{"A='4 + '; echo $(((4 + A) + 4)); echo after", "bash: line 1: 4 + : arithmetic syntax error: operand expected (error token is \"+ \")\nexit status 1"},
	{"echo $((++7)); echo $((--7))", "7\n7\n"},
	{"((++)); echo $?", "bash: line 1: ((: ++: arithmetic syntax error: operand expected (error token is \"+\")\n1\n"},
	{"echo $((+++7)); echo $((++ + 7)); echo $((---7)); echo $((-- - 7))", "7\n7\n-7\n-7\n"},
	{"a=1; echo $((4+++a)); echo $a; a=1; echo $((4---a)); echo $a", "6\n2\n4\n0\n"},
	{"readonly xx=5; echo $((xx=5)); echo $?", "bash: line 1: xx: readonly variable\nexit status 1"},
	// POSIX mode: an arithmetic assignment to a readonly variable is a
	// fatal variable-assignment error for a non-interactive shell, so the
	// trailing command never runs.
	{"set -o posix; readonly xx=5; echo $((xx=5)); echo unreached", "xx: readonly variable\nexit status 1 #JUSTERR"},
	{"x=1; ((x=2, y=x)); echo $x $y", "2 2\n"},
	{"x=(456 123); (( x[1] < x && (x=x[1], x[1]=$x) )); echo ${x[@]}", "123 456\n"},
	{"x=(456 123); (( x[1] < x[0] && (x[0]=x[1], x[1]=$x) )); echo ${x[@]}", "123 456\n"},
	{`declare -a a; a[0]=0; let a\[" "\]=13; declare -p a; let "a[\" \"]"=18; declare -p a; let "a[\"\"]"=22; declare -p a`, "declare -a a=([0]=\"13\")\ndeclare -a a=([0]=\"18\")\ndeclare -a a=([0]=\"22\")\n"},
	{"n=0; (( a[n]=++n )); echo $n ${a[@]}", "1 1\n"},
	{"n=0 a='(a[n]=++n)<7&&a[0]'; ((a[0])); echo ${a[@]:1}", "1 2 3 4 5 6 7\n"},
	{"set -u; echo $((a > 4)); echo after", "a: unbound variable\nexit status 1 #JUSTERR"},
	{"set -u; echo $((a[0] > 4)); echo after", "a: unbound variable\nexit status 1 #JUSTERR"},
	{"set -u; a=(); echo $((a[0] > 4)); echo after", "0\nafter\n"},
	{"set -u; a=(); echo $((a[b] > 4)); echo after", "b: unbound variable\nexit status 1 #JUSTERR"},
	// Assigning an element makes a `declare -a` array set, so a later
	// ${arr[i]} read under `set -u` does not falsely error (matches bash).
	{"set -u; declare -a B; B[0]=x; echo \"[${B[0]}]\"", "[x]\n"},
	{"set -u; declare -a B; B[0]=\"\"; echo \"[${B[0]}]\"", "[]\n"},
	{"set -u; declare -a B; for i in 0 1; do B[$i]=v$i; done; echo \"${B[0]}|${B[1]}\"", "v0|v1\n"},
	// A declared-but-unassigned array element is still unset under `set -u`.
	{"set -u; declare -a B; echo \"${B[0]}\"; echo after", "B[0]: unbound variable\nexit status 1 #JUSTERR"},
	// Reading a SET non-zero element under `set -u` must not falsely error,
	// even when element 0 was never assigned (the array's scalar value is
	// unset but the addressed element is set). Regression for a[2]=z.
	{"set -u; a[2]=z; echo \"${a[2]}\"", "z\n"},
	{"set -u; declare -A m; m[k]=v; echo \"${m[k]}\"", "v\n"},
	// A genuinely-unset element still errors, set array or not.
	{"set -u; a[0]=x; echo \"${a[5]}\"; echo after", "a[5]: unbound variable\nexit status 1 #JUSTERR"},
	{"set -u; declare -A m; m[k]=v; echo \"${m[no]}\"; echo after", "m[no]: unbound variable\nexit status 1 #JUSTERR"},
	{"a=b b=a; echo $((a + 7)); echo after", "bash: line 1: b: expression recursion level exceeded (error token is \"b\")\nexit status 1"},
	{"x=4+; declare -i x; x+=7 y=4; echo x=$x y=$y", "bash: line 1: 4+: arithmetic syntax error: operand expected (error token is \"+\")\nexit status 1"},
	{"x=8; echo $((--x++)); echo after", "bash: line 1: --x++: ++: assignment requires lvalue (error token is \"++\")\nexit status 1"},
	{"HOME=/usr/homes/chet; echo \"${HOME:`echo }`}\"; echo after", "bash: line 1: HOME: }: arithmetic syntax error: operand expected (error token is \"}\")\nexit status 1"},
	{"foo=1; echo $(( 'foo' )); echo after", "bash: line 1: 'foo' : arithmetic syntax error: operand expected (error token is \"'foo' \")\nexit status 1"},
	{"set -- a b c d op; echo ${!#}; v=bad-var; echo ${!v}; echo after", "op\nbash: line 1: bad-var: invalid variable name\nexit status 1"},
	{"set -- a 'b c' d; foo=@; printf '<%s>\\n' ${!foo}; printf 'Q<%s>\\n' \"${!foo}\"", "<a>\n<b>\n<c>\n<d>\nQ<a>\nQ<b c>\nQ<d>\n"},
	{"set -- a b c d e; echo ${6=arg6}; echo after", "bash: line 1: $6: cannot assign in this way\nexit status 1"},
	// `${#OP}` with an operand is the special parameter `$#` followed by
	// an expansion operator, not a malformed length operator: `=` leaves
	// the (always-set) count untouched, `+` substitutes the alternate, and
	// `%` trims a non-matching suffix.
	{"set -- a b c; echo ${#=x}", "3\n"},
	{"set -- a b c; echo ${#+alt}", "alt\n"},
	{"set -- a b c; echo ${#%x}", "3\n"},
	{"set -u; echo $9", "$9: unbound variable\nexit status 1 #JUSTERR"},
	{"set -u; echo ${9}", "9: unbound variable\nexit status 1 #JUSTERR"},
	{"v=abcde; echo ${v/#a/ab}; echo ${v/%?/last}; av=(abcd efgh); echo ${av[1]/#?/xx}; echo ${av[1]/%??/za}", "abbcde\nabcdlast\nxxfgh\nefza\n"},
	{"av=(abcd efgh ijkl); printf '<%s>\\n' ${av[@]/%??/xx}; set -- abcd efgh ijkl; printf 'P<%s>\\n' ${@/#??/za}", "<abxx>\n<efxx>\n<ijxx>\nP<zacd>\nP<zagh>\nP<zakl>\n"},
	{"IFS=; B=''; echo \"<${B[*]:-X}>\" \"<${B[@]:-X}>\"", "<X> <X>\n"},
	{"IFS=; A=('' ''); echo \"<${A[*]:-X}>\"", "<X>\n"},
	{"IFS=+; arr=(a b c); echo ${arr[*]/a/x}; set -- 'a+b' 'c+d' 'e+f'; echo ${*/a/x}", "x b c\nx b c d e f\n"},
	{"IFS=+; a=(aa bb); printf '<%s>\\n' ${a[@]} ${a[@]:0}", "<aa>\n<bb>\n<aa>\n<bb>\n"},
	{"recho() { i=1; for arg; do echo \"$i:<$arg>\"; i=$((i+1)); done; }; a[0]= a[1]=; recho \"${a[@]:-y}\"; unset a; a[1]=; recho \"${a[@]:-y}\"; set -- '' x; recho \"${@:-y}\"", "1:<>\n2:<>\n1:<y>\n1:<>\n2:<x>\n"},
	{"a[0]= a[1]=x; printf '<%s>\\n' ${a[@]:+y}", "<y>\n"},
	{"set -- '' ''; echo \"<${*:-X}>\"", "< >\n"},
	{"_QUANTITY= _QUOTA= _QUOTE= _QUILL= _QUEST= _QUART=; IFS=-; printf '<%s>\\n' \"${!_Q*}\"; printf '<%s>\\n' \"${!_Q@}\"", "<_QUANTITY-_QUART-_QUEST-_QUILL-_QUOTA-_QUOTE>\n<_QUANTITY>\n<_QUART>\n<_QUEST>\n<_QUILL>\n<_QUOTA>\n<_QUOTE>\n"},
	{"_Q=1; echo \"${!_Q* }\"; echo after", "bash: line 1: ${!_Q* }: bad substitution\nexit status 1"},
	{"set -- a b; echo ${!1*}; echo ${!@*}; echo after", "bash: line 1: ${!1*}: bad substitution\nexit status 1"},
	// An invalid substitution terminates a non-interactive Bash script.
	{"echo ${#+} second; echo after: $?", "bash: line 1: ${#+}: bad substitution\nexit status 1"},
	// Malformed expansions which bash accepts at parse time and
	// rejects at expansion time.
	{"echo ${x!y} second; echo after: $?", "bash: line 1: ${x!y}: bad substitution\nexit status 1"},
	{"echo ${#foo%} second; echo after: $?", "bash: line 1: ${#foo%}: bad substitution\nexit status 1"},
	// Bash does not support zsh-style nested ${...}/$(...); it parses
	// them at face value and rejects them at expansion time.
	{`c=""; echo ${c//${$(($#-1))}/x/}; echo after: $?`, "bash: line 1: ${$(($#-1))}: bad substitution\nexit status 1"},
	{"echo ${$(cmd)} second; echo after: $?", "bash: line 1: ${$(cmd)}: bad substitution\nexit status 1"},
	// Indirection through an unset variable terminates a non-interactive
	// Bash script, including when a default-style operator follows.
	{"echo ${!var:-unset}; echo after: $?", "bash: line 1: var: invalid indirect expansion\nexit status 1"},
	{"echo ${!var+set}; echo after: $?", "bash: line 1: var: invalid indirect expansion\nexit status 1"},
	// An invalid indirection target name terminates the script.
	{"x=-3; echo ${!x}; echo after: $?", "bash: line 1: -3: invalid variable name\nexit status 1"},
	// A blank (whitespace-only) subscript evaluates as index 0 for
	// indexed arrays and misses every associative array key.
	{"b[0]=4; echo ${b[   ]}; echo after: $?", "4\nafter: 0\n"},
	{"b[0]=4; echo ${b[' ']}; echo after: $?", "arithmetic syntax error: operand expected (error token is \"' '\")\nafter: 0\n #JUSTERR"},
	{"typeset -A v; v[0]=one; echo ${v[   ]}; echo after: $?", "\nafter: 0\n"},
	{"a[0]=0 a[1]=1; a[ ]=10; declare -p a; a[]=bad; echo after: $?", "declare -a a=([0]=\"10\" [1]=\"1\")\na[]: bad array subscript\nafter: 1\n #JUSTERR"},
	{"a[0]=0 a[1]=1; (( a[ ]=10 )); declare -p a; let 'a[ ]=11'; declare -p a; v=' '; let \"a[\\\"$v\\\"]=12\"; declare -p a", "declare -a a=([0]=\"10\" [1]=\"1\")\ndeclare -a a=([0]=\"11\" [1]=\"1\")\ndeclare -a a=([0]=\"12\" [1]=\"1\")\n"},
	{"declare -A a; a[0]=0 a[1]=1; v=' '; let \"a[\\\"$v\\\"]=13\"; declare -p a", "declare -A a=([1]=\"1\" [0]=\"0\" [\" \"]=\"13\" )\n"},
	{"shopt -s assoc_expand_once; declare -A a; a[0]=0 a[1]=1; let \"a[\\\" \\\"]=11\"; v=' '; let \"a[\\\"$v\\\"]=13\"; declare -p a", "declare -A a=([1]=\"1\" [0]=\"0\" [\"\\\" \\\"\"]=\"13\" )\n"},
	{"value='[$(echo total 0)]=1 [2]=2'; declare -a var=\"($value)\"; declare -p var", "1:9: not a valid arithmetic operator: `0`\ndeclare -a var=()\n #JUSTERR"},
	{"arrayA=(A B C); xx='arrayA[*]'; arrayB=( ${!xx} ); echo \"${#arrayB[*]}:${arrayB[0]}:${arrayB[1]}:${arrayB[2]}\"; arrayB=( \"${!xx}\" ); echo \"${#arrayB[*]}:${arrayB[0]}:${arrayB[1]}:${arrayB[2]}\"; xx='arrayA[@]'; arrayB=( ${!xx} ); echo \"${#arrayB[*]}:${arrayB[0]}:${arrayB[1]}:${arrayB[2]}\"; arrayB=( \"${!xx}\" ); echo \"${#arrayB[*]}:${arrayB[0]}:${arrayB[1]}:${arrayB[2]}\"", "3:A:B:C\n1:A B C::\n3:A:B:C\n3:A:B:C\n"},
	// Assignment binds lower than the ternary false branch in bash:
	// these parse like `(cond ? a : a) += 5`, which is not an lvalue.
	{"a=10; echo $((0 ? a : a+=5)); echo $a", "bash: line 1: 0 ? a : a+=5: attempted assignment to non-variable (error token is \"+=5\")\nexit status 1"},
	{"a=10; echo $((1 ? a*=2 : a+=5)); echo $a", "bash: line 1: 1 ? a*=2 : a+=5: attempted assignment to non-variable (error token is \"+=5\")\nexit status 1"},
	{"_ENV=oops; x=${_ENV[(_$-=0)+(_=1)-_${-%%*i*}]}; echo ${x:-unset}", "unset\n"},
	{
		"echo $((!0))",
		"1\n",
	},
	{
		"echo $((!3))",
		"0\n",
	},
	{
		"echo $((~0))",
		"-1\n",
	},
	{
		"echo $((~3))",
		"-4\n",
	},
	{
		"echo $((1 + 2 - 3))",
		"0\n",
	},
	{
		"echo $((-1 * 6 / 2))",
		"-3\n",
	},
	{
		"a=2; echo $(( a + $a + c ))",
		"4\n",
	},
	{
		"a=b; b=c; c=5; echo $((a % 3))",
		"2\n",
	},
	{
		"echo $((2 > 2 || 2 < 2))",
		"0\n",
	},
	{
		"echo $((2 >= 2 && 2 <= 2))",
		"1\n",
	},
	{
		"echo $(((1 & 2) != (1 | 2)))",
		"1\n",
	},
	{
		"echo $a; echo $((a = 3 ^ 2)); echo $a",
		"\n1\n1\n",
	},
	{
		"echo $((a += 1, a *= 2, a <<= 2, a >> 1))",
		"4\n",
	},
	{
		"echo $((a -= 10, a /= 2, a >>= 1, a << 1))",
		"-6\n",
	},
	{
		"echo $((a |= 3, a &= 1, a ^= 8, a %= 5, a))",
		"4\n",
	},
	{
		"echo $((a = 3, ++a, a--))",
		"4\n",
	},
	{
		"echo $((2 ** 3)) $((1234 ** 4567))",
		"8 0\n",
	},
	{
		"echo $((1 ? 2 : 3)) $((0 ? 2 : 3))",
		"2 3\n",
	},
	{
		"echo $((255+1))",
		"256\n",
	},
	{
		"echo $((0xff+1))",
		"256\n",
	},
	{
		"echo $((0377+1))",
		"256\n",
	},
	{
		"echo $((10#255+1))",
		"256\n",
	},
	{
		"echo $((16#ff+1))",
		"256\n",
	},
	{
		"echo $((2#11111111+1))",
		"256\n",
	},
	// TODO: Enable this test once integer bit widths are
	// handled in a consistent manner throughout the library.
	//{
	//	"echo $((16#badc0ffee+1))",
	//	"50159747055\n",
	//},
	{
		"echo $((16#cafe+1))",
		"51967\n",
	},
	{
		"echo $((nope+1))",
		"1\n", // Yes, this is what bash does.
	},
	{
		"((1))",
		"",
	},
	{
		"((3 == 4))",
		"exit status 1",
	},
	{
		"let i=(3+4); let i++; echo $i; let i--; echo $i",
		"8\n7\n",
	},
	{
		"let; echo $?",
		"bash: line 1: let: expression expected\n1\n",
	},
	{
		"let 3==4",
		"exit status 1",
	},
	{
		"a=1; let a++; echo $a",
		"2\n",
	},
	{
		"a=$((1 + 2)); echo $a",
		"3\n",
	},
	{
		"x=3; echo $(($x)) $((x))",
		"3 3\n",
	},
	{
		"set -- 1; echo $(($@))",
		"1\n",
	},
	{
		"a=b b=a; echo $(($a))",
		"bash: line 1: a: expression recursion level exceeded (error token is \"a\")\nexit status 1",
	},
	{
		"let x=3; let 3/0; ((3/0)); echo $((x/y)); let x/=0",
		"division by zero\ndivision by zero\ndivision by zero\ndivision by zero\nexit status 1 #JUSTERR",
	},
	{
		"let x=3; let 3%0; ((3%0)); echo $((x%y)); let x%=0",
		"division by zero\ndivision by zero\ndivision by zero\ndivision by zero\nexit status 1 #JUSTERR",
	},
	{
		"let x=' 3'; echo $x",
		"3\n",
	},
	{
		"x=' 3'; let x++; echo \"$x\"",
		"4\n",
	},
	// let with multiple expressions: each evaluates left to right and
	// later ones can reference earlier ones. Exit code is from the last.
	{"let a=10 b=20 c=a+b; echo \"$a $b $c\"", "10 20 30\n"},
	{"let 1 0; echo $?", "1\n"}, // last expr (0) → exit 1
	{"let 0 1; echo $?", "0\n"}, // last expr (1) → exit 0

	// select loop: prints menu to stderr, reads reply from stdin
	// (which we feed via heredoc), sets var to items[N-1], REPLY to
	// the raw input, then runs the body. Empty/invalid input is
	// handled separately below.
	{
		"PS3='> '; select x in a b c; do echo \"x=$x R=$REPLY\"; break; done <<<2",
		"1) a\n2) b\n3) c\n> x=b R=2\n",
	},
	// Invalid reply (out-of-range number) sets var to empty, REPLY
	// to the raw input, body runs.
	{
		"PS3='> '; select x in a b c; do echo \"x=$x R=$REPLY\"; break; done <<<99",
		"1) a\n2) b\n3) c\n> x= R=99\n",
	},
	// EOF on stdin exits the loop with status 1.
	{
		"PS3='> '; select x in a b c; do echo body; done </dev/null; echo end=$?",
		"1) a\n2) b\n3) c\n> \nend=1\n",
	},

	// set/shift
	{
		"echo $#; set foo bar; echo $#",
		"0\n2\n",
	},
	{
		"shift; set a b c; shift; echo $@",
		"b c\n",
	},
	{
		"shift; echo after: $?",
		"after: 1\n",
	},
	{
		"shift 2; set a b c; shift 2; echo $@",
		"c\n",
	},
	{
		`echo $#; set '' ""; echo $#`,
		"0\n2\n",
	},
	{
		"set -- a b; echo $#",
		"2\n",
	},
	{
		"set -U",
		"set: -U: invalid option\nset: usage: set [-abefhkmnptuvxBCEHPT] [-o option-name] [--] [-] [arg ...]\nexit status 2 #JUSTERR",
	},
	{
		"set -o trackall",
		"set: trackall: invalid option name\nexit status 2 #JUSTERR",
	},
	{
		"set -e; false; echo foo",
		"exit status 1",
	},
	{
		"set -e; shouldnotexist; echo foo",
		"\"shouldnotexist\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	{
		"set -e; set +e; false; echo foo",
		"foo\n",
	},
	{
		"set -e; ! false; echo foo",
		"foo\n",
	},
	{
		"set -e; ! true; echo foo",
		"foo\n",
	},
	{
		"set -e; if false; then echo never; fi; echo foo",
		"foo\n",
	},
	{
		"set -e; while false; do echo never; done; echo foo",
		"foo\n",
	},
	{
		"set -e; false || true; echo foo",
		"foo\n",
	},
	{
		"set -e; false && true; echo foo",
		"foo\n",
	},
	{
		"set -e; true && false; echo foo",
		"exit status 1",
	},
	{
		"false | :",
		"",
	},
	{
		// Important that we don't print in these, as otherwise we get "broken pipe" errors.
		"GOSH_CMD=exit_5 $GOSH_PROG | GOSH_CMD=exit_0 $GOSH_PROG",
		"",
	},
	{
		"set -o pipefail; false | :",
		"exit status 1",
	},
	{
		"set -o pipefail; GOSH_CMD=exit_5 $GOSH_PROG | GOSH_CMD=exit_0 $GOSH_PROG",
		"exit status 5",
	},
	{
		"set -o pipefail; true | false | true | :",
		"exit status 1",
	},
	{
		"set -o pipefail; set -M 2>/dev/null | false",
		"exit status 1",
	},
	{
		"set -o pipefail; false | :; echo next",
		"next\n",
	},
	{
		"set -o pipefail; exit 0 | :; echo next",
		"next\n",
	},
	{
		"set -o pipefail; exit 1 | :; echo next",
		"next\n",
	},
	{
		"set -e -o pipefail; false | :; echo next",
		"exit status 1",
	},
	{
		"exit 0 && true; echo foo",
		"",
	},
	{
		"exit 1 && true; echo foo",
		"exit status 1",
	},
	{
		"set -f; >a.x; echo *.x;",
		"*.x\n",
	},
	{
		"set -f; set +f; >a.x; echo *.x;",
		"a.x\n",
	},
	{
		"set -a; foo=bar; $ENV_PROG | grep ^foo=",
		"foo=bar\n",
	},
	{
		"set -a; foo=(b a r); $ENV_PROG | grep ^foo=",
		"exit status 1",
	},
	{
		"foo=bar; set -a; $ENV_PROG | grep ^foo=",
		"exit status 1",
	},
	{
		"a=b; echo $a; set -u; echo $a",
		"b\nb\n",
	},
	{
		"echo $a; set -u; echo $a; echo extra",
		"\na: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"foo=bar; set -u; echo ${foo/bar/}",
		"\n",
	},
	{
		"foo=bar; set -u; echo ${foo#bar}",
		"\n",
	},
	{
		"set -u; echo ${foo/bar/}",
		"foo: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"set -u; echo ${foo#bar}",
		"foo: unbound variable\nexit status 1 #JUSTERR",
	},
	// TODO: detect this case as unset
	// {
	// 	"set -u; foo=(bar); echo $foo; echo ${foo[3]}",
	// 	"bar\nfoo: unbound variable\nexit status 1 #JUSTERR",
	// },
	{
		"set -u; foo=(bar); echo ${foo[3]}",
		"foo[3]: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"set -u; echo ${narray[4]}",
		"narray[4]: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"set -u; echo ${narray[bar]}",
		"bar: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"set -u; foo=(''); echo ${foo[0]}",
		"\n",
	},
	{
		"set -u; echo ${#foo}",
		"foo: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"set -u; echo ${foo+bar}",
		"\n",
	},
	{
		"set -u; echo ${foo:+bar}",
		"\n",
	},
	{
		"set -u; echo ${foo-bar}",
		"bar\n",
	},
	{
		"set -u; echo ${foo:-bar}",
		"bar\n",
	},
	{
		"set -u; echo ${foo=bar}",
		"bar\n",
	},
	{
		"set -u; echo ${foo:=bar}",
		"bar\n",
	},
	{
		"set -u; echo ${foo?bar}",
		"foo: bar\nexit status 1 #JUSTERR",
	},
	{
		"set -u; echo ${foo:?bar}",
		"foo: bar\nexit status 1 #JUSTERR",
	},
	{
		"set -ue; set -ueo pipefail",
		"",
	},
	{"set -n; echo foo", ""},
	{"set -n; [ wrong", ""},
	// One-way: the `set +n` is itself never executed. Verified against
	// bash 5.3, which prints nothing and exits 0.
	{"set -n; set +n; echo foo", ""},
	{
		"set -o foobar",
		"set: foobar: invalid option name\nexit status 2 #JUSTERR",
	},
	{"set -o noexec; echo foo", ""},
	{"set +o noexec; echo foo", "foo\n"},
	{"set -e; set -o | grep -E 'errexit|noexec' | wc -l | tr -d ' '", "2\n"},
	{"set -e; set -o | grep -E 'errexit|noexec' | grep 'on$' | wc -l | tr -d ' '", "1\n"},
	{
		// `set -a; set +o`: confirm the explicitly-set allexport
		// is reflected as `set -o allexport`. The no-op aliases
		// (history, monitor, …) interleave but we just look for
		// the line we care about.
		"set -a; set +o | grep '^set -o allexport$'",
		"set -o allexport\n",
	},
	{`set - foobar; echo $@; set -; echo $@`, "foobar\nfoobar\n"},

	// GNU Bash 5.3 rejects single-letter names following -o/+o.
	// Short flags remain valid as set -a and set +f.
	{`set -o allexport; set +o a; [[ -o allexport ]]`, "bash: line 1: set: a: invalid option name\n"},
	{`set -o allexport; set +o a; VAR=val; echo $VAR`, "bash: line 1: set: a: invalid option name\nval\n"},
	{`set -o noglob; set +o f; [[ -o noglob ]]`, "bash: line 1: set: f: invalid option name\n"},
	{`set -o noglob; set +o f; echo foo*`, "bash: line 1: set: f: invalid option name\nfoo*\n"},
	// noexec is the exception to the +o pattern above: once it is on
	// nothing else is executed, so the `set +o n` meant to turn it back
	// off never runs. Bash 5.3 prints nothing and exits 0 for both.
	{`set -o noexec; set +o n; [[ -o noexec ]]`, ""},
	{`set -o noexec; set +o n; echo executed`, ""},
	{`set -o nounset; set +o u; [[ -o nounset ]]`, "bash: line 1: set: u: invalid option name\n"},
	{`set -o xtrace; set +o x; [[ -o xtrace ]]`, "+ set +o x\nbash: line 1: set: x: invalid option name\n+ [[ -o xtrace ]]\n"},
	{`set -o errexit; set +o e; [[ -o errexit ]]`, "bash: line 1: set: e: invalid option name\nexit status 2"},

	// A short name following -o is invalid.
	{`set -o a; [[ -o allexport ]]`, "bash: line 1: set: a: invalid option name\nexit status 1"},
	{`set -o f; [[ -o noglob ]]`, "bash: line 1: set: f: invalid option name\nexit status 1"},
	{`set -o n; [[ -o noexec ]]`, "bash: line 1: set: n: invalid option name\nexit status 1"},
	{`set -o u; [[ -o nounset ]]`, "bash: line 1: set: u: invalid option name\nexit status 1"},
	{`set -o e; [[ -o errexit ]]`, "bash: line 1: set: e: invalid option name\nexit status 1"},
	{`set -o C; [[ -o noclobber ]]`, "bash: line 1: set: C: invalid option name\nexit status 1"},

	// Both invalid names report errors.
	{`set -o C; set +o C; [[ -o noclobber ]]`, "bash: line 1: set: C: invalid option name\nbash: line 1: set: C: invalid option name\nexit status 1"},

	// set +/-flag (short flag form) for accept-and-ignore options.
	{`set -b; [[ $- = *b* ]]`, ""},
	{`set +b; [[ $- = *b* ]]`, "exit status 1"},
	{`set -v; [[ $- = *v* ]]`, ""},
	{`set +v; [[ $- = *v* ]]`, "exit status 1"},
	// hashall (h) defaults on and tracks set -h/+h in $-.
	{`[[ $- = *h* ]]`, ""},
	{`set +h; [[ $- = *h* ]]`, "exit status 1"},
	{`set +h; set -h; [[ $- = *h* ]]`, ""},

	// set +/-flag for core POSIX options (short flag form).
	{`set -C; [[ -o noclobber ]]`, ""},
	{`set +C; [[ -o noclobber ]]`, "exit status 1"},

	// set -o <long_name> for accept-and-ignore options.
	{`set -o notify; [[ $- = *b* ]]`, ""},
	{`set +o notify; [[ $- = *b* ]]`, "exit status 1"},
	{`set -o verbose; [[ $- = *v* ]]`, ""},
	{`set +o verbose; [[ $- = *v* ]]`, "exit status 1"},
	{`set -o b; [[ $- = *b* ]]`, "bash: line 1: set: b: invalid option name\nexit status 1"},
	{`set -o b; set +o b; [[ $- = *b* ]]`, "bash: line 1: set: b: invalid option name\nbash: line 1: set: b: invalid option name\nexit status 1"},

	// unset
	{
		"a=1; echo $a; unset a; echo $a",
		"1\n\n",
	},
	{
		"notinpath() { echo func; }; notinpath; unset -f notinpath; notinpath",
		"func\n\"notinpath\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	{
		"a=1; a() { echo func; }; unset -f a; echo $a",
		"1\n",
	},
	{
		"a=1; a() { echo func; }; unset -v a; a; echo $a",
		"func\n\n",
	},
	{
		"unset -f -v SHELL",
		"unset: cannot simultaneously unset a function and a variable\nexit status 1 #JUSTERR",
	},
	{
		"unset '1bad' '2bad'",
		"",
	},
	{
		"set -o posix; unset 1bad; echo after",
		"after\n",
	},
	{
		"set -o posix; unset -v 1bad; echo after",
		"unset: `1bad': not a valid identifier\nexit status 2 #JUSTERR",
	},
	{
		"set -o posix; export 1bad; echo after",
		"export: invalid name \"1bad\"\nexit status 1 #JUSTERR",
	},
	{
		"notinpath=1; notinpath() { echo func; }; notinpath; echo $notinpath; unset notinpath; notinpath; echo $notinpath; unset notinpath; notinpath",
		"func\n1\nfunc\n\n\"notinpath\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	{
		"unset PATH; [[ $PATH == '' ]]",
		"",
	},
	{
		"readonly a=1; echo $a; unset a; echo $a",
		"1\nunset: a: cannot unset: readonly variable\n1\n #IGNORE bash prints a warning",
	},
	{
		"s=abc; unset 's[1]'; echo status:$?; echo $s",
		"unset: s: not an array variable\nstatus:1\nabc\n #JUSTERR",
	},
	{
		"s=abc; unset 's[0]'; echo status:$?; echo ${s-unset}",
		"status:0\nunset\n",
	},
	{
		"f() { local a=1; echo $a; unset a; echo $a; }; f",
		"1\n\n",
	},
	{
		`a=b eval 'echo $a; unset a; echo $a'`,
		"b\n\n",
	},
	{
		`$(unset INTERP_GLOBAL); echo $INTERP_GLOBAL; unset INTERP_GLOBAL; echo $INTERP_GLOBAL`,
		"value\n\n",
	},
	{
		`x=orig; f() { local x=local; unset x; x=still_local; }; f; echo $x`,
		"orig\n",
	},
	{
		`x=orig; f() { local x=local; unset x; [[ -v x ]] && echo set || echo unset; }; f`,
		"unset\n",
	},
	{
		`PS3="pick one: "; select opt in foo bar baz; do echo "Selected $opt"; break; done <<< 3`,
		"1) foo\n2) bar\n3) baz\npick one: Selected baz\n",
	},
	{
		`(set -o posix; for invalid-name in a; do echo body; done; echo after); echo outer:$?`,
		"bash: line 1: `invalid-name': not a valid identifier\nouter:2\n",
	},
	{
		`bad_select() { select $1 in a b c; do echo $REPLY; done; }; bad_select "a b"; echo status:$?`,
		"bash: line 1: `$1': not a valid identifier\nstatus:1\n",
	},
	{
		`opts=(foo bar baz); select opt in ${opts[@]}; do echo "Selected $opt"; break; done <<< 99`,
		"1) foo\n2) bar\n3) baz\n#? Selected \n",
	},
	{
		`select opt in foo; do
	case $opt in
	foo) echo "option 1"; break;;
	*) echo "invalid option $REPLY"; break;;
	esac
done <<< 2`,
		"1) foo\n#? invalid option 2\n",
	},

	// shopt
	{"set -e; shopt -o | grep -E '^(errexit|noexec)' | wc -l | tr -d ' '", "2\n"},
	{"set -e; shopt -o | grep -E '^(errexit|noexec)' | grep 'on$' | wc -l | tr -d ' '", "1\n"},
	{"set -e; shopt | grep -E '^(errexit|noexec)' | wc -l | tr -d ' '", "0\n"},
	{"shopt -s -o noexec; echo foo", ""},
	{"shopt -so noexec; echo foo", ""},
	{"shopt -u -o noexec; echo foo", "foo\n"},
	{
		"shopt -s -u checkhash",
		"shopt: cannot set and unset shell options simultaneously\nexit status 1 #JUSTERR",
	},
	{"shopt -u globstar; shopt globstar | grep 'off$' | wc -l | tr -d ' '", "1\n"},
	{"shopt -s globstar; shopt globstar | grep 'off$' | wc -l | tr -d ' '", "0\n"},
	{"shopt extglob | grep 'off' | wc -l | tr -d ' '", "1\n"},
	{
		"shopt inherit_errexit",
		"inherit_errexit     \toff\nexit status 1",
	},
	{
		"shopt -- extglob",
		"extglob             \toff\nexit status 1",
	},
	{
		"shopt -o -s pipefail; shopt -o pipefail | grep -q 'on$'",
		"",
	},
	{
		"shopt -o -u pipefail; shopt -o pipefail | grep -q 'on$'",
		"exit status 1",
	},
	{
		"shopt pipefail",
		"shopt: pipefail: invalid shell option name\nexit status 1 #JUSTERR",
	},
	{
		"shopt -s pipefail",
		"shopt: pipefail: invalid shell option name\nexit status 1 #JUSTERR",
	},
	{
		"shopt -o -s extglob",
		"shopt: extglob: invalid option name\nexit status 1 #JUSTERR",
	},
	{
		"shopt -s login_shell",
		"",
	},
	{
		"shopt -s interactive_comments",
		"",
	},
	{
		"shopt -s nosuchname",
		"shopt: nosuchname: invalid shell option name\nexit status 1 #JUSTERR",
	},
	{
		"shopt -o -s nosuchname",
		"shopt: nosuchname: invalid option name\nexit status 1 #JUSTERR",
	},
	{
		"touch a .b ..c; shopt -u dotglob; echo *",
		"a\n",
	},
	{
		"touch a .b ..c; shopt -s dotglob; echo *",
		"..c .b a\n",
	},
	{
		"mkdir sub .sub2; touch {sub,.sub2}/{a,.b}; shopt -s globstar; shopt -u dotglob; echo **/* | sed 's@\\\\@/@g'",
		"sub sub/a\n",
	},
	{
		"mkdir sub .sub2; touch {sub,.sub2}/{a,.b}; shopt -s globstar; shopt -s dotglob; echo **/* | sed 's@\\\\@/@g'",
		".sub2 .sub2/.b .sub2/a sub sub/.b sub/a\n",
	},
	{
		// Beware that macOS file systems are by default case-preserving but
		// case-insensitive, so e.g. "touch x X" creates only one file.
		"touch a ab Ac Ad; shopt -u nocaseglob; echo a*",
		"a ab\n",
	},
	{
		"touch a ab Ac Ad; shopt -s nocaseglob; echo a*",
		"Ac Ad a ab\n",
	},
	{
		"touch a ab abB Ac Ad; shopt -u nocaseglob; echo *b",
		"ab\n",
	},
	{
		"touch a ab abB Ac Ad; shopt -s nocaseglob; echo *b",
		"ab abB\n",
	},
	{
		// `shopt -p` lists every shopt in reusable form.
		// We don't pin the full output; just check exit 0.
		"shopt -p > /dev/null && echo ok",
		"ok\n",
	},
	{
		// `shopt -q NAME` returns 0 if set, 1 if not, no output.
		"shopt -q extglob; echo $?; shopt -s extglob; shopt -q extglob; echo $?",
		"1\n0\n",
	},

	// IFS
	{`echo -n "$IFS"`, " \t\n"},
	{`a="x:y:z"; IFS=:; echo $a`, "x y z\n"},
	{`a=(x y z); IFS=-; echo ${a[*]}`, "x y z\n"},
	{`a=(x y z); IFS=-; echo ${a[@]}`, "x y z\n"},
	{`a=(x y z); IFS=-; echo "${a[*]}"`, "x-y-z\n"},
	{`a=(x y z); IFS=-; echo "${a[@]}"`, "x y z\n"},
	{`a="  x y z"; IFS=; echo $a`, "  x y z\n"},
	{`a=(x y z); IFS=; echo "${a[*]}"`, "xyz\n"},
	{`a=(x y z); IFS=-; echo "${!a[@]}"`, "0 1 2\n"},
	{`set -- x y z; IFS=-; echo $*`, "x y z\n"},
	{`set -- x y z; IFS=-; echo "$*"`, "x-y-z\n"},
	{`set -- a b; IFS=é; echo "$*"`, "aéb\n"},
	{`set -- "x y" z; unset IFS; printf '<%s>\n' $@`, "<x>\n<y>\n<z>\n"},
	{`set -- "x y" z; IFS=; printf '<%s>\n' $@`, "<x y>\n<z>\n"},
	{`set -- x y z; IFS=; echo $*`, "x y z\n"},
	{`set -- x y z; IFS=; echo "$*"`, "xyz\n"},
	{`set -- x y z; IFS=; unset v; printf '<%s>\n' ${v-$@}`, "<x>\n<y>\n<z>\n"},
	{`set -- x y z; IFS=; unset v; printf '<%s>\n' ${v-"$@"}`, "<x>\n<y>\n<z>\n"},
	{`set -- x y z; IFS=; unset v; printf '<%s>\n' ${v-$*}`, "<x>\n<y>\n<z>\n"},
	{`set -- x y z; IFS=; unset v; printf '<%s>\n' ${v-"$*"}`, "<xyz>\n"},
	{`set -- x y; unset v; printf '<%s>\n' ${v="$*"}; printf '[%s]\n' "$v"`, "<x>\n<y>\n[x y]\n"},
	{`set -o posix; set -- " abc" "def ghi" "jkl "; IFS=; unset v; printf '<%s>\n' ${v-$@}`, "< abc>\n<def ghi>\n<jkl >\n"},
	{`set -o posix; set -- " abc" "def ghi" "jkl "; IFS=:; unset v; printf '<%s>\n' ${v-$@}`, "< abc def ghi jkl >\n"},
	{`set -o posix; set -- " abc" "def ghi" "jkl "; unset IFS v; printf '<%s>\n' ${v-$@}`, "<abc>\n<def>\n<ghi>\n<jkl>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS u; printf '<%s>\n' ${u:-$@}`, "<1>\n<t>\n<w>\n<o>\n<3>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS u; printf '<%s>\n' ${u-$@}`, "<1>\n<t>\n<w>\n<o>\n<3>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS; s=x; printf '<%s>\n' ${s+$@}`, "<1>\n<t>\n<w>\n<o>\n<3>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS; s=x; printf '<%s>\n' ${s:+$@}`, "<1>\n<t>\n<w>\n<o>\n<3>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS u; printf '<%s>\n' ${u:-begin$@end}`, "<begin1>\n<t>\n<w>\n<o>\n<3end>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS u; printf '<%s>\n' ${u:-"$@"}`, "<1>\n<t w  o>\n<3>\n"},
	{`set -o posix; set -- 1 "t w  o" 3; unset IFS u; printf '<%s>\n' ${u:-"x$*y"}`, "<x1 t w  o 3y>\n"},
	{`set -o posix; set -- 1 "t w  o" 3 4 5 6 7 8 9 "$(printf 't\t e\n     n')"; unset IFS u; f() { printf '%s\n' "$#"; }; f ${u:-$@}`, "14\n"},

	// builtin
	{"builtin", ""},
	{"builtin noexist", "builtin: noexist: not a shell builtin\nexit status 1 #JUSTERR"},
	{
		"builtin -x",
		"builtin: -x: invalid option\nbuiltin: usage: builtin [shell-builtin [arg ...]]\nexit status 2 #JUSTERR",
	},
	{"builtin echo foo", "foo\n"},
	{
		"echo() { printf 'bar\n'; }; echo foo; builtin echo foo",
		"bar\nfoo\n",
	},

	// type
	{"type", ""},
	{"type for", "for is a shell keyword\n"},
	{"type echo", "echo is a shell builtin\n"},
	{"echo() { :; }; type echo | grep 'is a function'", "echo is a function\n"},
	{"type $PATH_PROG | grep -q -E ' is (/|[A-Z]:)'", ""},
	{"type noexist", "type: noexist: not found\nexit status 1 #JUSTERR"},
	{"PATH=/; type $PATH_PROG", "type: " + pathProg + ": not found\nexit status 1 #JUSTERR"},
	{"shopt -s expand_aliases; alias interp_foo='bar baz'\ntype interp_foo", "interp_foo is aliased to `bar baz'\n"},
	{"alias interp_foo='bar baz'\ntype interp_foo", "type: interp_foo: not found\nexit status 1 #JUSTERR"},
	{"type -p $PATH_PROG | grep -q -E '^(/|[A-Z]:)'", ""},
	{"PATH=/; type -p $PATH_PROG", "exit status 1"},
	// TODO: type -P should force PATH lookup even for builtins, unlike type -p.
	{"type -P $PATH_PROG | grep -q -E '^(/|[A-Z]:)'", ""},
	{"PATH=/; type -P $PATH_PROG", "exit status 1"},
	{"shopt -s expand_aliases; alias interp_foo='bar'; type -t interp_foo", "alias\n"},
	{"type -t case", "keyword\n"},
	{"interp_foo(){ :; }; type -t interp_foo", "function\n"},
	{"type -t type", "builtin\n"},
	{"type -t $PATH_PROG", "file\n"},
	{"type -t inexisting_dfgsdgfds", "exit status 1"},

	// type -a: show all matches in priority order. echo is both a
	// builtin and a file in PATH (via the test exec handler).
	{"type -a -t echo", "builtin\nfile\n"},
	{"interp_myfn(){ :; }; type -a -t interp_myfn", "function\n"},
	{"interp_myfn(){ :; }; type -a interp_myfn echo | head -2", "interp_myfn is a function\ninterp_myfn () \n"},
	{"interp_myfn(){ echo foo |& cat; }; type interp_myfn", "interp_myfn is a function\ninterp_myfn () \n{ \n    echo foo 2>&1 | cat\n}\n"},
	{`set -o posix
swap32_posix()
{
	local arg
	for arg in "$@"; do
		echo $((
			($arg & 4278190080) >> 24 |
			($arg & 16711680) >> 8 |
			($arg & 65280) << 8 |
			($arg & 255) << 24
		))
	done
}
type swap32_posix`, "swap32_posix is a function\nswap32_posix () \n{ \n    local arg;\n    for arg in \"$@\";\n    do\n        echo $((\n\t\t\t($arg & 4278190080) >> 24 |\n\t\t\t($arg & 16711680) >> 8 |\n\t\t\t($arg & 65280) << 8 |\n\t\t\t($arg & 255) << 24\n\t\t));\n    done\n}\n"},

	// type -f: skip function lookup, fall through to builtin/file.
	{"echo(){ :; }; type -t echo", "function\n"},
	{"echo(){ :; }; type -t -f echo", "builtin\n"},

	// command -V: verbose form, mirrors `type` default output.
	{"command -V echo", "echo is a shell builtin\n"},
	{"command -V noexist", "command: noexist: not found\nexit status 1 #JUSTERR"},

	// hash
	{"hash $PATH_PROG", ""},
	{"set +o hashall; hash -p /bin/sh sh", "hash: hashing disabled\nexit status 1 #JUSTERR"},
	{"hash -v", "hash: -v: invalid option\nhash: usage: hash [-lr] [-p pathname] [-dt] [name ...]\nexit status 2 #JUSTERR"},
	{"hash -d", "hash: -d: option requires an argument\nexit status 1 #JUSTERR"},

	// trap
	{"trap 'echo at_exit' EXIT; true", "at_exit\n"},
	{"trap 'echo on_err' ERR; false; echo FAIL", "on_err\nFAIL\n"},
	{"trap 'echo on_err' ERR; false || true; echo OK", "OK\n"},
	{"trap 'echo at_exit' EXIT; trap - EXIT; echo OK", "OK\n"},
	{"set -e; trap 'echo A' ERR EXIT; false; echo FAIL", "A\nA\nexit status 1"},
	{"trap 'foobar' UNKNOWN", "trap: UNKNOWN: invalid signal specification\nexit status 2 #JUSTERR"},
	{"trap -p NOSIG", "trap: NOSIG: invalid signal specification\nexit status 1 #JUSTERR"},
	{"set -o posix; trap 'echo x' INT; trap -p INT", "trap -- 'echo x' INT\n"},
	{
		"trap -s",
		"trap: -s: invalid option\ntrap: usage: trap [-Plp] [[action] signal_spec ...]\nexit status 2 #JUSTERR",
	},
	{"set -T; f(){\n:\n}\ntrap 'echo return:$LINENO' RETURN; f", "return:1\n"},
	{"f(){ trap 'echo return:$LINENO' RETURN; }; f", "return:1\n"},
	{"trap 'echo OUT_RETURN' RETURN; f(){ :; }; f", ""},
	{"trap 'echo OUT_RETURN' RETURN; f(){ trap - RETURN; }; f; trap -p RETURN", "trap -- 'echo OUT_RETURN' RETURN\n"},
	{"f(){ caller 0; }; f", "exit status 1"},
	{"g(){ caller 0; caller 1; }; f(){ g; }; f", "1 f bash\nexit status 1"},
	// TODO: our builtin appears to not receive the piped bytes?
	// {"trap 'echo on_err' ERR; trap | grep -q '.*echo on_err.*'", "trap -- \"echo on_err\" ERR\n"},
	{"trap 'false' ERR EXIT; false", "exit status 1"},

	// eval
	{"eval", ""},
	{"eval ''", ""},
	{
		"eval -i 'echo foo'",
		"eval: -i: invalid option\neval: usage: eval [arg ...]\nexit status 2 #JUSTERR",
	},
	{"eval -- echo foo", "foo\n"},
	{"eval echo foo", "foo\n"},
	{"eval 'echo foo'", "foo\n"},
	{"eval 'exit 1'", "exit status 1"},
	{"eval '(x'", "eval: 1:1: unexpected EOF while looking for matching `)'\nexit status 1 #JUSTERR"},
	{"set a b; eval 'echo $@'", "a b\n"},
	{"eval 'a=foo'; echo $a", "foo\n"},
	{`a=b eval "echo $a"`, "\n"},
	{`a=b eval 'echo $a'`, "b\n"},
	{`eval 'echo "\$a"'`, "$a\n"},
	{`a=b eval 'x=y eval "echo \$a \$x"'`, "b y\n"},
	{`a=b eval 'a=y eval "echo $a \$a"'`, "b y\n"},
	{"a=b eval '(echo $a)'", "b\n"},

	// source
	{
		"source",
		"source: filename argument required\nsource: usage: source [-p path] filename [arguments]\nexit status 2 #JUSTERR",
	},
	{
		"echo 'echo foo' >a; source ./a; . ./a",
		"foo\nfoo\n",
	},
	{
		"echo 'echo $@' >a; source ./a; source ./a b c; echo $@",
		"\nb c\n\n",
	},
	{
		"echo 'foo=bar' >a; source ./a; echo $foo",
		"bar\n",
	},
	{
		"echo 'echo ${BASH_SOURCE[0]}; f(){ echo ${FUNCNAME[1]}:${BASH_SOURCE[1]}; }; f' >a; source ./a",
		"./a\nsource:./a\n",
	},
	{
		"echo 'f(){ printf \"%s|%s|%s\\n\" \"${FUNCNAME[0]}\" \"${BASH_SOURCE[1]}\" \"${BASH_LINENO[0]}\"; }; f' >a; source ./a",
		"f|./a|1\n",
	},
	{
		`f1() { f2 y 2; }; f2() { f3 z 3; }; f3() { printf '<%s>\n' "${BASH_ARGV[@]}"; printf 'argc:%s\n' "${BASH_ARGC[@]}"; }; f1 x 1`,
		"<>\nargc:\n",
	},

	// source from PATH
	{
		"mkdir test; echo 'echo foo' >test/a; PATH=$PWD/test source a; . test/a",
		"foo\nfoo\n",
	},

	// source with set and shift
	{
		"echo 'set -- d e f' >a; source ./a; echo $@",
		"d e f\n",
	},
	{
		"echo 'echo $@' >a; set -- b c; source ./a; echo $@",
		"b c\nb c\n",
	},
	{
		"echo 'echo $@' >a; set -- b c; source ./a d e; echo $@",
		"d e\nb c\n",
	},
	{
		"echo 'shift; echo $@' >a; set -- b c; source ./a d e; echo $@",
		"e\nb c\n",
	},
	{
		"echo 'shift' >a; set -- b c; source ./a; echo $@",
		"c\n",
	},
	{
		"echo 'shift; set -- $@' >a; set -- b c; source ./a d e; echo $@",
		"e\n",
	},
	{
		"echo 'set -- g f'>b; echo 'set -- d e f; echo $@; source ./b;' >a; source ./a; echo $@",
		"d e f\ng f\n",
	},
	{
		"echo 'set -- g f'>b; echo 'echo $@; set -- d e f; source ./b;' >a; source ./a b c; echo $@",
		"b c\ng f\n",
	},
	{
		"echo 'shift; echo $@' >b; echo 'shift; echo $@; source ./b' >a; source ./a b c d; echo $@",
		"c d\nd\n\n",
	},
	{
		"echo 'set -- b c d' >b; echo 'source ./b' >a; set -- a; source ./a; echo $@",
		"b c d\n",
	},
	{
		"echo 'echo $@' >b; echo 'set -- b c d; source ./b' >a; set -- a; source ./a; echo $@",
		"b c d\nb c d\n",
	},
	{
		"echo 'shift; echo $@' >b; echo 'shift; echo $@; source ./b c d' >a; set -- a b; source ./a; echo $@",
		"b\nd\nb\n",
	},
	{
		"echo 'set -- a b c' >b; echo 'echo $@; source ./b; echo $@' >a; source ./a; echo $@",
		"\na b c\na b c\n",
	},

	// indexed arrays
	{
		"a=foo; echo ${a[0]} ${a[@]} ${a[x]}; echo ${a[1]}",
		"foo foo foo\n\n",
	},
	{
		"a=(); echo ${a[0]} ${a[@]} ${a[x]} ${a[1]}",
		"\n",
	},
	{
		"a=(b c); echo $a; echo ${a[0]}; echo ${a[1]}; echo ${a[x]}",
		"b\nb\nc\nb\n",
	},
	{
		"a=(b c); echo ${a[@]}; echo ${a[*]}",
		"b c\nb c\n",
	},
	{
		"a=(1 2 3); echo ${a[2-1]}; echo $((a[1+1]))",
		"2\n3\n",
	},
	{
		"a=(1 2) x=(); a+=b x+=c; echo ${a[@]}; echo ${x[@]}",
		"1b 2\nc\n",
	},
	{
		"a=(1 2) x=(); a+=(b c) x+=(d e); echo ${a[@]}; echo ${x[@]}",
		"1 2 b c\nd e\n",
	},
	{
		"a=bbb; a+=(c d); echo ${a[@]}",
		"bbb c d\n",
	},
	{
		`a=('a  1' 'b  2'); for e in ${a[@]}; do echo "$e"; done`,
		"a\n1\nb\n2\n",
	},
	{
		`a=('a  1' 'b  2'); for e in "${a[*]}"; do echo "$e"; done`,
		"a  1 b  2\n",
	},
	{
		`a=('a  1' 'b  2'); for e in "${a[@]}"; do echo "$e"; done`,
		"a  1\nb  2\n",
	},
	{
		`declare -a a; a[0]='a  1'; a[1]='b  2'; for e in "${a[@]}"; do echo "$e"; done`,
		"a  1\nb  2\n",
	},
	{
		`a=([1]=y [0]=x); echo ${a[0]}`,
		"x\n",
	},
	{
		`a=(y); a[2]=x; echo ${a[2]}`,
		"x\n",
	},
	{
		`a="y"; a[2]=x; echo ${a[2]}`,
		"x\n",
	},
	{
		`declare -a a=(x y); echo ${a[1]}`,
		"y\n",
	},
	{
		`a=b; echo "${a[@]}"`,
		"b\n",
	},
	{
		`a=(b); echo ${a[3]}`,
		"\n",
	},
	{
		`a=(b); echo ${a[-2]}`,
		"a: bad array subscript\n\n #JUSTERR",
	},
	{
		`a=(b); echo ${#a[-2]}`,
		"[-2]: bad array subscript\n #JUSTERR",
	},
	{
		`a=(b); echo ok ${a[-2]} later`,
		"a: bad array subscript\nok later\n #JUSTERR",
	},
	{
		`a=(b); ${a[-2]} echo hi`,
		"a: bad array subscript\nhi\n #JUSTERR",
	},
	{
		`a=abcde; declare -a a; a[5]="hello world"; a[4+5/2]="test expression"; declare a["7 + 8"]="test 2"; a[7 + 8]="test 2"; declare -p a; echo "${#a[@]}"; printf '%s\n' "${!a[@]}"`,
		"declare -a a=([0]=\"abcde\" [5]=\"hello world\" [6]=\"test expression\" [15]=\"test 2\")\n4\n0\n5\n6\n15\n",
	},
	{
		`a=(x "" y); unset 'a[0]'; a[5]=z; declare -p a; echo "${#a[@]}"; printf '<%s>\n' "${a[@]}"`,
		"declare -a a=([1]=\"\" [2]=\"y\" [5]=\"z\")\n3\n<>\n<y>\n<z>\n",
	},
	{
		`a=(x y z); n=3; unset 'a[n-1]'; declare -p a; unset 'a[-1]'`,
		"declare -a a=([0]=\"x\" [1]=\"y\")\n",
	},
	{
		`a=([0]=' x ' [1]=' y '); for v in "${a[@]}"; do echo "$v"; done`,
		" x \n y \n",
	},
	{
		`a=([0]=' x ' [1]=' y '); for v in "${a[*]}"; do echo "$v"; done`,
		" x   y \n",
	},
	{
		`a=([0]=' x ' [1]=' y '); for v in "${!a[@]}"; do echo "$v"; done`,
		"0\n1\n",
	},
	{
		`a=([0]=' x ' [1]=' y '); for v in "${!a[*]}"; do echo "$v"; done`,
		"0 1\n",
	},

	// associative arrays
	{
		`a=foo; echo ${a[""]} ${a["x"]}`,
		"foo foo\n",
	},
	{
		`declare -A a=(); echo ${a[0]} ${a[@]} ${a[1]} ${a["x"]}`,
		"\n",
	},
	{
		`declare -A a; a=4; declare -p a`,
		"declare -A a=([0]=\"4\" )\n",
	},
	{
		`declare -A a; a=([one]=1 [two]=2); declare -p a`,
		"declare -A a=([two]=\"2\" [one]=\"1\" )\n",
	},
	{
		`declare -A a; a[hello world]=flip; printf '<%s>\n' "${a[hello world]}"; a=([six]=6 [foo bar]="qux qix"); printf '<%s>\n' "${a[foo bar]}"; declare -p a`,
		"<flip>\n<qux qix>\ndeclare -A a=([six]=\"6\" [\"foo bar\"]=\"qux qix\" )\n",
	},
	{
		`declare -A a=([1+2]=c); echo ${a[1+2]}`,
		"c\n",
	},
	{
		`declare -A assoc=([i]=string [i+1]=string+1); printf 'assoc[i]=%s\n' "${assoc[i]}"; printf 'assoc[i+1]=%s\n' "${assoc[i+1]}"; assoc[i+1]+=X; printf 'assoc[i+1]=%s\n' "${assoc[i+1]}"`,
		"assoc[i]=string\nassoc[i+1]=string+1\nassoc[i+1]=string+1X\n",
	},
	{
		`touch afo; declare -A a=([foo]=one [bar]=two); unset a[foo]; declare -p a; declare -A c; c[foo]=one; c[bar]=two; unset c[foo]; declare -p c; b=(zero one); unset b[0]; declare -p b`,
		"declare -A a=([bar]=\"two\" )\ndeclare -A c=([bar]=\"two\" )\ndeclare -a b=([1]=\"one\")\n",
	},
	{
		`flix=9; declare -Ai a=([zero]=1+4 [one]=3+7 [foo bar]=flix); a[foo bar]+=7; declare -p a`,
		"declare -Ai a=([one]=\"10\" [\"foo bar\"]=\"16\" [zero]=\"5\" )\n",
	},
	{
		// bash 5.3 stops processing the compound list at the first
		// non-subscripted word (arrayfunc.c: any_failed++; break),
		// so [one] is never assigned.
		`declare -A a; a=([zero]=0 four [one]=1); declare -p a`,
		"a: four: must use subscript when assigning associative array\ndeclare -A a=([zero]=\"0\" )\n #JUSTERR",
	},
	{
		`flix=9; declare -A a=([s*]=6 [foo bar]=flix); declare -p a; printf '<%s>\n' "${a[foo bar]}"; b=([1+2]=three); declare -p b`,
		"declare -A a=([\"s*\"]=\"6\" [\"foo bar\"]=\"flix\" )\n<flix>\ndeclare -a b=([3]=\"three\")\n",
	},
	{
		`declare -A a=([foo]=bar); unset empty; echo ${#a[$empty]}; echo ${#a[missing]}`,
		"[$empty]: bad array subscript\n0\n #JUSTERR",
	},
	{
		`declare -a AA; unset 'AA[-2]'`,
		"unset: [-2]: bad array subscript\nexit status 1 #JUSTERR",
	},
	{
		`declare -A a=([one]=a [*]=12 [hello world]=flip [box]="multiple words"); printf '<%s>\n' "${a[@]}"; printf '<%s>\n' "${!a[@]}"`,
		"<multiple words>\n<12>\n<flip>\n<a>\n<box>\n<*>\n<hello world>\n<one>\n",
	},
	{
		`declare -A a; a[bar\"bie]=doll; a[bar\'bie]=toy; a[bar\$bie]=cash; printf '<%s>\n' "${!a[@]}"; declare -p a`,
		"<bar'bie>\n<bar$bie>\n<bar\"bie>\ndeclare -A a=([\"bar'bie\"]=\"toy\" [\"bar\\$bie\"]=\"cash\" [\"bar\\\"bie\"]=\"doll\" )\n",
	},
	{
		`declare -A a; declare +A a; declare -a b; declare +a b`,
		"declare: a: cannot destroy array variables in this way\ndeclare: b: cannot destroy array variables in this way\nexit status 1 #JUSTERR",
	},
	{
		`declare -ar a; declare +a a`,
		"declare: a: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		`declare -A a=([0]=zero [x]=ex); echo "$a"; echo "${a:1:2}"`,
		"zero\ner\n",
	},
	{
		`declare -A a=([x]=b [y]=c); echo $a; echo ${a[0]}; echo ${a["x"]}; echo ${a["_"]}`,
		"\n\nb\n\n",
	},
	{
		`declare -Ag a=([x]=y); echo ${a["x"]}`,
		"y\n",
	},
	{
		`declare -A a=([x]=b [y]=c); for e in ${a[@]}; do echo $e; done | sort`,
		"b\nc\n",
	},
	{
		`declare -A a=([y]=b [x]=c); for e in ${a[*]}; do echo $e; done | sort`,
		"b\nc\n",
	},
	{
		`declare -A a=([x]=a); a["y"]=d; a["x"]=c; for e in ${a[@]}; do echo $e; done | sort`,
		"c\nd\n",
	},
	{
		`declare -A a=([x]=a); a[y]=d; a[x]=c; for e in ${a[@]}; do echo $e; done | sort`,
		"c\nd\n",
	},
	{
		// Without `declare -A` this is an indexed array; the quoted
		// subscripts are evaluated arithmetically (`x`/`y` are unset ->
		// 0), so both assign element 0: a[0]=b then a[0]=c.
		`a=(["x"]=b ["y"]=c); echo ${a["y"]}`,
		"c\n",
	},
	{
		`declare -A a=(['x']=b); echo ${a['x']} ${a[$'x']} ${a[$"x"]}`,
		"b b b\n",
	},
	{
		`arr=([0x003d]==); echo ${arr[61]}`,
		"=\n",
	},
	{
		`arr=([0x0020]=\  [0x0021]=\! [0x005c]=\\); printf '<%s>\n' "${arr[32]}" "${arr[33]}" "${arr[92]}"`,
		"< >\n<!>\n<\\>\n",
	},
	{`printf '<%s>\n' $'\Uffffffff'`, "<>\n"},
	{
		// Assignment coerces the quoted subscript to zero, but reading a
		// quoted name is an arithmetic operand error.
		`a=(['x']=b); echo ${a['y']}`,
		"bash: line 1: 'y': arithmetic syntax error: operand expected (error token is \"'y'\")\nexit status 1",
	},
	{
		`declare -A a=(['a  1']=' x ' ['b  2']=' y '); for v in "${a[@]}"; do echo "$v"; done | sort`,
		" x \n y \n",
	},
	{
		`declare -A a=(['a  1']=' x ' ['b  2']=' y '); for v in "${a[*]}"; do echo "$v"; done`,
		" x   y \n",
	},
	{
		`declare -A a=(['a  1']=' x ' ['b  2']=' y '); for v in "${!a[@]}"; do echo "$v"; done | sort`,
		"a  1\nb  2\n",
	},
	{
		`declare -A a=(['a  1']=' x ' ['b  2']=' y '); for v in "${!a[*]}"; do echo "$v"; done`,
		"a  1 b  2\n",
	},
	{
		`declare -A a; a[a]=x; a[b]=y; for v in "${!a[@]}"; do echo "$v"; done | sort`,
		"a\nb\n",
	},
	{
		`declare -A a; a[a]=x; a[b]=y; declare -A a; for v in "${!a[@]}"; do echo "$v"; done | sort`,
		"a\nb\n",
	},
	{
		`IFS=; a=('a b' 'c d' 'e f'); printf '<%s>\n' ${a[@]:1:2}`,
		"<c d>\n<e f>\n",
	},
	{
		`IFS=; a=(ab cd ef); foo='var with spaces'; printf '<%s>\n' ${foo}${a[@]}`,
		"<var with spacesab>\n<cd>\n<ef>\n",
	},
	{`array[1001 - $(echo -n 1001)]=1; echo ${array[0]}`, "1\n"},
	{`a=(zero one two three); bar=2; echo ${a[" bar "]}`, "two\n"},
	{`declare a='(1 2 3)'; declare -p a`, "declare -- a=\"(1 2 3)\"\n"},
	{`declare -a a; declare a='(1 2 3)'; declare -p a`, "declare -a a=([0]=\"1\" [1]=\"2\" [2]=\"3\")\n"},
	{`declare -A a=scalar; declare -p a`, "declare -A a=([0]=\"scalar\" )\n"},
	{`declare -A a; declare a='([0]=a [1]=b)'; declare -p a`, "declare -A a=([1]=\"b\" [0]=\"a\" )\n"},
	{`declare -A c='(1 2)'; declare -p c`, "declare -A c=([1]=\"2\" )\n"},
	{`c=(old); readonly 'c=(3)'; declare -p c`, "declare -ar c=([0]=\"(3)\")\n"},
	{`r=(old); export 'r=(5)'; declare -p r`, "declare -ax r=([0]=\"(5)\")\n"},
	{`echo "${dbg-'"'hey}"`, "''hey\n"},
	{`echo "${dbg-'"hey'}"`, "'hey'\n"},
	// weird assignments
	{"a=b; a=(c d); echo ${a[@]}", "c d\n"},
	{"a=(b c); a=d; echo ${a[@]}", "d c\n"},
	{"declare -A a=([x]=b [y]=c); a=d; for e in ${a[@]}; do echo $e; done | sort", "b\nc\nd\n"},
	{"i=3; a=b; a[i]=x; echo ${a[@]}", "b x\n"},
	{"i=3; declare a=(b); a[i]=x; echo ${!a[@]}", "0 3\n"},
	{"i=3; declare -A a=(['x']=b); a[i]=x; for e in ${!a[@]}; do echo $e; done | sort", "i\nx\n"},

	// declare
	{"declare -B foo", "declare: -B: invalid option\ndeclare: usage: declare [-aAfFgiIlnrtux] [name[=value] ...] or declare -p [-aAfFilnrtux] [name ...]\nexit status 2 #JUSTERR"},
	{"declare -- -z", "declare: invalid name \"-z\"\nexit status 1 #JUSTERR"},
	{"a=b; declare a; echo $a; declare a=; echo $a", "b\n\n"},
	{"a=b; declare a; echo $a", "b\n"},
	{
		"declare a=b c=(1 2); echo $a; echo ${c[@]}",
		"b\n1 2\n",
	},
	{"a=x; declare $a; echo $a $x", "x\n"},
	{"a=x=y; declare $a; echo $a $x", "x=y y\n"},
	{"a='x=(y)'; declare $a; echo $a $x", "x=(y) (y)\n"},
	{"a='x=b y=c'; declare $a; echo $x $y", "b c\n"},
	{"declare =bar", "declare: invalid name \"\"\nexit status 1 #JUSTERR"},
	{"declare $unset=$unset", "declare: invalid name \"\"\nexit status 1 #JUSTERR"},

	// export
	{"declare foo=bar; $ENV_PROG | grep '^foo='", "exit status 1"},
	{"declare -x foo=bar; $ENV_PROG | grep '^foo='", "foo=bar\n"},
	{"export foo=bar; $ENV_PROG | grep '^foo='", "foo=bar\n"},
	{"foo=bar; export foo; $ENV_PROG | grep '^foo='", "foo=bar\n"},
	{"export foo=bar; foo=baz; $ENV_PROG | grep '^foo='", "foo=baz\n"},
	{"export foo=bar; readonly foo=baz; $ENV_PROG | grep '^foo='", "foo=baz\n"},
	{"export foo=(1 2); $ENV_PROG | grep '^foo='", "exit status 1"},
	{"declare -A foo=([a]=b); export foo; $ENV_PROG | grep '^foo='", "exit status 1"},
	{"export foo=(b c); foo=x; $ENV_PROG | grep '^foo='", "exit status 1"},
	{"foo() { bar=foo; export bar; }; foo; $ENV_PROG | grep ^bar=", "bar=foo\n"},
	{"foo() { export bar; }; bar=foo; foo; $ENV_PROG | grep ^bar=", "bar=foo\n"},
	{"foo() { export bar; }; foo; bar=foo; $ENV_PROG | grep ^bar=", "bar=foo\n"},
	{"foo() { export bar=foo; }; foo; readonly bar; $ENV_PROG | grep ^bar=", "bar=foo\n"},
	{"var=10; export var; export -n var; var=60 export var; declare -p var", "declare -x var=\"60\"\n"},
	// `export -f name` of something that is not a function is an error,
	// not an invisible function (errors.tests line 120); the diagnostic
	// does not depend on the host.
	{"export -f XPATH; echo status=$?", "export: XPATH: not a function\nstatus=1\n #JUSTERR"},
	{"XPATH=x; export -f XPATH; echo status=$?", "export: XPATH: not a function\nstatus=1\n #JUSTERR"},

	// local
	{
		"local a=b",
		"local: can only be used in a function\nexit status 1 #JUSTERR",
	},
	{
		"local a=b 2>/dev/null; echo $a",
		"\n",
	},
	{
		"{ local a=b; }",
		"local: can only be used in a function\nexit status 1 #JUSTERR",
	},
	{
		"echo 'local a=b' >a; source ./a",
		"local: can only be used in a function\nexit status 1 #JUSTERR",
	},
	{
		"echo 'local a=b' >a; f() { source ./a; }; f; echo $a",
		"\n",
	},
	{
		"f() { local a=b; }; f; echo $a",
		"\n",
	},
	{
		"a=x; f() { local a=b; }; f; echo $a",
		"x\n",
	},
	{
		"a=x; f() { echo $a; local a=b; echo $a; }; f",
		"x\nb\n",
	},
	{
		"f1() { local a=b; }; f2() { f1; echo $a; }; f2",
		"\n",
	},
	{
		"f() { a=1; declare b=2; export c=3; readonly d=4; declare -g e=5; }; f; echo $a $b $c $d $e",
		"1 3 4 5\n",
	},
	{
		`w=old; f() { local w=local; declare -g w=global; echo "f:$w"; }; f; echo "G:$w"`,
		"f:local\nG:global\n",
	},
	{
		`var=global; f() { local -I var; declare -p var; }; f`,
		"declare -- var=\"global\"\n",
	},
	{
		`f() { local x; g; }; g() { local -p x; }; f`,
		"local: x: not found\nexit status 1 #JUSTERR",
	},
	{
		`IGNOREEOF=0; set -o ignoreeof; echo $IGNOREEOF; f() { local -; set +o ignoreeof; shopt -o ignoreeof; }; f; shopt -o ignoreeof; echo $IGNOREEOF`,
		"10\nignoreeof           \toff\nignoreeof           \ton\n10\n",
	},
	{
		`before=$-; f() { local -; set -u; local -p; }; f; echo "$before|$-"`,
		"local -\nhBs|hBs\n",
	},
	{
		`trap 'echo trap:$FUNCNAME' EXIT; f() { exit; }; f`,
		"trap:f\n",
	},
	{
		`f() { local x; [[ -v x ]] && echo set || echo unset; }; f`,
		"unset\n",
	},
	{
		`f() { local x=; [[ -v x ]] && echo set || echo unset; }; f`,
		"set\n",
	},
	{
		`export x=before; f() { local x; export x=after; $ENV_PROG | grep '^x='; }; f; echo $x`,
		"x=after\nbefore\n",
	},
	{
		"getx() { echo $X; }; f() { local X=Y; getx; echo $X; }; f",
		"Y\nY\n",
	},
	{
		"setx() { X=Y; }; f() { local X; setx; echo $X; }; f",
		"Y\n",
	},
	{
		"setx() { local X=Y; }; f() { local X; setx; echo $X; }; f",
		"\n",
	},
	{
		"setx() { declare X=Y; }; f() { local X; setx; echo $X; }; f",
		"\n",
	},
	{
		"setx() { X=Y :; }; f() { local X; setx; echo $X; }; f",
		"\n",
	},

	// unset global from inside function
	{"f() { unset foo; echo $foo; }; foo=bar; f", "\n"},
	{"f() { unset foo; }; foo=bar; f; echo $foo", "\n"},

	// name references
	{"declare -n foo=bar; bar=etc; [[ -R foo ]]", ""},
	{"declare -n foo=bar; bar=etc; [ -R foo ]", ""},
	{"nameref foo=bar; bar=etc; [[ -R foo ]]", " #IGNORE"},
	{`arr=(a b); f(){ local -n A=arr; printf '<%s>\n' "${!A[@]}"; }; f`, "<0>\n<1>\n"},
	{`arr=([0x03A8]=x); f(){ local -n A=${1:?}; printf '<%s>\n' "${!A[@]}"; }; f arr`, "<936>\n"},
	{"declare foo=bar; bar=etc; [[ -R foo ]]", "exit status 1"},
	{
		"declare -n foo=bar; bar=etc; echo $foo; bar=zzz; echo $foo",
		"etc\nzzz\n",
	},
	{
		"declare -n foo=bar; bar=(x y); echo ${foo[1]}; bar=(a b); echo ${foo[1]}",
		"y\nb\n",
	},
	{
		"declare -n foo=bar; bar=(1 3 5 7 9); foo[2]=42; echo ${bar[@]}",
		"1 3 42 7 9\n",
	},
	{
		"arr=(a b c); declare -n ref='arr[1]'; echo $ref; ref=X; declare -p arr; echo ${!ref}; echo ${!ref[@]}",
		"b\ndeclare -a arr=([0]=\"a\" [1]=\"X\" [2]=\"c\")\narr[1]\n\n",
	},
	{
		"arr=(a b c); declare -n ref='arr[1]'; ref+=X; unset ref; declare -p arr ref",
		"declare -a arr=([0]=\"a\" [2]=\"c\")\ndeclare -n ref=\"arr[1]\"\n",
	},
	{
		"declare -A arr=([k]=v); declare -n ref='arr[k]'; ref+=X; echo $ref; unset ref; declare -p arr ref",
		"vX\ndeclare -A arr=()\ndeclare -n ref=\"arr[k]\"\n",
	},
	{
		"bar=(1 3 5 7 9); f(){ local -n v=$1; v[2]=44; }; f bar; echo ${bar[@]}",
		"1 3 44 7 9\n",
	},
	{
		"declare -n foo=bar; bar=etc; echo $foo; unset bar; echo $foo",
		"etc\n\n",
	},
	{
		"declare -n a1=a2 a2=a3 a3=a4; a4=x; echo $a1 $a3",
		"x x\n",
	},
	{
		"declare -n foo=bar bar=foo; echo $foo",
		"warning: foo: circular name reference\n\n #IGNORE",
	},
	{
		"declare -n foo=bar; echo $foo",
		"\n",
	},
	{
		"declare -n foo=bar; echo ${!foo}",
		"bar\n",
	},
	{
		"declare -n foo=bar; bar=etc; echo $foo; echo ${!foo}",
		"etc\nbar\n",
	},
	{
		"declare -n foo=bar; bar=etc; foo=value; echo $foo; echo $bar",
		"value\nvalue\n",
	},
	{
		"declare -n foo=bar; foo=value; echo $foo; echo $bar",
		"value\nvalue\n",
	},
	{
		"declare -n foo=bar; declare foo=value; echo $foo; echo $bar",
		"value\nvalue\n",
	},
	{
		"declare -n foo=bar bar=baz; foo=value; echo $foo; echo $bar; echo $baz",
		"value\nvalue\nvalue\n",
	},
	{
		"declare -n foo=bar; set -u; echo ${foo}",
		"foo: unbound variable\nexit status 1 #JUSTERR",
	},
	{
		"declare -n foo=bar; set -u; echo ${foo:=value}; echo $foo; echo $bar",
		"value\nvalue\nvalue\n",
	},
	{
		"declare -n foo=bar; foo=value $ENV_PROG | grep '^bar='",
		"bar=value\n",
	},
	{
		"echo ${!@}-${!*}; set -- foo; echo ${!@}-${!*}-${!1}; foo=value; echo ${!@}-${!*}-${!1}",
		"-\n--\nvalue-value-value\n",
	},
	{
		"declare -n ref=arr; ref+=(x y); echo ${ref[@]} ${arr[@]}",
		"x y x y\n",
	},
	{
		"declare -n ref=foo; readonly ref; declare -p ref foo; foo=4",
		"declare -n ref=\"foo\"\ndeclare -r foo\nfoo: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"readonly foo; declare -n ref=foo; ref=4",
		"foo: readonly variable\nexit status 1 #JUSTERR",
	},

	// read-only vars
	{"declare -r foo=bar; echo $foo", "bar\n"},
	{"readonly foo=bar; echo $foo", "bar\n"},
	{
		"readonly -x foo",
		"readonly: -x: invalid option\nreadonly: usage: readonly [-aAf] [name[=value] ...] or readonly -p\nexit status 2 #JUSTERR",
	},
	{"readonly foo=bar; export foo; echo $foo", "bar\n"},
	{"readonly foo=bar; readonly bar=foo; export foo bar; echo $bar", "foo\n"},
	// Assigning to a readonly variable via the export/readonly
	// builtins fails with status 1 and keeps the old value.
	{"a='1  *  2'; command command export A=$a; printf \"%s\\n\" \"$A\"", "bash: line 1: export: `*': not a valid identifier\nbash: line 1: export: `2': not a valid identifier\n1\n"},
	{"a='1  *  2'; command command readonly A=$a; printf \"%s\\n\" \"$A\"", "bash: line 1: readonly: `*': not a valid identifier\nbash: line 1: readonly: `2': not a valid identifier\n1\n"},
	{"readonly v=a; command export v=foo; echo after: $?; echo $v", "bash: line 1: v: readonly variable\nafter: 1\na\n"},
	{"readonly v=a; command readonly v=foo; echo after: $?; echo $v", "bash: line 1: v: readonly variable\nafter: 1\na\n"},
	// POSIX mode implies shift_verbose.
	{"set -o posix; command shift 2; echo after: $?", "bash: line 1: shift: 2: shift count out of range\nafter: 1\n"},
	{"set -o posix; command shift; echo after: $?", "bash: line 1: shift: shift count out of range\nafter: 1\n"},
	{"readonly -p | grep '^declare -r SHELLOPTS='", "declare -r SHELLOPTS=\"braceexpand:hashall:interactive-comments\"\n"},
	{
		"a=b; a=c; echo $a; readonly a; a=d",
		"c\na: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"declare -r foo=bar; foo=etc",
		"foo: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"VAR=4; readonly VAR; VAR=7 :; echo $VAR",
		"bash: line 1: VAR: readonly variable\n4\n",
	},
	{
		"set -o posix; VAR=4; readonly VAR; VAR=7 :; echo after",
		"VAR: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"set -o posix; VAR=4; readonly VAR; VAR=7; echo after",
		"VAR: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"set -o posix; readonly VAR; : ${VAR:=value}; echo after",
		"VAR: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"VAR=4; readonly VAR; VAR=7 echo ok; echo status:$?",
		"bash: line 1: VAR: readonly variable\nok\nstatus:0\n",
	},
	{
		"set -o posix; VAR=4; readonly VAR; VAR=7 echo ok; echo after",
		"VAR: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"HOME=/foo/original; set -k; export HOME=/foo/bar >/dev/null; c=7; HOME=/a/b/c echo $HOME c=9; echo $c",
		"/foo/original\n7\n",
	},
	{
		"HOME=/foo/original; set -k; ECHO=; export HOME=/foo/bar >/dev/null; HOME=/a/b/c $ECHO a=$HOME c=9; echo $HOME $c $a",
		"/a/b/c 9 /a/b/c\n",
	},
	{
		"f() { declare -p var; }; var[0]=X var[@]=Y f",
		"`var[0]': not a valid identifier\n`var[@]': not a valid identifier\ndeclare: var: not found\nexit status 1 #JUSTERR",
	},
	{
		"f() { test -v 'var[0]'; echo $?; }; var[0]=X f",
		"bash: line 1: `var[0]': not a valid identifier\n1\n",
	},
	{
		"f() { local -i a; a+=3; echo $a; }; a=4 b=7 f; echo after: ${a-unset}",
		"7\nafter: unset\n",
	},
	{
		"f() { local a; echo ${a-unset}; }; a=4 f",
		"4\n",
	},
	{
		"f() { local a; echo ${a-unset}; }; export a=4; f",
		"unset\n",
	},
	{
		"set -o posix; f() { foo=abc :; typeset +x foo; $ENV_PROG | grep '^foo='; }; f; declare -p foo",
		"foo=abc\ndeclare -x foo=\"abc\"\n",
	},
	{
		"set -o posix; f() { var=value declare -x var; declare -p var; }; var=one; f; declare -p var",
		"declare -x var=\"value\"\ndeclare -- var=\"one\"\n",
	},
	{
		"g() { local -i a; a+=3; echo $a; }; f() { g; }; a=4 f",
		"7\n",
	},
	{
		"VAR=4; readonly VAR; VAR=7 notthere; echo status:$?",
		"VAR: readonly variable\n\"notthere\": executable file not found in $PATH\nstatus:127\n #IGNORE",
	},
	{
		"declare -r foo=bar; export foo=",
		"foo: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"readonly foo=bar; foo=etc",
		"foo: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"foo() { bar=foo; readonly bar; }; foo; bar=bar",
		"bar: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"foo() { readonly bar; }; foo; bar=foo",
		"bar: readonly variable\nexit status 1 #JUSTERR",
	},
	{
		"foo() { readonly bar=foo; }; foo; export bar; $ENV_PROG | grep '^bar='",
		"bar=foo\n",
	},

	// multiple var modes at once
	{
		"declare -r -x foo=bar; $ENV_PROG | grep '^foo='",
		"foo=bar\n",
	},
	{
		"declare -r -x foo=bar; foo=x",
		"foo: readonly variable\nexit status 1 #JUSTERR",
	},

	// globbing
	{"echo .", ".\n"},
	{"echo ..", "..\n"},
	{"echo ./.", "./.\n"},
	{
		">a.x >b.x >c.x; echo *.x; rm a.x b.x c.x",
		"a.x b.x c.x\n",
	},
	{
		`>a.x; echo '*.x' "*.x"; rm a.x`,
		"*.x *.x\n",
	},
	{
		`>a.x >b.x; printf '<%s>\n' \* a\*; rm a.x b.x`,
		"<*>\n<a*>\n",
	},
	{
		`>a.x >b.y; echo *'.'x; rm a.x`,
		"a.x\n",
	},
	{
		`>a.x; echo *'.x' "a."* '*'.x; rm a.x`,
		"a.x a.x *.x\n",
	},
	{
		"echo *.x; echo foo *.y bar",
		"*.x\nfoo *.y bar\n",
	},
	{
		`>a.x >b.x >c.x; a=*.x; echo $a; echo "$a"`,
		"a.x b.x c.x\n*.x\n",
	},
	{
		`>a.x >b.x >c.x; a=(*.x); echo "${a[@]}"; echo ${a[1]}`,
		"a.x b.x c.x\nb.x\n",
	},
	{
		"mkdir a; >a/b.x; echo */*.x | sed 's@\\\\@/@g'; cd a; echo *.x",
		"a/b.x\nb.x\n",
	},
	{
		"mkdir -p a/b/c; echo a/* | sed 's@\\\\@/@g'",
		"a/b\n",
	},
	{
		">.hidden >a; echo *; echo .h*; rm .hidden a",
		"a\n.hidden\n",
	},
	{
		`mkdir d; >d/.hidden >d/a; set -- "$(echo d/*)" "$(echo d/.h*)"; echo ${#1} ${#2}; rm -r d`,
		"3 9\n",
	},
	{
		"mkdir -p a/b/c; echo a/** | sed 's@\\\\@/@g'",
		"a/b\n",
	},
	{
		"shopt -s globstar; mkdir -p a/b/c; echo a/** | sed 's@\\\\@/@g'",
		"a/ a/b a/b/c\n",
	},
	{
		"shopt -s globstar; mkdir -p a/b/c; echo **/c | sed 's@\\\\@/@g'",
		"a/b/c\n",
	},
	{
		"shopt -s globstar; mkdir -p a/b; touch c; echo ** | sed 's@\\\\@/@g'",
		"a a/b c\n",
	},
	{
		"shopt -s globstar; mkdir -p a/b; touch c; echo **/ | sed 's@\\\\@/@g'",
		"a/ a/b/\n",
	},
	{
		"shopt -s globstar; mkdir -p a/b/c a/d; echo ** | sed 's@\\\\@/@g'",
		"a a/b a/b/c a/d\n",
	},
	{
		"shopt -s globstar; mkdir -p a.x a/b.x a/b/c.x; echo **.x ./**.x | sed 's@\\\\@/@g'",
		"a.x ./a.x\n",
	},
	{
		"shopt -s globstar; mkdir -p a/b; touch a/b/c; echo **/* | sed 's@\\\\@/@g'",
		"a a/b a/b/c\n",
	},
	{
		"shopt -s globstar; mkdir -p b; touch x2 a b/c d x1; echo **/* | sed 's@\\\\@/@g'",
		"a b b/c d x1 x2\n",
	},
	{
		"mkdir foo; touch foo/bar; echo */bar */bar/ | sed 's@\\\\@/@g'",
		"foo/bar */bar/\n",
	},
	{
		"shopt -s nullglob; touch existing-1; echo missing-* existing-*",
		"existing-1\n",
	},
	{
		"touch ŀfoo; echo ŀ*",
		"ŀfoo\n",
	},

	// Extended globbing via the extglob option.
	// Note how extglob affects Bash's own line-by-line parsing, so we set the option before a newline.
	{
		"shopt -s extglob\necho invalid-?([)",
		"invalid-?([)\n",
	},
	{
		"shopt -s extglob\necho +()c @()x",
		"+()c @()x\n",
	},
	{
		"touch az a1z a12z a123z; echo a?([0-9])z",
		"extended globbing operator used without the \"extglob\" option set\n #JUSTERR",
	},
	{
		"shopt -s extglob\ntouch az a1z a12z a123z; echo a?([0-9])z",
		"a1z az\n",
	},
	{
		"shopt -s extglob\ntouch az a1z a12z a123z; echo a*([0-9])z",
		"a123z a12z a1z az\n",
	},
	{
		"shopt -s extglob\ntouch az a1z a12z a123z; echo a+([0-9])z",
		"a123z a12z a1z\n",
	},
	{
		"shopt -s extglob\ntouch abd acd aed; echo a+(b|c)d",
		"abd acd\n",
	},
	{
		"shopt -s extglob\ntouch ab abcdef abef abcfef; echo ab**(e|f)",
		"ab abcdef abcfef abef\n",
	},
	{
		"shopt -s extglob\ncase '*(a|b[)' in *(a|b[)) echo yes;; *) echo no;; esac",
		"yes\n",
	},
	{
		"shopt -s extglob\ntouch a ab ba; echo a*!(x)",
		"a ab\n",
	},
	{
		"shopt -s extglob\ntouch a b c .x .y .z; echo !(f); echo !(f)!(f)",
		"a b c\na b c\n",
	},
	{
		"shopt -s extglob; GLOBIGNORE='+([^[:alnum:]]):@([-.,:; _]):[![:alnum:]]'; touch ';' '++'; echo *",
		"*\n",
	},
	{
		"shopt -s extglob; shopt -u globskipdots; touch .a .foo a.log; echo .*; echo @(.*); echo .?; echo @(.?); echo '---'; echo *(.)",
		". .. .a .foo\n. .. .a .foo\n.. .a\n.. .a\n---\n*(.)\n #IGNORE S275 decision: GNU Bash parses the whole line before shopt -s extglob runs; this parser always accepts extglob",
	},
	{
		"shopt -s extglob\ntouch az a1z a12z a123z; echo a@([0-9])z",
		"a1z\n",
	},
	{
		"mkdir -p ab/cd; touch ab/cd/efg; GLOBIGNORE='ab/cd/efg'; echo */*/efg",
		"*/*/efg\n",
	},
	{
		"enable -f ./strmatch.so strmatch; strmatch 'ab[/]ef' 'ab[/]ef'; echo $?; strmatch 'ab/ef' 'ab[/]ef'; echo $?",
		"0\n1\n",
	},
	{
		"shopt -s nullglob extglob\nprintf '<%s>\\n' @(missing) after",
		"<after>\n",
	},
	{
		"shopt -s extglob\ntouch a{1..9}0z; echo a+(0|[1-2]|8)z",
		"a10z a20z a80z\n",
	},
	{
		"shopt -s extglob\ntouch az a1z a12z a123z; echo a!([0-9])z",
		"a123z a12z az\n",
	},
	// !(pattern) extglob negation in case and [[ ]] matching
	{
		"shopt -s extglob\ncase \"bar\" in !(foo)) echo match;; esac",
		"match\n",
	},
	{
		"shopt -s extglob\ncase \"foo\" in !(foo)) echo match;; esac",
		"",
	},
	{
		"shopt -s extglob\ncase \"\" in !(foo)) echo match;; esac",
		"match\n",
	},
	{
		"shopt -s extglob\ncase \"baz\" in !(foo|bar)) echo match;; esac",
		"match\n",
	},
	{
		"shopt -s extglob\ncase \"file.tar.gz\" in !(*.sig)) echo match;; esac",
		"match\n",
	},
	{
		"shopt -s extglob\ncase \"file.sig\" in !(*.sig)) echo match;; esac",
		"",
	},
	{
		"shopt -s extglob\ncase \"foo_xxx_baz\" in foo_!(bar)_baz) echo match;; esac",
		"match\n",
	},
	{
		"shopt -s extglob\ncase \"foo_bar_baz\" in foo_!(bar)_baz) echo match;; esac",
		"",
	},
	{
		"shopt -s extglob\n[[ \"bar\" == !(foo) ]] && echo match",
		"match\n",
	},
	// Unsupported: multiple groups, glob prefix, or glob suffix.
	{
		"shopt -s extglob\ncase \"xabab\" in *a!(b)) echo match;; esac",
		" #IGNORE glob prefix not supported",
	},
	{
		"shopt -s extglob\ncase \"baz\" in !(foo)!(bar)) echo match;; esac",
		"match\n",
	},
	{
		"shopt -s extglob\ncase \".bar\" in .*!(foo)) echo match;; esac",
		" #IGNORE glob prefix not supported",
	},
	{
		"shopt -s extglob\ncase \".foo\" in .*!(foo)) echo match;; esac",
		" #IGNORE glob prefix not supported",
	},
	{
		"shopt -s extglob\ncase \"bar\" in .*!(foo)) echo match;; esac",
		" #IGNORE glob prefix not supported",
	},
	{
		// Extended pattern matching is always available outside of pathname expansions (globbing).
		"[[ a123z == a@([0-9])z ]]; echo $?; [[ a123z == a+([0-9])z ]]; echo $?",
		"1\n0\n",
	},
	// Ensure that setting nullglob does not return invalid globs as null
	// strings.
	{
		"shopt -s nullglob; [ -n butter ] && echo bubbles",
		"bubbles\n",
	},
	{
		"cat <<EOF\n{foo,bar}\nEOF",
		"{foo,bar}\n",
	},
	{
		"cat <<EOF\n*.go\nEOF",
		"*.go\n",
	},
	{
		"mkdir -p a/b a/c; echo ./a/* | sed 's@\\\\@/@g'",
		"./a/b ./a/c\n",
	},
	{
		"mkdir -p a/b a/c d; cd d; echo ../a/* | sed 's@\\\\@/@g'",
		"../a/b ../a/c\n",
	},
	{
		"mkdir x-d1 x-d2; >x-f; echo x-*/ | sed 's@\\\\@/@g'",
		"x-d1/ x-d2/\n",
	},
	{
		"mkdir x-d1 x-d2; >x-f; echo ././x-*/// | sed 's@\\\\@/@g'",
		"././x-d1/ ././x-d2/\n",
	},
	{
		"mkdir -p x-d1/a x-d2/b; >x-f; echo x-*/* | sed 's@\\\\@/@g'",
		"x-d1/a x-d2/b\n",
	},
	{
		"mkdir -p foo/bar; ln -s foo sym; echo sy*/; echo sym/b*",
		"sym/\nsym/bar\n",
	},
	{
		">foo; ln -s foo sym; echo sy*; echo sy*/",
		"sym\nsy*/\n",
	},
	{
		"mkdir x-d; >x-f; test -d $PWD/x-*/",
		"",
	},
	{
		"mkdir dir; >dir/x-f; ln -s dir sym; cd sym; test -f $PWD/x-*",
		"",
	},

	// brace expansion; there are also some tests in the expand package
	{"echo a}b", "a}b\n"},
	{"echo {a,b{c,d}", "{a,bc {a,bd\n"},
	{"echo a{b}", "a{b}\n"},
	{"echo a{à,世界}", "aà a世界\n"},
	{"echo a{b,c}d{e,f}g", "abdeg abdfg acdeg acdfg\n"},
	{"echo a{b{x,y},c}d", "abxd abyd acd\n"},
	// a leading ~ in a brace-expansion result still undergoes tilde expansion
	// (a trailing empty Lit from `{x,}` must not suppress it); empty elements
	// are otherwise preserved.
	{"HOME=/h; echo {~/a,~/b}", "/h/a /h/b\n"},
	{"echo a{b,}c", "abc ac\n"},
	{"echo a{1..", "a{1..\n"},
	{"echo a{1..2}b{4..5}c", "a1b4c a1b5c a2b4c a2b5c\n"},
	{"echo a{c..f}", "ac ad ae af\n"},
	{"echo a{4..1..1}", "a4 a3 a2 a1\n"},
	{"b=c; echo ${b}a{4..1..1}", "ca4 ca3 ca2 ca1\n"},
	{"b=c; echo a{1,2}$b", "a1c a2c\n"},
	{"echo a{1,2}'bc'", "a1bc a2bc\n"},
	{`echo a\{1,2}b`, "a{1,2}b\n"},
	{`echo a{1,2\`, "a{1,2\\\n"},
	{`echo a{1,2\}b`, "a{1,2}b\n"},
	{`echo a{1\,2,3}b`, "a1,2b a3b\n"},
	{`echo a{1\}2,3}b`, "a1}2b a3b\n"},
	{`echo a{1\..2}b`, "a{1..2}b\n"},
	{`echo \{\{iriname\}\}`, "{{iriname}}\n"},
	{
		"echo {1..100000}",
		"brace expansion would exceed 16384 elements\n #IGNORE bash has no defensive limit below MaxInt",
	},
	{
		"echo a{0..9999999999}b",
		"brace expansion would exceed 16384 elements\n #JUSTERR bash errors with a different message",
	},

	// brace expansion in declarations
	{"declare {A,B}_VAR=1; echo $A_VAR $B_VAR", "1 1\n"},
	{"declare {x,y}=val; echo $x $y", "val val\n"},
	{"declare -x RUN_{VERY_,}EXPENSIVE_TESTS=yes; echo $RUN_EXPENSIVE_TESTS", "yes\n"},
	{"declare {A,B}_VAR; A_VAR=1; B_VAR=2; echo $A_VAR $B_VAR", "1 2\n"},
	{"declare {foo=x,bar=y}; echo $foo $bar", "x y\n"},
	{`declare foo{bar=baz`, "declare: invalid name \"foo{bar\"\nexit status 1 #JUSTERR"},
	{"{a,b}=value", "\"a=value\": executable file not found in $PATH\nexit status 127 #JUSTERR"},

	// tilde expansion
	{
		"[[ '~/foo' == ~/foo ]] || [[ ~/foo == '~/foo' ]]",
		"exit status 1",
	},
	{
		"case '~/foo' in ~/foo) echo match ;; esac",
		"",
	},
	{
		"a=~/foo; [[ $a == '~/foo' ]]",
		"exit status 1",
	},
	{
		`a=$(echo "~/foo"); [[ $a == '~/foo' ]]`,
		"",
	},
	{
		`HOME=/foo; rel=/bar; echo ~/bar ~/'bar' ~/"bar" ~/$rel ~/"$rel"`,
		"/foo/bar /foo/bar /foo/bar /foo//bar /foo//bar\n",
	},
	{
		`HOME=/foo; rel=/bar; echo ~'/bar' ~"/bar" ~$rel ~"/$rel"`,
		"~/bar ~/bar ~/bar ~//bar\n",
	},
	{
		`HOME=/foo; echo ~ ~/ ~/'' ~'' ~""`,
		"/foo /foo/ /foo/ ~ ~\n",
	},

	// /dev/null
	{"echo foo >/dev/null", ""},
	{"cat </dev/null", ""},

	// time - real would be slow and flaky; see TestElapsedString
	{"{ time; } |& wc | tr -s ' '", " 4 6 42\n"},
	{"{ time echo -n; } |& wc | tr -s ' '", " 4 6 42\n"},
	{"{ time -p; } |& wc | tr -s ' '", " 3 6 29\n"},
	{"{ time -p echo -n; } |& wc | tr -s ' '", " 3 6 29\n"},

	// unsupported builtins — remaining genuinely-unsupported names route to
	// per-name hints via the default arm. Exhaustive coverage lives in
	// TestUnsupportedHints (unexported_test.go); these spot-check one to
	// catch a regression in the dispatcher's default arm.
	{
		"newgrp staff",
		"newgrp: not supported in this shell — group switching is not supported; switch groups in the parent process (e.g. with sudo -g)\nexit status 2 #JUSTERR #IGNORE S275 decision: an embedded interpreter must not hand control to the host newgrp, which switches groups and may start a new shell",
	},

	// coproc: pipes are real, fd numbers are real, and `<&${CO[0]}` /
	// `>&${CO[1]}` go through fdTable. Without the fdTable lookup the
	// redirect layer would reject any numeric fd >= 3 with "bad fd number".
	// See docs/plan-punted-builtins.md for the numbered-fd refactor scope.
	{
		"coproc CO { read line; echo got=$line; }; echo hi >&${CO[1]}; read out <&${CO[0]}; echo $out",
		"got=hi\n",
	},
	{
		"coproc { :; }; echo ${COPROC[0]} ${COPROC[1]}; wait $COPROC_PID",
		"63 60\n",
	},
	{
		"coproc CO { /bin/sleep 2; }; { sleep 0.05; kill $CO_PID; } & wait $CO_PID; echo status:$?",
		"status:143\n",
	},

	// numbered fds (Phase 2): persistent assignment via exec, scoped via
	// plain redirect, dup forms with N >= 3 on either side. See
	// docs/plan-punted-builtins.md.
	{"echo a >&3", "3: Bad file descriptor\nexit status 1 #JUSTERR"},
	{"exec 3>f; echo a >&3; echo b >&3; cat f", "a\nb\n"},
	{"echo data >f; exec 3<f; read line <&3; echo got=$line", "got=data\n"},
	// scoped 3>f: echo writes to stdout (not fd 3); file f is created empty.
	{"echo a 3>f; cat f", "a\n"},
	// A nested subshell inherits the outer subshell's fd table after
	// `3>&1` is applied, matching bash's redir.tests.
	{"( ( echo hello 1>&3 ) 3>&1 ) 2>&1", "hello\n"},
	{"( ( echo hello 1>&3 ) 3>&1 ) >/dev/null 2>&1 | cat", ""},

	// {var}> named-fd allocator (Phase 2): picks a fresh fd >= 10,
	// stores it in $var so the script can refer to it via `>&$var`.
	{"exec {fd}>f; echo fd=$fd; echo hi >&$fd; exec {fd}>&-; cat f", "fd=10\nhi\n"},
	{"exec {a}>f; exec {b}>g; echo $a $b", "10 11\n"},
	{"echo data >f; exec {fd}<f; read line <&$fd; echo got=$line", "got=data\n"},

	// fg: in-shell builtin waits on the bgProc.done channel and propagates
	// the captured exit status. Without a controlling TTY we don't try to
	// reattach stdio; see docs/plan-punted-builtins.md.
	{"fg", "fg: no job control\nexit status 1 #JUSTERR"},
	{"bg", "bg: no job control\nexit status 1 #JUSTERR"},
	{"fg %99", "fg: no job control\nexit status 1 #JUSTERR"},
	// The backgrounded subshell's stdout is routed to /dev/null: whether its
	// "done" lands before the foreground `fg` errors and the run is captured
	// is a race (bg jobs are goroutines here, real forks in bash — it races
	// in bash too), so keep the captured output deterministic while still
	// exercising fg with a backgrounded job present.
	{"(echo done >/dev/null) & fg", "fg: no job control\nexit status 1 #JUSTERR"},
	{"(exit 7) & fg; echo after=$?", "bash: line 1: fg: no job control\nafter=1\n"},
	// POSIX 2.11: without job control, an asynchronous list reads from a
	// /dev/null-like input unless the command supplies an explicit redirect.
	// This is also Bash's default-mode behavior (VSC-PCTS TP460).
	{"echo leaked >f; exec <f; cat & wait", " #IGNORE S275 decision: the program is parsed up front, not read from stdin; an async job reads /dev/null (VSC-PCTS TP460)"},
	{"echo piped | (cat & wait)", "piped\n"},
	{"for x in 1 2; do { read line; echo $line; } & wait; done <<'EOF'\na\nb\nEOF", "a\nb\n"},

	// umask: per-Runner virtual mask. Reading is 4-digit octal; setting
	// updates only the runner field, not the process.
	{"umask 077; umask", "0077\n"},
	{"umask 022; umask", "0022\n"},
	{"umask 022; umask u=r+w; umask -S", "u=rw,g=rx,o=rx\n"},
	{"umask 022; umask u+w=r+x; umask -S", "u=rx,g=rx,o=rx\n"},
	{"umask 022; umask g+u,o+rwx-u; umask -S", "u=rwx,g=rwx,o=\n"},
	{"umask 022; umask +xwr; umask -S", "u=rwx,g=rwx,o=rwx\n"},
	{"umask 999", "bash: line 1: umask: 999: octal number out of range\nexit status 1"},
	{"umask -i", "umask: -i: invalid option\numask: usage: umask [-p] [-S] [mode]\nexit status 2 #JUSTERR"},
	{"umask 022; umask --; umask", "0022\n0022\n"},

	// logout from a non-login shell errors with the bash-compatible
	// message. The login-shell success path is covered in
	// TestRunnerLoginShell.
	{
		"logout",
		"logout: not login shell: use `exit'\nexit status 1 #JUSTERR",
	},

	// exec
	{"exec", ""},
	{
		"exec builtin echo foo",
		"exec: builtin: not found\nexit status 127 #JUSTERR",
	},
	{
		"exec $GOSH_PROG 'echo foo'; echo bar",
		"foo\n",
	},

	// read
	{
		"read </dev/null",
		"exit status 1",
	},
	{
		"read 1</dev/null",
		"exit status 1",
	},
	{
		"read -X",
		"read: invalid option \"-X\"\nexit status 2 #JUSTERR",
	},
	{
		"read -rX",
		"read: invalid option \"-X\"\nexit status 2 #JUSTERR",
	},
	{
		"read 0ab",
		"read: `0ab': not a valid identifier\nexit status 1 #JUSTERR",
	},
	{
		"read <<< foo; echo $REPLY",
		"foo\n",
	},
	{
		"read <<<'  a  b  c  '; echo \"$REPLY\"",
		"  a  b  c  \n",
	},
	{
		"read <<< 'y\nn\n'; echo $REPLY",
		"y\n",
	},
	{
		"read a_0 <<< foo; echo $a_0",
		"foo\n",
	},
	{
		"read 'a[2]' <<< foo; declare -p a",
		"declare -a a=([2]=\"foo\")\n",
	},
	{
		"declare -A A; read 'A[*]' <<< foo; declare -p A",
		"declare -A A=([\"*\"]=\"foo\" )\n",
	},
	{
		"read a b <<< 'foo  bar  baz  '; echo \"$a\"; echo \"$b\"",
		"foo\nbar  baz\n",
	},
	{
		"while read a; do echo $a; done <<< 'a\nb\nc'",
		"a\nb\nc\n",
	},
	{
		"while read a b; do echo -e \"$a\n$b\"; done <<< '1 2\n3'",
		"1\n2\n3\n\n",
	},
	{
		`read a <<< '\\'; echo "$a"`,
		"\\\n",
	},
	{
		`read a <<< '\a\b\c'; echo "$a"`,
		"abc\n",
	},
	{
		"read -r a b <<< '1\\\t2'; echo $a; echo $b;",
		"1\\\n2\n",
	},
	{
		"echo line\\\ncontinuation | while read a; do echo $a; done",
		"linecontinuation\n",
	},
	{
		"while read a; do echo $a; GOSH_CMD=print_ok $GOSH_PROG; done <<< 'a\nb\nc'",
		"a\nexec ok\nb\nexec ok\nc\nexec ok\n",
	},
	{
		"while read a; do echo $a; GOSH_CMD=print_ok $GOSH_PROG; done <<EOF\na\nb\nc\nEOF",
		"a\nexec ok\nb\nexec ok\nc\nexec ok\n",
	},
	{
		// <<- strips tabs only at the beginning of a physical line. A tab
		// after an expanded parameter remains an IFS separator for read.
		"left=aa; right=bb; while read -r re input expected status; do printf '%s|%s|%s|%s\\n' \"$re\" \"$input\" \"$expected\" \"$status\"; done <<-EOF\n\tpattern\t$left\t$right\t0\nEOF",
		"pattern|aa|bb|0\n",
	},
	{
		// POSIX expr GA142's authority row places two near-RE_DUP_MAX
		// expanded fields next to literal tab separators. Keep the exact
		// long four-field shape which exposed expansion-boundary stripping.
		"dup_max=255; longstr=; i=0; while [ \"$i\" -lt 254 ]; do longstr=${longstr}a; i=$((i + 1)); done; while read -r re input expected status; do printf '%s|%s|%s|%s\\n' \"$re\" \"${#input}\" \"${#expected}\" \"$status\"; done <<-EOF\n\t\\(a\\{1,$dup_max\\}\\)\t$longstr\t$longstr\t0\nEOF",
		"\\(a\\{1,255\\}\\)|254|254|0\n",
	},
	{
		"echo file1 >f; echo file2 >>f; while read a; do echo $a; done <f",
		"file1\nfile2\n",
	},
	{
		"read -u 42 a",
		"read: 42: invalid file descriptor: Bad file descriptor\nexit status 2 #JUSTERR",
	},
	// TODO: our final exit status here isn't right.
	// {
	// 	"while read a; do echo $a; GOSH_CMD=print_fail $GOSH_PROG; done <<< 'a\nb\nc'",
	// 	"a\nexec fail\nb\nexec fail\nc\nexec fail\nexit status 1",
	// },
	{
		`read -r a <<< '\\'; echo "$a"`,
		"\\\\\n",
	},
	{
		"read -r a <<< '\\a\\b\\c'; echo $a",
		"\\a\\b\\c\n",
	},
	{
		"IFS=: read a b c <<< '1:2:3'; echo $a; echo $b; echo $c",
		"1\n2\n3\n",
	},
	{
		"IFS=: read a b c <<< '1\\:2:3'; echo \"$a\"; echo $b; echo $c",
		"1:2\n3\n\n",
	},
	{
		"read -n 5 a <<< 'абвгдежзиклмноп '; echo -$a- ${#a}",
		"-абвгд- 5\n",
	},
	{
		"read -p",
		"read: -p: option requires an argument\nexit status 2 #JUSTERR",
	},
	{
		"read -X -p",
		"read: invalid option \"-X\"\nexit status 2 #JUSTERR",
	},
	{
		"read -p 'Display me as a prompt. Continue? (y/n) ' choice <<< 'y'; echo $choice",
		"y\n",
	},
	{
		"read -r -p 'Prompt and raw flag together: ' a <<< '\\a\\b\\c'; echo $a",
		"\\a\\b\\c\n",
	},
	{
		"read -p 'no prompt: ' a <<EOF\nvalue\nEOF\necho $a",
		"value\n",
	},

	// read -t TIMEOUT
	{
		"read -t",
		"read: -t: option requires an argument\nexit status 2 #JUSTERR",
	},
	{
		"read -t bogus x",
		"read: bogus: invalid timeout specification\nexit status 1 #JUSTERR",
	},
	{
		"read -t -1 x",
		"read: -1: invalid timeout specification\nexit status 1 #JUSTERR",
	},
	{
		"read -t 5 x <<< hello; echo $x",
		"hello\n",
	},
	{
		"read -t 5 x </dev/null; echo $?",
		"1\n",
	},
	{
		"read -t 0.05 x < <(sleep 0.2); echo $?",
		"142\n",
	},
	{
		"a=4; read -t 0.05 a < <(sleep 0.2); status=$?; echo ${a:-unset} $status",
		"unset 142\n",
	},
	{
		"a=4; read -t 0.000001 a <<< abcde; status=$?; echo ${a:-unset} $status",
		"abcde 0\n #IGNORE S275 decision: GNU Bash races its 1us alarm against the here-string read (142 on Linux); ready input is read here",
	},
	{
		"a=4; read -t 0.000001 a <<<abcde; status=$?; echo ${a:-unset} $status",
		"abcde 0\n #IGNORE S275 decision: GNU Bash races its 1us alarm against the here-string read (142 on Linux); ready input is read here",
	},
	{
		"echo abcde | { read -t 0.5 a; status=$?; echo ${a:-unset} $status; }",
		"abcde 0\n",
	},

	// read -a
	{
		`echo "1 2 3" | { read -a arr; echo "${arr[0]} ${arr[1]} ${arr[2]}"; }`,
		"1 2 3\n",
	},
	{
		`echo "a b c" | { read -a arr; echo "${#arr[@]}"; }`,
		"3\n",
	},
	{
		`echo "" | { read -a arr; echo "${#arr[@]}"; }`,
		"0\n",
	},
	{
		`echo 'a\tb' | { read -ra arr; echo "${#arr[@]} ${arr[0]}"; }`,
		"1 a\\tb\n",
	},
	{
		`a=a; echo | (read a; echo -n "$a")`,
		"",
	},
	{
		`a=b; read a < /dev/null; echo -n "$a"`,
		"",
	},
	{
		"a=c; echo x | (read a; echo -n $a)",
		"x",
	},
	{
		"a=d; echo -n y | (read a; echo -n $a)",
		"y",
	},

	// getopts
	{
		"getopts",
		"getopts: usage: getopts optstring name [arg ...]\nexit status 2",
	},
	{
		"getopts a a:b",
		"getopts: `a:b': not a valid identifier\nexit status 1 #JUSTERR",
	},
	{
		"set -- -a; getopts -- a opt; echo status=$? opt=$opt OPTIND=$OPTIND",
		"status=0 opt=a OPTIND=2\n",
	},
	{
		"set -- -c foo -h; getopts 'hc:' opt-; echo status=$? opt=$opt OPTARG=$OPTARG OPTIND=$OPTIND",
		"getopts: `opt-': not a valid identifier\nstatus=1 opt= OPTARG=foo OPTIND=3\n #JUSTERR",
	},
	{
		"getopts abc opt -a; echo $opt; $optarg",
		"a\n",
	},
	{
		"getopts abc opt -z",
		"bash: illegal option -- z\n #IGNORE",
	},
	{
		"getopts a: opt -a",
		"bash: option requires an argument -- a\n #IGNORE",
	},
	{
		"getopts :abc opt -z; echo $opt; echo $OPTARG",
		"?\nz\n",
	},
	{
		"getopts : opt -:; echo $opt; echo $OPTARG",
		"?\n:\n",
	},
	{
		"getopts :a: opt -a; echo $opt; echo $OPTARG",
		":\na\n",
	},
	{
		"readonly OPTARG && getopts a: opt -a foo; echo status:$?; echo ${OPTARG-unset}",
		"bash: line 1: OPTARG: readonly variable\nstatus:0\nunset\n",
	},
	{
		"getopts abc opt foo -a; echo $opt; echo $OPTIND",
		"?\n1\n",
	},
	{
		"getopts abc opt -a foo; echo $opt; echo $OPTIND",
		"a\n2\n",
	},
	{
		"OPTIND=3; getopts abc opt -a -b -c; echo $opt;",
		"c\n",
	},
	{
		"OPTIND=100; getopts abc opt -a -b -c; echo $opt;",
		"?\n",
	},
	{
		"OPTIND=foo; getopts abc opt -a -b -c; echo $opt;",
		"a\n",
	},
	{
		"while getopts ab:c opt -c -b arg -a foo; do echo $opt $OPTARG $OPTIND; done",
		"c 2\nb arg 4\na 5\n",
	},
	{
		"while getopts abc opt -ba -c foo; do echo $opt $OPTARG $OPTIND; done",
		"b 1\na 2\nc 3\n",
	},
	{
		"a() { while getopts abc: opt; do echo $opt $OPTARG; done }; a -a -b -c arg",
		"a\nb\nc arg\n",
	},
	{
		"while getopts 'hc:' opt; do echo '-'; done; echo OPTIND=$OPTIND; set -- -h -c foo x y z; while getopts 'hc:' opt; do echo '-'; done; echo OPTIND=$OPTIND; set --; while getopts 'hc:' opt; do echo '-'; done; echo OPTIND=$OPTIND",
		"OPTIND=1\n-\n-\nOPTIND=4\nOPTIND=1\n",
	},
	{
		"set -- -a; while getopts 'ab:' opt; do echo '.'; done; echo OPTIND=$OPTIND; set -- -c -d -e foo; while getopts 'cde:' opt; do echo '-'; done; echo OPTIND=$OPTIND; set -- -f; while getopts 'f:' opt; do echo '_'; done; echo OPTIND=$OPTIND",
		".\nOPTIND=2\n-\n-\nOPTIND=5\nOPTIND=2\n",
	},
	// mapfile
	{
		"mapfile <<EOF\na\nb\nc\nEOF\n" + `for x in "${MAPFILE[@]}"; do echo "$x"; done`,
		"a\n\nb\n\nc\n\n",
	},
	{
		"mapfile -t <<EOF\na\nb\nc\nEOF\n" + `for x in "${MAPFILE[@]}"; do echo "$x"; done`,
		"a\nb\nc\n",
	},
	{
		"mapfile -u 42 A",
		"mapfile: 42: invalid file descriptor: Bad file descriptor\nexit status 2 #JUSTERR",
	},
	{
		"mapfile ''",
		"mapfile: empty array variable name\nexit status 1 #JUSTERR",
	},
	{
		"mapfile -t -d b <<EOF\nabc\nEOF\n" + `for x in "${MAPFILE[@]}"; do echo "$x"; done`,
		"a\nc\n\n",
	},
	{
		"mapfile -t butter <<EOF\na\nb\nc\nEOF\n" + `for x in "${butter[@]}"; do echo "$x"; done`,
		"a\nb\nc\n",
	},
}

var runTestsUnix = []runTest{
	{"[[ -n $PPID && $PPID -ge 0 ]]", ""}, // can be 0 if running as the init process
	{
		"kill -s CHLD $$; echo chld:$?; kill -s CONT $$; echo cont:$?; kill -s URG $$; echo urg:$?",
		"chld:0\ncont:0\nurg:0\n",
	},
	{
		"for sig in STOP TSTP TTIN TTOU; do (kill -s $sig $$; status=$?; kill -s CONT $$; exit $status); echo $sig:$?; done",
		"STOP:0\nTSTP:0\nTTIN:0\nTTOU:0\n",
	},
	{
		"while kill -s 0 $$; do :; done & kill -s USR1 $!; wait $!; s=$?; [ $s -gt 128 ] && echo signaled",
		"signaled\n #IGNORE S275 decision: a goroutine-backed job has no kernel PID, so no numeric termination notice is printed",
	},

	{
		`mkdir d1 d2; echo 'echo ERROR' >d1/s; echo 'echo OK' >d2/s; chmod a=wx d1/s; chmod a=r d2/s; PATH=$PWD/d1:$PWD/d2:$PATH; . s`,
		"OK\n",
	},

	// exec -a NAME CMD overrides argv[0] of the spawned process; the
	// shell does not return (so "unreached" is never printed).
	{
		`exec -a renamed /bin/sh -c 'printf "%s\n" "$0"'; echo unreached`,
		"renamed\n",
	},
	{
		`FOO=BAR; export FOO; exec -c /bin/sh -c 'printf "%s\n" "${FOO-unset}"'; echo unreached`,
		"unset\n",
	},
	{
		`exec -a`,
		"exec: -a: option requires an argument\nexit status 2 #JUSTERR",
	},
	{
		`exec -a foo`,
		"",
	},
	{
		`enable -d notbuiltin`,
		"enable: notbuiltin: not a shell builtin\nexit status 1 #JUSTERR",
	},
	{
		`enable -d test`,
		"enable: test: not dynamically loaded\nexit status 1 #JUSTERR",
	},
	{
		`enable -f ./strmatch.so strmatch; enable -d strmatch`,
		"",
	},
	{
		`shopt -s expand_aliases; alias let='let --'; let '1 == 1'`,
		"",
	},
	{
		`alias '\$'=xx`,
		"alias: `\\$': invalid alias name\nexit status 1 #JUSTERR",
	},
	{
		`BASH_ALIASES['\$']=xx`,
		"`\\$': invalid alias name\nexit status 1 #JUSTERR",
	},
	{
		`exec -z echo hi`,
		"exec: invalid option \"-z\"\nexit status 2 #JUSTERR",
	},
	{
		// no root user on windows
		"[[ ~root == '~root' ]]",
		"exit status 1",
	},

	// windows does not support paths with '*'
	{
		"mkdir -p '*/a.z' 'b/a.z'; cd '*'; set -- *.z; echo $#",
		"1\n",
	},
	{
		"mkdir -p 'a-*/d'; test -d $PWD/a-*/*",
		"",
	},

	// no fifos on windows
	{
		"[ -p a ] && echo x; mkfifo a; [ -p a ] && echo y",
		"y\n",
	},
	{
		"[[ -p a ]] && echo x; mkfifo a; [[ -p a ]] && echo y",
		"y\n",
	},

	{"sh() { :; }; sh -c 'echo foo'", ""},
	{"sh() { :; }; command sh -c 'echo foo'", "foo\n"},

	// chmod is practically useless on Windows
	{
		"[ -x a ] && echo x; >a; chmod 0755 a; [ -x a ] && echo y",
		"y\n",
	},
	{
		"[[ -x a ]] && echo x; >a; chmod 0755 a; [[ -x a ]] && echo y",
		"y\n",
	},
	{
		">a; [ -k a ] && echo x; chmod +t a; [ -k a ] && echo y",
		"y\n",
	},
	{
		">a; [ -u a ] && echo x; chmod u+s a; [ -u a ] && echo y",
		"y\n",
	},
	{
		">a; [ -g a ] && echo x; chmod g+s a; [ -g a ] && echo y",
		"y\n",
	},
	{
		">a; [[ -k a ]] && echo x; chmod +t a; [[ -k a ]] && echo y",
		"y\n",
	},
	{
		">a; [[ -u a ]] && echo x; chmod u+s a; [[ -u a ]] && echo y",
		"y\n",
	},
	{
		">a; [[ -g a ]] && echo x; chmod g+s a; [[ -g a ]] && echo y",
		"y\n",
	},
	{
		`mkdir a; chmod 0100 a; cd a`,
		"",
	},
	// Note that these will succeed if we're root.
	{
		`mkdir a; chmod 0000 a; cd a`,
		"cd: a: Permission denied\nexit status 1 #JUSTERR",
	},
	{
		`mkdir a; chmod 0222 a; cd a`,
		"cd: a: Permission denied\nexit status 1 #JUSTERR",
	},
	{
		`mkdir a; chmod 0444 a; cd a`,
		"cd: a: Permission denied\nexit status 1 #JUSTERR",
	},
	{
		`mkdir a; chmod 0010 a; cd a`,
		"cd: a: Permission denied\nexit status 1 #JUSTERR",
	},
	{
		`mkdir a; chmod 0001 a; cd a`,
		"cd: a: Permission denied\nexit status 1 #JUSTERR",
	},
	{
		`unset UID`,
		"unset: UID: cannot unset: readonly variable\nexit status 1 #IGNORE",
	},
	{
		`unset -v BASH_LINENO BASH_SOURCE`,
		"unset: BASH_LINENO: cannot unset\nunset: BASH_SOURCE: cannot unset\nexit status 1 #JUSTERR",
	},
	{
		`test -n "$EUID" && echo OK`,
		"OK\n",
	},
	{
		`set EUID=newvalue; test EUID != newvalue && echo OK || echo EUID=$EUID`,
		"OK\n",
	},
	{
		`unset EUID`,
		"unset: EUID: cannot unset: readonly variable\nexit status 1 #IGNORE",
	},
	// GID is not a special shell variable in bash.
	{
		`unset GID`,
		"",
	},
	{
		`GID=123; echo $GID`,
		"123\n",
	},
	{
		`[[ -z $GID ]] && echo "GID not set"`,
		"GID not set\n",
	},

	// Unix-y PATH
	{
		"PATH=; bash -c 'echo foo'",
		"\"bash\": executable file not found in $PATH\nexit status 127 #JUSTERR",
	},
	// NOTE: bash 5.3 and dash both run `echo`/`pwd` as regular builtins even
	// with an empty PATH in posix mode (verified: real bash `set -o posix;
	// PATH=; echo hi` prints "hi"; dash likewise). A previous change made
	// bashy require them on PATH, which regressed the bash-5.3 execscript
	// fixture (its `echo $?` under PATH= stopped printing) and diverged from
	// both reference shells. Regular builtins stay available regardless of PATH.
	{
		"cd /; sure/is/missing",
		"stat /sure/is/missing: no such file or directory\nexit status 127 #JUSTERR",
	},
	{
		"echo '#!/bin/sh\necho b' >a; chmod 0755 a; PATH=; a",
		"b\n",
	},
	{
		"mkdir c; cd c; echo '#!/bin/sh\necho b' >a; chmod 0755 a; PATH=; a",
		"b\n",
	},
	{
		"mkdir c; echo '#!/bin/sh\necho b' >c/a; chmod 0755 c/a; c/a",
		"b\n",
	},
	{
		"GOSH_CMD=lookpath $GOSH_PROG",
		"sh found\n",
	},

	// error strings which are too different on Windows
	{
		"echo foo >/shouldnotexist/file",
		"open /shouldnotexist/file: no such file or directory\nexit status 1 #JUSTERR",
	},
	{
		"set -e; echo foo >/shouldnotexist/file; echo foo",
		"open /shouldnotexist/file: no such file or directory\nexit status 1 #JUSTERR",
	},
	{
		// A redirection error on a command with no command word still
		// fails the command, so errexit exits the shell.
		"set -e; </shouldnotexist/file; echo foo",
		"open /shouldnotexist/file: no such file or directory\nexit status 1 #JUSTERR",
	},
	{
		// ... but it's exempt as the left operand of || (errexit only
		// acts on the final status of the list).
		"set -e; </shouldnotexist/file || echo handled; echo foo",
		"open /shouldnotexist/file: no such file or directory\nhandled\nfoo\n #JUSTERR",
	},

	// process substitution; named pipes (fifos) are a TODO for windows
	{
		"sed 's/o/e/g' <(echo foo bar)",
		"fee bar\n",
	},
	{
		"cat <(echo foo) <(echo bar) <(echo baz)",
		"foo\nbar\nbaz\n",
	},
	{
		"cat <(cat <(echo nested))",
		"nested\n",
	},
	{
		"cat ${foo:-<(echo a)}",
		"a\n",
	},
	{
		// The tests here use "wait" because otherwise the parent may finish before
		// the subprocess has had time to process the input and print the result.
		"echo foo bar > >(sed 's/o/e/g'); wait",
		"fee bar\n",
	},
	{
		"echo foo bar | tee >(sed 's/o/e/g') >/dev/null; wait",
		"fee bar\n",
	},
	{
		"echo nested > >(cat > >(cat); wait); wait",
		"nested\n",
	},
	{
		"cat <(exit 0); wait $!; echo $?",
		"0\n",
	},
	{
		"cat <(exit 5); wait $!; echo $?",
		"5\n",
	},
	{
		// The reader here does not consume the named pipe.
		"test -e <(echo foo)",
		"",
	},
	// echo trace
	{
		`set -x; animals=("dog", "cat", "otter"); echo "hello ${animals[*]}"`,
		`+ animals=("dog", "cat", "otter")
+ echo 'hello dog, cat, otter'
hello dog, cat, otter
`,
	},
	{
		`set -x; s="always print a decimal point for %e, %E, %f, %F, %g and %G; do not remove trailing zeros for %g and %G"; echo "$s"`,
		`+ s='always print a decimal point for %e, %E, %f, %F, %g and %G; do not remove trailing zeros for %g and %G'
+ echo 'always print a decimal point for %e, %E, %f, %F, %g and %G; do not remove trailing zeros for %g and %G'
always print a decimal point for %e, %E, %f, %F, %g and %G; do not remove trailing zeros for %g and %G
`,
	},
	{
		`set -x; var=42; (( $var )); null=; (( $null )); set +x`,
		`+ var=42
+ ((  42  ))
+ null=
+ ((    ))
+ set +x
`,
	},
	{
		`set -x
x=without; echo "$x"
x="double quote"; echo "$x"
x='single quote'; echo "$x"`,
		`+ x=without
+ echo without
without
+ x='double quote'
+ echo 'double quote'
double quote
+ x='single quote'
+ echo 'single quote'
single quote
`,
	},
	// for trace
	{
		`set -x
exec >/dev/null
echo "trace should go to stderr"`,
		`+ exec
+ echo 'trace should go to stderr'
`,
	},
	{
		`set -x
animals=(dog, cat, otter)
for i in ${animals[@]}
do
   echo "hello ${i}"
done
`,
		`+ animals=(dog, cat, otter)
+ for i in ${animals[@]}
+ echo 'hello dog,'
hello dog,
+ for i in ${animals[@]}
+ echo 'hello cat,'
hello cat,
+ for i in ${animals[@]}
+ echo 'hello otter'
hello otter
`,
	},
	{
		`set -x
loop() {
    for i do
        echo "something with $i"
    done
}
loop 1 2 3`,
		`+ loop 1 2 3
+ for i in "$@"
+ echo 'something with 1'
something with 1
+ for i in "$@"
+ echo 'something with 2'
something with 2
+ for i in "$@"
+ echo 'something with 3'
something with 3
`,
	},
	{
		`set -x; animals=(dog, cat, otter); for i in ${animals[@]}; do echo "hello ${i}"; done`,
		`+ animals=(dog, cat, otter)
+ for i in ${animals[@]}
+ echo 'hello dog,'
hello dog,
+ for i in ${animals[@]}
+ echo 'hello cat,'
hello cat,
+ for i in ${animals[@]}
+ echo 'hello otter'
hello otter
`,
	},
	{
		`set -x; a=x"y"$z b=(foo bar $none '')`,
		"+ a=xy\n+ b=(foo bar $none '')\n",
	},
	{
		`set -x; for i in a b; do echo $i; done`,
		`+ for i in a b
+ echo a
a
+ for i in a b
+ echo b
b
`,
	},
	{
		`set -x; for i in $none_a $none_b; do echo $i; done`,
		``,
	},
	// case trace
	{
		`set -x; pet=dog; case $pet in 'dog') echo "barks";; *) echo "unknown";; esac`,
		`+ pet=dog
+ case $pet in
+ echo barks
barks
`,
	},
	{
		`set -x
pet="dog"
case $pet in
  dog)
    echo "barks"
    ;;
  *)
    echo "unknown"
    ;;
esac`,
		`+ pet=dog
+ case $pet in
+ echo barks
barks
`,
	},
	// arithmetic
	{
		`set -x
a=$(( 4 + 5 )); echo $a
a=$((3+5)); echo $a`,
		`+ a=9
+ echo 9
9
+ a=8
+ echo 8
8
`,
	},
	{
		`set -x;
let a=5+4; echo $a
let "a = 5 + 4"; echo $a
let a++; echo $a`,
		`+ let a=5+4
+ echo 9
9
+ let 'a = 5 + 4'
+ echo 9
9
+ let a++
+ echo 10
10
`,
	},
	// functions
	{
		`set -x; function with_function () { echo 'hello, world'; }; with_function`,
		`+ with_function
+ echo 'hello, world'
hello, world
`,
	},
	{
		`set -x; without_function () { echo 'hello, world'; }; without_function`,
		`+ without_function
+ echo 'hello, world'
hello, world
`,
	},
	{
		// globbing wildcard as function name
		`@() { echo "$@"; }; @ lala; function +() { echo "$@"; }; + foo`,
		"lala\nfoo\n",
	},
	{
		`      @() { echo "$@"; }; @ lala;`,
		"lala\n",
	},
	{
		// globbing wildcard as function name but with space after the name
		`+ () { echo "$@"; }; + foo; @ () { echo "$@"; }; @ lala; ? () { echo "$@"; }; ? bar`,
		"foo\nlala\nbar\n",
	},
	// mapfile, no process substitution yet on Windows
	{
		`mapfile -t -d "" < <(printf "a\0b\n"); for x in "${MAPFILE[@]}"; do echo "$x"; done`,
		"a\nb\n\n",
	},
	// Windows does not support having a `\n` in a filename
	{
		`> $'bar\nbaz'; echo bar*baz`,
		"bar\nbaz\n",
	},
}

var runTestsWindows = []runTest{
	{"[[ -n $PPID || $PPID -gt 0 ]]", ""}, // os.Getppid can be 0 on windows
	{"cmd() { :; }; cmd /c 'echo foo'", ""},
	{"cmd() { :; }; command cmd /c 'echo foo'", "foo\r\n"},
	{
		"GOSH_CMD=lookpath $GOSH_PROG",
		"cmd found\n",
	},
	{
		"localCase=camel; LocalCase=pascal; echo $localcase",
		"pascal\n",
	},
	{
		// Matching the env var name set as a global
		// in a case sensitive way.
		"$ENV_PROG | grep -i '^mixedCase_interp'",
		"mixedCase_INTERP_GLOBAL=value\n",
	},
	{
		// Overwriting the env var set as a global
		// in a case insensitive way.
		"MIXEDCASE_interp_global=replaced; echo $MIXEDCASE_interp_GLOBAL",
		"replaced\n",
	},
	{
		"MIXEDCASE_interp_global=replaced; $ENV_PROG | grep -i '^mixedcase_interp'",
		"MIXEDCASE_interp_global=replaced\n",
	},
}

// These tests are specific to 64-bit architectures, and that's fine. We don't
// need to add explicit versions for 32-bit.
var runTests64bit = []runTest{
	{"printf %i,%u -3 -3", "-3,18446744073709551613"},
	{"printf %o -3", "1777777777777777777775"},
	{"printf %x -3", "fffffffffffffffd"},
	// 64-bit arithmetic: correct on 64-bit platforms; on 32-bit the evaluator
	// truncates to native int until the int64 arith API migration (v4).
	{"[[ 1000000000123 -lt 100 ]]", "exit status 1"},
	{"[[ 1000000000123 -eq 1000000000456 ]]", "exit status 1"},
	{"((1000000000123 == 1000000000456))", "exit status 1"},
	{"v=-9223372036854775808; echo $((v)); echo $((v / -1)); echo $((v * -1)); echo $((-v))", "-9223372036854775808\n-9223372036854775808\n-9223372036854775808\n-9223372036854775808\n"},
}

func init() {
	if runtime.GOOS == "linux" {
		runTests[226].want = "bash: line 1: printf: Value too large for defined data type\nexit status 1"
		runTests[717].want = "exit status 1"
	}
	if runtime.GOOS != "windows" {
		runTests[1403].want = typeEchoKinds(os.Getenv("PATH"))
	}
	if runtime.GOOS == "windows" {
		runTests = append(runTests, runTestsWindows...)
	} else { // Unix-y
		runTests = append(runTests, runTestsUnix...)
	}
	if bits.UintSize == 64 {
		runTests = append(runTests, runTests64bit...)
	}
	// GNU Bash 5.3 treats an invalid -o/+o name as a fatal special
	// builtin error in non-interactive POSIX mode. Flag forms stay valid.
	runTests = append(runTests,
		runTest{`set -o posix; set -o a; echo after`, "bash: line 1: set: a: invalid option name\nexit status 2"},
		runTest{`set -o posix; set +o f; echo after`, "bash: line 1: set: f: invalid option name\nexit status 2"},
		runTest{`set -a; set +f; [[ -o allexport ]] && [[ ! -o noglob ]]`, ""},
		runTest{`HOME=$PWD; [[ $(dirs) == '~' && $(dirs -l) == "$PWD" ]]`, ""},
	)
}

func killListFirstLine() string {
	if runtime.GOOS == "windows" {
		return " 1) SIGHUP        2) SIGINT        3) SIGQUIT       4) SIGILL        5) SIGTRAP      \n"
	}
	return " 1) SIGHUP\t 2) SIGINT\t 3) SIGQUIT\t 4) SIGILL\t 5) SIGTRAP\n"
}

func typeEchoKinds(pathList string) string {
	result := "builtin\n"
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, "echo")); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			result += "file\n"
		}
	}
	return result
}

// ln -s: wine doesn't implement symlinks; see https://bugs.winehq.org/show_bug.cgi?id=44948
// process substitutions are not supported on Windows
var skipOnWindows = regexp.MustCompile(`ln -s|<\(`)

// process substitutions seemflaky on mac; see https://github.com/mvdan/sh/issues/576
var skipOnMac = regexp.MustCompile(`>\(|<\(`)

// busybox's /bin/sh is a multi-call binary: `exec -a renamed /bin/sh …`
// invokes it with argv[0]="renamed", so it looks up a (non-existent)
// "renamed" applet instead of running as a shell. The argv[0] assertion thus
// holds for bash/dash but not busybox (e.g. on alpine), so skip it there.
var skipOnBusyboxSh = regexp.MustCompile(`exec -a \w+ /bin/sh\b`)

func binShIsBusybox() bool {
	p, err := filepath.EvalSymlinks("/bin/sh")
	return err == nil && strings.Contains(p, "busybox")
}

func skipIfUnsupported(tb testing.TB, src string) {
	switch {
	case runtime.GOOS == "windows" && skipOnWindows.MatchString(src):
		tb.Skipf("skipping non-portable test on windows")
	case runtime.GOOS == "darwin" && skipOnMac.MatchString(src):
		tb.Skipf("skipping non-portable test on mac")
	case skipOnBusyboxSh.MatchString(src) && binShIsBusybox():
		tb.Skipf("skipping argv[0] test: /bin/sh is busybox (multi-call applet lookup)")
	case strings.Contains(src, "chmod u+s") && !supportsSetIDMode(os.ModeSetuid):
		tb.Skipf("skipping setuid mode test on filesystem without setuid support")
	case strings.Contains(src, "chmod g+s") && !supportsSetIDMode(os.ModeSetgid):
		tb.Skipf("skipping setgid mode test on filesystem without setgid support")
	}
}

func supportsSetIDMode(bit os.FileMode) bool {
	dir, err := os.MkdirTemp("", "sh-setid-test-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "a")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return false
	}
	if err := os.Chmod(path, 0o644|bit); err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&bit != 0
}

func TestRunnerRun(t *testing.T) {
	t.Parallel()

	p := syntax.NewParser()
	for _, c := range runTests {
		t.Run("", func(t *testing.T) {
			skipIfUnsupported(t, c.in)
			t.Logf("input: %q", c.in)

			// Parse first, as we reuse a single parser.
			file := parse(t, p, c.in)

			t.Parallel()

			tdir := t.TempDir()
			var cb concBuffer
			// Oracle cases with a Bash stdin prefix exercise the runner's
			// compatible diagnostic mode. Source bytes are needed to quote
			// malformed expansion text exactly as Bash prints it.
			var bashSource []byte
			if strings.Contains(c.in, "swap32_posix") ||
				strings.Contains(c.want, "bash: line 1: ") ||
				strings.Contains(c.want, "bad substitution") ||
				strings.Contains(c.want, "assignment requires lvalue") {
				bashSource = []byte(c.in)
			}
			opts := []interp.RunnerOption{interp.Dir(tdir), interp.StdIO(nil, &cb, &cb),
				interp.StandardInput(true),
				interp.WithBashCompatErrors(strings.Contains(c.want, "bash: line 1: ")),
				interp.WithBashSource(bashSource),
				interp.ExecHandlers(testExecHandler),
			}
			if runtime.GOOS == "linux" && c.in == runTests[717].in {
				// The GNU comparator starts this case with HOME=$PWD.
				// `dirs` abbreviates that path to ~ in the subshell.
				opts = append(opts, interp.Env(expand.ListEnviron(append(os.Environ(), "HOME="+tdir)...)))
			}
			r, err := interp.New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.WriteString(err.Error())
			}

			// Some builtins like "pushd" can show absolute paths as part of error messages.
			// Allow a very simple search-and-replace for the equivalent to "$PWD/a".
			want := strings.ReplaceAll(c.want, "ABS_PATH_A", filepath.Join(tdir, "a"))
			want = strings.ReplaceAll(want, "ABS_PATH_D", filepath.Join(tdir, "d"))

			if i := strings.Index(want, " #"); i >= 0 {
				want = want[:i]
			}
			if got := cb.String(); got != want {
				if len(got) > 200 {
					got = "…" + got[len(got)-200:]
				}
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q",
					c.in, want, got)
			}
		})
	}
}

func TestCoprocExternalReflectorTerminatesBySyntheticPid(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src := `
coproc REFLECT { "$GOSH_PROG"; }
echo ${REFLECT[@]}
echo flop >&${REFLECT[1]}
read LINE <&${REFLECT[0]}
echo "$LINE"
{ sleep 0.05; kill $REFLECT_PID; } &
wait $REFLECT_PID >/dev/null 2>&1 || { status=$?; echo "status:$status"; }
`
	file := parse(t, nil, src)
	var stdout, stderr concBuffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron(append(os.Environ(),
			"GOSH_PROG="+exe,
			"GOSH_CMD=reflect",
		)...)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "63 60\nflop\nstatus:143\n"; got != want {
		t.Fatalf("stdout = %q, want %q; stderr=%q", got, want, stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestCoprocInProcessReflectorCancellationClosesPipes(t *testing.T) {
	t.Parallel()

	src := `
coproc REFLECT { reflect; }
echo flop >&${REFLECT[1]}
read LINE <&${REFLECT[0]}
echo "$LINE"
kill $REFLECT_PID
wait $REFLECT_PID >/dev/null 2>&1 || echo "status:$?"
`
	file := parse(t, nil, src)
	var stdout, stderr concBuffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.ExecHandler(func(ctx context.Context, args []string) error {
			if args[0] != "reflect" {
				return fmt.Errorf("unexpected command %q", args[0])
			}
			hc := interp.HandlerCtx(ctx)
			_, err := io.Copy(hc.Stdout, hc.Stdin)
			return err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "flop\nstatus:143\n"; got != want {
		t.Fatalf("stdout = %q, want %q; stderr=%q", got, want, stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestRunAliasExpandedSourceLineCallerContext(t *testing.T) {
	const setup = "shopt -s expand_aliases\nset -o posix\nalias short='probe )'\n"
	const source = setup + "echo \"$( short \"\n"
	type contextKey struct{}
	var output bytes.Buffer // bashpp-racegate:safe-synchronized recovery calls and output reads execute synchronously.
	r, err := interp.New(interp.StdIO(nil, &output, &output), interp.WithBashSource([]byte(source)),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if args[0] != "probe" {
					return next(ctx, args)
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				_, err := fmt.Fprint(interp.HandlerCtx(ctx).Stdout, ctx.Value(contextKey{}))
				return err
			}
		}))
	qt.Assert(t, qt.IsNil(err))
	defer r.Reset()
	qt.Assert(t, qt.IsNil(r.Run(t.Context(), parse(t, nil, setup))))
	caller := context.WithValue(t.Context(), contextKey{}, "ok 8")
	qt.Assert(t, qt.IsTrue(r.RunAliasExpandedSourceLine(caller, 4)))
	qt.Assert(t, qt.Equals(output.String(), "ok 8 \n"))
	output.Reset()
	canceled, cancel := context.WithCancel(caller)
	cancel()
	qt.Assert(t, qt.IsTrue(r.RunAliasExpandedSourceLine(canceled, 4)))
	qt.Assert(t, qt.Equals(output.String(), ""))
}

func TestRunnerInheritedUnsetRestoredAfterFunctionTemp(t *testing.T) {
	for _, mode := range []string{"default", "posix"} {
		for _, inherited := range []bool{false, true} {
			name := fmt.Sprintf("%s/inherited=%t", mode, inherited)
			t.Run(name, func(t *testing.T) {
				prefix := ""
				if mode == "posix" {
					prefix = "set -o posix; "
				}
				src := prefix + "outer() { unset LC_ALL; " +
					"LC_CTYPE=C; export LC_CTYPE; LC_ALL=POSIX probe; observe; }; " +
					"outer; observe"
				env := expand.ListEnviron()
				if inherited {
					env = expand.ListEnviron("LC_ALL=POSIX")
				}
				file := parse(t, nil, src)
				var stdout, stderr bytes.Buffer
				r, err := interp.New(
					interp.Env(env),
					interp.StdIO(nil, &stdout, &stderr),
					interp.ExecHandlers(localeTempProbeHandler),
				)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Run(context.Background(), file); err != nil {
					t.Fatal(err)
				}
				want := "LC_ALL_set=false LC_CTYPE=<C>\nLC_ALL_set=false LC_CTYPE=<C>\n"
				if got := stdout.String(); got != want {
					t.Fatalf("stdout mismatch:\nwant: %q\ngot:  %q\nstderr: %q", want, got, stderr.String())
				}
			})
		}
	}
}

func TestRunnerInheritedUnsetByTemporaryUnset(t *testing.T) {
	for _, mode := range []string{"default", "posix"} {
		t.Run(mode, func(t *testing.T) {
			prefix := ""
			if mode == "posix" {
				prefix = "set -o posix; "
			}
			file := parse(t, nil, prefix+"outer() { unset LC_ALL; LC_ALL=temp unset LC_ALL; observe; }; outer; observe")
			var stdout bytes.Buffer
			r, err := interp.New(
				interp.Env(expand.ListEnviron("LC_ALL=POSIX")),
				interp.StdIO(nil, &stdout, nil),
				interp.ExecHandlers(localeTempProbeHandler),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			want := "LC_ALL_set=false LC_CTYPE=<>\nLC_ALL_set=false LC_CTYPE=<>\n"
			if got := stdout.String(); got != want {
				t.Fatalf("stdout mismatch:\nwant: %q\ngot:  %q", want, got)
			}
		})
	}
}

func localeTempProbeHandler(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		switch args[0] {
		case "probe":
			vr := hc.Env.Get("LC_ALL")
			if !vr.IsSet() || vr.String() != "POSIX" {
				return fmt.Errorf("probe LC_ALL: got set=%t value=%q, want set POSIX", vr.IsSet(), vr.String())
			}
			return nil
		case "observe":
			fmt.Fprintf(hc.Stdout, "LC_ALL_set=%t LC_CTYPE=<%s>\n",
				hc.Env.Get("LC_ALL").IsSet(), hc.Env.Get("LC_CTYPE").String())
			return nil
		}
		return next(ctx, args)
	}
}

func TestPrintfPlusLeadingFormat(t *testing.T) {
	tests := []runTest{
		{in: "printf +3", want: "+3"},
		{in: `printf '+3: %s\n' world`, want: "+3: world\n"},
		{in: `printf -v out +3; printf '%s' "$out"`, want: "+3"},
		{in: `printf -- '+3: %s\n' world`, want: "+3: world\n"},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			file := parse(t, nil, test.in)
			var cb concBuffer
			r, err := interp.New(interp.StdIO(nil, &cb, &cb))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.WriteString(err.Error())
			}
			if got := cb.String(); got != test.want {
				t.Fatalf("want %q, got %q", test.want, got)
			}
		})
	}
}

func TestLegacyBacktickIndirectExpansion(t *testing.T) {
	src := "set -- one payload tail\n" +
		"n=1\n" +
		"capture() { printf '%s|%s|%s' \"$#\" \"$1\" \"$2\"; }\n" +
		"args=`capture echo \\\\$\\`expr $n + 1\\``\n" +
		"got=`eval echo \\\\$\\`expr $n + 1\\``\n" +
		"printf 'args=<%s> got=<%s> status=%s\\n' \"$args\" \"$got\" \"$?\"\n"
	file := parse(t, nil, src)
	var cb concBuffer
	r, err := interp.New(interp.StdIO(nil, &cb, &cb))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.WriteString(err.Error())
	}
	if got, want := cb.String(), "args=<2|echo|$2> got=<payload> status=0\n"; got != want {
		t.Fatalf("legacy nested backtick expansion: want %q, got %q", want, got)
	}
}

func TestRunnerCdStatErrorReason(t *testing.T) {
	t.Parallel()

	file := parse(t, nil, "cd longname")
	var cb concBuffer
	r, err := interp.New(
		interp.Dir(t.TempDir()),
		interp.StdIO(nil, &cb, &cb),
		interp.ExecHandlers(testExecHandler),
		interp.StatHandler(func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
			return nil, &fs.PathError{
				Op:   "stat",
				Path: name,
				Err:  errors.New("file name too long"),
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.WriteString(err.Error())
	}
	want := "cd: longname: File name too long\nexit status 1"
	if got := cb.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}

type epipeWriter struct{}

func (epipeWriter) Write([]byte) (int, error) {
	return 0, syscall.EPIPE
}

func TestPosixListingBuiltinsReportBrokenPipe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
	}{
		{"alias", "alias alias_epipe=true; command alias alias_epipe"},
		{"export", "export FOO=bar; command export -p"},
		{"readonly", "readonly RO=bar; command readonly -p"},
		{"set", "FOO=bar; command set"},
		{"hash", "hash -p /bin/sh sh; command hash"},
		{"times", "command times"},
		{"umask", "command umask"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file := parse(t, syntax.NewParser(), tc.src)
			var stderr bytes.Buffer
			r, err := interp.New(
				interp.StdIO(nil, epipeWriter{}, &stderr),
				interp.WithPosixMode(true),
			)
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			err = r.Run(ctx, file)
			code, ok := interp.IsExitStatus(err)
			if !ok || code == 0 {
				t.Fatalf("exit status = %v, %v; want non-zero", code, ok)
			}
			want := tc.name + ": write error: Broken pipe\n"
			if got := stderr.String(); got != want {
				t.Fatalf("stderr mismatch:\nwant: %q\ngot:  %q", want, got)
			}
		})
	}
}

func TestPosixQuotedEqualsCommandAfterAssignment(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("requires executing a script with '=' in its filename")
	}

	tdir := t.TempDir()
	cmdPath := filepath.Join(tdir, "var=753")
	if err := os.WriteFile(cmdPath, []byte("#!/bin/sh\necho var=753 executed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `PATH=.:$PATH
"var=753"
var'=753'
var\=753
x=y "var=753"
x=y var'=753'
x=y var\=753
`
	file := parse(t, syntax.NewParser(), src)
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.Dir(tdir),
		interp.StdIO(nil, &stdout, &stderr),
		interp.WithPosixMode(true),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	want := strings.Repeat("var=753 executed\n", 6)
	if got := stdout.String(); got != want {
		t.Fatalf("stdout mismatch:\nwant: %q\ngot:  %q", want, got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

// TestRestrictedReadonlyTempAssign checks that a restricted shell (-r),
// which freezes PATH/SHELL/ENV/BASH_ENV as readonly, follows bash 5.3 for
// assignments to those variables:
//
//   - A readonly-var assignment that PRECEDES a command word (a temporary /
//     assign_in_env assignment) prints the "readonly variable" error to
//     stderr but STILL RUNS the command with the unchanged value; the
//     command's own exit status stands (rc 0 for `echo`).
//   - A STANDALONE readonly assignment (no command) errors and aborts the
//     rest of the physical line in a non-interactive shell, exiting 1.
func TestRestrictedReadonlyTempAssign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		src        string
		wantStdout string
		wantStderr string
		wantCode   uint8
	}{
		{
			name:       "temp-assign PATH before command runs anyway",
			src:        "PATH=/x echo hi",
			wantStdout: "hi\n",
			wantStderr: "PATH: readonly variable\n",
			wantCode:   0,
		},
		{
			name:       "temp-assign SHELL before command runs anyway",
			src:        "SHELL=/x echo hi",
			wantStdout: "hi\n",
			wantStderr: "SHELL: readonly variable\n",
			wantCode:   0,
		},
		{
			name:       "export readonly errors but following command runs",
			src:        "export PATH=/x; echo done",
			wantStdout: "done\n",
			wantStderr: "PATH: readonly variable\n",
			wantCode:   0,
		},
		{
			name:       "standalone readonly assignment aborts the line",
			src:        "PATH=/x; echo after",
			wantStdout: "",
			wantStderr: "PATH: readonly variable\n",
			wantCode:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.src), "")
			qt.Assert(t, qt.IsNil(err))

			var stdout, stderr bytes.Buffer
			r, err := interp.New(interp.StdIO(nil, &stdout, &stderr), interp.Params("-r"))
			qt.Assert(t, qt.IsNil(err))

			var code uint8
			if err := r.Run(context.Background(), file); err != nil {
				if s, ok := interp.IsExitStatus(err); ok {
					code = s
				} else {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			qt.Check(t, qt.Equals(stdout.String(), tc.wantStdout))
			qt.Check(t, qt.Equals(stderr.String(), tc.wantStderr))
			qt.Check(t, qt.Equals(code, tc.wantCode))
		})
	}
}

func TestPosixSpecialBuiltinFuncDeclInSubshell(t *testing.T) {
	// POSIX interpretation 383: defining a function named after a special
	// builtin is a fatal error in posix mode (bash 5.3 general.c
	// valid_function_word, gated on posixly_correct). The whole subshell
	// aborts, so `echo after` never runs. bashy conforms to POSIX when posix
	// is indicated — see the FuncDecl handler in runner.go.
	src := "( set -o posix\n" +
		"break()\n" +
		"{\n" +
		"echo hi\n" +
		"}\n" +
		"echo after\n" +
		")\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./func5.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashCompatErrors(true))
	qt.Assert(t, qt.IsNil(err))

	// A special-builtin function definition aborts the subshell; the runner
	// surfaces it as a non-nil error and never prints "after".
	_ = r.Run(context.Background(), file)
	qt.Assert(t, qt.Equals(cb.String(), "./func5.sub: line 7: `break': is a special builtin\n"))
}

func TestFuncNameGatingBash53Final(t *testing.T) {
	t.Parallel()
	// Released bash 5.3 (general.c valid_function_word) rejects a function
	// name word carrying W_HASDOLLAR or W_QUOTED in the default mode:
	// `function sys$read`, `<(:) ()`, and `'a b c' ()` all fail with
	// "not a valid identifier" reported at the definition's closing brace,
	// while `function a=2` and `function 11111` define callable functions.
	// bash-5.3-rc2 still accepted the quoted and procsub-shaped names
	// ("allow quotes for now", W_HASDOLLAR only) and rc2's tests/func.right
	// encodes that abandoned behavior; the release reverted it
	// (W_HASDOLLAR|W_QUOTED, the old check tagged "/*TAG: bash-5.4 */").
	// This pins the released-5.3 semantics so a conformance run against an
	// rc2 fixture tree cannot masquerade as an engine regression again.
	src := "function a=2\n" +
		"{\n" +
		"\tprintf 'FUNCNAME: %s\\n' $FUNCNAME\n" +
		"}\n" +
		"function 11111\n" +
		"{\n" +
		"\tprintf 'FUNCNAME: %s\\n' $FUNCNAME\n" +
		"}\n" +
		"a\\=2\n" +
		"11111\n" +
		"function sys$read\n" +
		"{\n" +
		"\techo x\n" +
		"}\n" +
		"echo st:$?\n" +
		"<(:) ()\n" +
		"{\n" +
		"\techo $FUNCNAME\n" +
		"}\n" +
		"echo st2:$?\n" +
		"\\<\\(:\\)\n" +
		"type '<(:)'\n" +
		"echo st3:$?\n" +
		"'a b c' ()\n" +
		"{\n" +
		"\techo x\n" +
		"}\n" +
		"echo st4:$?\n" +
		"a\\ b\\ c\n" +
		"type 'a b c'\n" +
		"echo st5:$?\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./func5x.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashCompatErrors(true))
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	// Byte-identical to GNU bash 5.3.15(1)-release on the same script.
	want := "FUNCNAME: a=2\n" +
		"FUNCNAME: 11111\n" +
		"./func5x.sub: line 14: `sys$read': not a valid identifier\n" +
		"st:1\n" +
		"./func5x.sub: line 19: `<(:)': not a valid identifier\n" +
		"st2:1\n" +
		"./func5x.sub: line 21: <(:): command not found\n" +
		"./func5x.sub: line 22: type: <(:): not found\n" +
		"st3:1\n" +
		"./func5x.sub: line 27: `'a b c'': not a valid identifier\n" +
		"st4:1\n" +
		"./func5x.sub: line 29: a b c: command not found\n" +
		"./func5x.sub: line 30: type: a b c: not found\n" +
		"st5:1\n"
	qt.Assert(t, qt.Equals(cb.String(), want))
}

func TestFunctionRedirsApplyOnCallNotDefinition(t *testing.T) {
	src := "func(){ echo bad; } <_no_such_file_\n" +
		"echo def:$?\n" +
		"func\n" +
		"echo call:$?\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "func-redir")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashCompatErrors(true))
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.StringContains(cb.String(), "def:0\n"))
	qt.Assert(t, qt.StringContains(cb.String(), "_no_such_file_"))
	qt.Assert(t, qt.StringContains(cb.String(), "call:1\n"))
	qt.Assert(t, qt.IsFalse(strings.Contains(cb.String(), "bad\n")))
}

func TestBashCompatPosixSlashNamedFuncDeclExternalLookup(t *testing.T) {
	src := "set -o posix\n" +
		"a/b() { echo RAN; }\n" +
		"echo \"def-rc=$?\"\n" +
		"a/b 2>&1\n" +
		"echo \"exec-rc=$?\"\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(src), "SH")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.Dir(t.TempDir()), interp.StdIO(nil, &cb, &cb), interp.WithBashCompatErrors(true))
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(), "def-rc=0\nSH: line 4: a/b: No such file or directory\nexec-rc=127\n"))
}

func TestBashCompatPosixTempEnvFunctionCallRestore(t *testing.T) {
	src := "set -o posix\n" +
		"func() { return 5; }\n" +
		"myfunction() { var=20 return; }\n" +
		"var=20\n" +
		"myfunction\n" +
		"var=30 func\n" +
		"echo $? $var\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./func3.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &cb, &cb))
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(), "5 20\n"))
}

func TestBashPPVarenvTempAssignmentToFunc(t *testing.T) {
	src := "func() {\n" +
		"\tlocal YYZ\n" +
		"\tYYZ=\"song by rush\"\n" +
		"\techo $YYZ\n" +
		"\techo $A\n" +
		"}\n" +
		"YYZ=\"toronto airport\"\n" +
		"A=\"AVAR\"\n" +
		"echo $YYZ\n" +
		"echo $A\n" +
		"A=BVAR func\n" +
		"echo $YYZ\n" +
		"echo $A\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "./varenv.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &cb, &cb))
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(), "toronto airport\nAVAR\nsong by rush\nBVAR\ntoronto airport\nAVAR\n"))
}

func TestBashCompatPosixTempEnvSpecialBuiltinInFunctionPersists(t *testing.T) {
	src := "set -o posix\n" +
		"myfunction() { var=20 return; }\n" +
		"var=10\n" +
		"myfunction\n" +
		"echo $var\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./varenv12.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &cb, &cb))
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(), "20\n"))
}

func TestBashCompatMalformedLengthSubstitution(t *testing.T) {
	src := "echo ${#:}\n" +
		"echo ${#/}\n" +
		"echo ${#%}\n" +
		"echo ${#=}\n" +
		"echo ${#+}\n" +
		"echo ${#1xyz}\n" +
		"echo ${#:%}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./more-exp.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.Equals(err.Error(), "exit status 1"))
	qt.Assert(t, qt.Equals(cb.String(),
		"./more-exp.tests: line 1: ${#:}: bad substitution\n"+
			"./more-exp.tests: line 2: ${#/}: bad substitution\n"+
			"./more-exp.tests: line 3: ${#%}: bad substitution\n"+
			"./more-exp.tests: line 4: ${#=}: bad substitution\n"+
			"./more-exp.tests: line 5: ${#+}: bad substitution\n"+
			"./more-exp.tests: line 6: ${#1xyz}: bad substitution\n"+
			"./more-exp.tests: line 7: #: %: arithmetic syntax error: operand expected (error token is \"%\")\n"))
}

// Assigning to a positional or special parameter (`${1=x}`, `${@=x}`,
// `${!=x}`) is an error: bash prints the diagnostic, sets $? = 1, and
// discards the rest of the current physical line before resuming at the
// next line. With bash-compatible error reporting we match that, so the
// remaining statements on the erroring line are skipped while a later
// line still runs.
func TestBashCompatCannotAssignSpecialParam(t *testing.T) {
	src := "echo ${1=x} skipped; echo also-skipped\n" +
		"echo next-line ${#}\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./assign.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(),
		"./assign.tests: line 1: $1: cannot assign in this way\n"+
			"next-line 0\n"))
}

// A script read from stdin (File.Name=="") with bashSource set enables the
// stdin-source line-consumption feature (commands reading fd0 consume
// subsequent script lines, like bash). An explicit input redirect must win
// over it: `read x <<E` reads the heredoc body, not the next script line.
func TestStdinScriptInputRedirectBeatsLineConsumption(t *testing.T) {
	run := func(src string) string {
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		qt.Assert(t, qt.IsNil(err))
		var cb bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashSource([]byte(src)),
			interp.WithStdinScript(true))
		qt.Assert(t, qt.IsNil(err))
		_ = r.Run(context.Background(), file)
		return cb.String()
	}
	// heredoc body wins (regression: previously read/cat consumed later lines)
	qt.Assert(t, qt.Equals(run("read x <<E\nVAL\nE\necho \"[$x]\"\n"), "[VAL]\n"))
	// here-string too
	qt.Assert(t, qt.Equals(run("read x <<<hello\necho \"[$x]\"\n"), "[hello]\n"))
	// but read WITHOUT a redirect still consumes the next script line (the bash
	// stdin-script quirk): it eats `echo …`, so nothing prints.
	qt.Assert(t, qt.Equals(run("read x\necho \"[$x]\"\n"), ""))
}

func TestBashSourceTopLevelAndNounsetDefault(t *testing.T) {
	run := func(name, src string) string {
		t.Helper()
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), name)
		qt.Assert(t, qt.IsNil(err))
		var cb bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.Params("-u"))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNil(r.Run(context.Background(), file)))
		return cb.String()
	}

	// Critical regression: a file-backed main script must have BASH_SOURCE[0]
	// set to the script path at top level, matching Bash 3.2/5.x.
	t.Run("file-backed-absolute", func(t *testing.T) {
		src := `printf 'top=<%s> n=%s\n' "${BASH_SOURCE[0]:-default}" "${#BASH_SOURCE[@]}"
f() { printf 'func=<%s>|<%s> n=%s\n' "${BASH_SOURCE[0]:-default}" "${BASH_SOURCE[1]:-default}" "${#BASH_SOURCE[@]}"; }
f
`
		qt.Assert(t, qt.Equals(run("/work/script.sh", src),
			"top=</work/script.sh> n=1\nfunc=</work/script.sh>|</work/script.sh> n=2\n"))
	})

	// Relative parse names must also populate BASH_SOURCE[0].
	t.Run("file-backed-relative", func(t *testing.T) {
		qt.Assert(t, qt.Equals(
			run("./run.sh", `printf '<%s>\n' "${BASH_SOURCE[0]}"`),
			"<./run.sh>\n"))
	})

	// Scalar $BASH_SOURCE (no index) must expand to BASH_SOURCE[0].
	t.Run("scalar-expansion", func(t *testing.T) {
		qt.Assert(t, qt.Equals(
			run("/app/main.sh", `printf '<%s>\n' "$BASH_SOURCE"`),
			"</app/main.sh>\n"))
	})

	// Nested function calls: BASH_SOURCE grows with each frame.
	t.Run("nested-functions", func(t *testing.T) {
		src := `
outer() { inner; }
inner() { printf '%s\n' "${#BASH_SOURCE[@]}" "${BASH_SOURCE[@]}"; }
outer
`
		qt.Assert(t, qt.Equals(run("/x.sh", src),
			"3\n/x.sh\n/x.sh\n/x.sh\n"))
	})

	// bash -c and stdin have no BASH_SOURCE element; the indexed default is
	// nevertheless nounset-safe and expands rather than panicking.
	t.Run("stdin-empty", func(t *testing.T) {
		qt.Assert(t, qt.Equals(run("", `printf '<%s> n=%s\n' "${BASH_SOURCE[0]:-default}" "${#BASH_SOURCE[@]}"`),
			"<default> n=0\n"))
	})

	// CommandString mode (-c): no top-level BASH_SOURCE; inside a function
	// it carries the argv0 name.
	t.Run("command-string", func(t *testing.T) {
		commandSrc := `printf '<%s> n=%s\n' "${BASH_SOURCE[0]:-default}" "${#BASH_SOURCE[@]}"; f() { printf 'f=<%s> n=%s argv0=<%s>\n' "${BASH_SOURCE[0]:-default}" "${#BASH_SOURCE[@]}" "$0"; }; f`
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(commandSrc), "bash")
		qt.Assert(t, qt.IsNil(err))
		var cb bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.Params("-u"), interp.CommandString(true))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNil(r.Run(context.Background(), file)))
		qt.Assert(t, qt.Equals(cb.String(), "<default> n=0\nf=<bash> n=1 argv0=<bash>\n"))
	})
}

// builtinTargetQuoted scans the original source line — only available when
// WithBashSource is set — to decide whether a read/printf/wait target token
// was quoted. When a target ends a physical line with a backslash (a line
// continuation, outside or inside double quotes) the escape scan must not
// advance past the end of the line. It previously did `i += 2`, slicing
// `line[start:i]` out of bounds and panicking; e.g. `read -r x \` on the
// terminal physical line panicked while bash printed the read value.
func TestBashSourceTargetQuotedTerminalBackslash(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			// terminal backslash-newline outside quotes (line continuation).
			name: "unquoted",
			src:  "IFS= read -r x \\\n<<< hi\nprintf '%s\\n' \"$x\"\n",
			want: "hi\n",
		},
		{
			// terminal backslash-newline inside double quotes.
			name: "double-quoted",
			src:  "read -r first \"sec\\\nond\" <<< 'one two'\nprintf '%s|%s\\n' \"$first\" \"$second\"\n",
			want: "one|two\n",
		},
	}
	run := func(src string) string {
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "./read.tests")
		qt.Assert(t, qt.IsNil(err))
		var cb bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashSource([]byte(src)))
		qt.Assert(t, qt.IsNil(err))
		if err := r.Run(context.Background(), file); err != nil {
			cb.WriteString(err.Error())
		}
		return cb.String()
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qt.Assert(t, qt.Equals(run(tc.src), tc.want))
		})
	}
}

func TestBashCompatUnsetParameterCustomMessage(t *testing.T) {
	src := "recho ${ABXD:?\"parameter unset\"}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./new-exp.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.ErrorMatches(err, "exit status 1"))
	qt.Assert(t, qt.Equals(cb.String(),
		"./new-exp.tests: line 1: ABXD: parameter unset\n"))
}

func TestBashCompatEvalArithOperandError(t *testing.T) {
	// Bash defers arithmetic parsing to expansion time, so an
	// operator-first expression inside eval'd $((...)) / $[...]
	// reports through the arithmetic evaluator: no `eval:` tag, no
	// source echo (errors.tests lines 286-287).
	src := "eval echo \\$\\[/bin/sh + 0\\]\n" +
		"eval echo '$((/bin/sh + 0))'\n" +
		"true\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./errors.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(),
		"./errors.tests: line 1: /bin/sh + 0: arithmetic syntax error: operand expected (error token is \"/bin/sh + 0\")\n"+
			"./errors.tests: line 2: /bin/sh + 0: arithmetic syntax error: operand expected (error token is \"/bin/sh + 0\")\n"))
}

func TestBashCompatEvalUnclosedParenLineRebase(t *testing.T) {
	// The parser stamps "from `(' command on line N" with the line
	// inside the eval'd string; bash counts from the top of the
	// enclosing script (errors8.sub line 6).
	src := "\n\n\n\n\neval '( '\ntrue\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./errors8.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(),
		"./errors8.sub: eval: line 7: syntax error: unexpected end of file from `(' command on line 6\n"))
}

func TestBashCompatTestVarSetSplitArrayRef(t *testing.T) {
	src := "key='$(echo foo)'\n" +
		"test -v aa[$key]\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./quotearray1.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.ErrorMatches(err, "exit status 2"))
	qt.Assert(t, qt.Equals(cb.String(),
		"./quotearray1.sub: line 2: test: aa[$(echo: binary operator expected\n"))
}

func TestBashCompatUnsetQuotedArrayOperands(t *testing.T) {
	src := `declare -A a
key='$(echo foo)'
a[$key]=1
unset 'a[$key]'
declare -p a
a[$key]=1
unset a[$key]
declare -p a
unset a
declare -A a
a['$key']=2
unset "a['\$key']"
declare -p a
declare -A a=(@ v0 . v1)
key=@
unset 'a[$key]'
declare -p a
declare -A assoc
assoc['$var']=value
var=x123
shopt -s assoc_expand_once
unset 'assoc[$var]'
declare -p assoc
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./quotearray5.sub")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(),
		"declare -A a=()\n"+
			"declare -A a=([\"\\$(echo foo)\"]=\"1\" )\n"+
			"declare -A a=()\n"+
			"declare -A a=([.]=\"v1\" )\n"+
			"declare -A assoc=()\n"))
}

func TestBashCompatExecInvalidOptionUsage(t *testing.T) {
	src := "exec -1</dev/null\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./redir.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.ErrorMatches(err, "exit status 2"))
	qt.Assert(t, qt.Equals(cb.String(),
		"./redir.tests: line 1: exec: -1: invalid option\n"+
			"exec: usage: exec [-cl] [-a name] [command [argument ...]] [redirection ...]\n"))
}

func TestBashCompatReadonlyAssignErrexitExits(t *testing.T) {
	// A standalone assignment to a readonly variable is fatal under
	// `set -e`: bash prints the diagnostic and exits the shell without
	// running the next command (builtin-vars.test.sh). Without errexit
	// the same assignment only discards the rest of the physical line
	// and execution continues.
	t.Run("errexit-exits", func(t *testing.T) {
		src := "set -o errexit\nreadonly foo=bar\nfoo=eggs\necho \"status=$?\"\n"
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./vars.tests")
		qt.Assert(t, qt.IsNil(err))
		var cb bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashCompatErrors(true), interp.WithBashSource([]byte(src)))
		qt.Assert(t, qt.IsNil(err))
		err = r.Run(context.Background(), file)
		qt.Assert(t, qt.ErrorMatches(err, "exit status 1"))
		qt.Assert(t, qt.Equals(cb.String(), "./vars.tests: line 3: foo: readonly variable\n"))
	})
	t.Run("no-errexit-continues", func(t *testing.T) {
		src := "readonly foo=bar\nfoo=eggs\necho next\n"
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./vars.tests")
		qt.Assert(t, qt.IsNil(err))
		var cb bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &cb, &cb), interp.WithBashCompatErrors(true), interp.WithBashSource([]byte(src)))
		qt.Assert(t, qt.IsNil(err))
		err = r.Run(context.Background(), file)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(cb.String(), "./vars.tests: line 2: foo: readonly variable\nnext\n"))
	})
}

func TestBashCompatShoptLongOptionInvalid(t *testing.T) {
	// bash's `shopt` getopt only accepts -p/-q/-s/-u/-o (and a bare
	// `--` to end options). Any `--xxx` long form (`--set`, `--unset`,
	// `--foo`) is rejected as `shopt: --: invalid option` with usage
	// (arith.test.sh `shopt --set strict_arith`). A lone `--` is still
	// the valid end-of-options marker.
	src := "shopt --\nshopt --set strict_arith\nshopt -- extglob\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./arith.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	// The last command (`shopt -- extglob`, an unset query) exits 1.
	qt.Assert(t, qt.ErrorMatches(err, "exit status 1"))
	qt.Assert(t, qt.Equals(cb.String(),
		"./arith.tests: line 2: shopt: --: invalid option\n"+
			"shopt: usage: shopt [-pqsu] [-o] [optname ...]\n"+
			"extglob             \toff\n"))
}

func TestBashCompatUnsetInvalidPathName(t *testing.T) {
	src := "unset /bin/sh\nunset invalid-name\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./errors.tests")
	qt.Assert(t, qt.IsNil(err))

	var cb bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashCompatErrors(true),
		interp.WithBashSource([]byte(src)),
	)
	qt.Assert(t, qt.IsNil(err))

	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(cb.String(), ""))
}

func readLines(hc interp.HandlerContext) ([][]byte, error) {
	bs, err := io.ReadAll(hc.Stdin)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		bs = bytes.ReplaceAll(bs, []byte("\r\n"), []byte("\n"))
	}
	bs = bytes.TrimSuffix(bs, []byte("\n"))
	return bytes.Split(bs, []byte("\n")), nil
}

func absPath(dir, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path) // TODO: this clean is likely unnecessary
}

var testBuiltinsMap = map[string]func(interp.HandlerContext, []string) error{
	"cat": func(hc interp.HandlerContext, args []string) error {
		if len(args) == 0 {
			if hc.Stdin == nil || hc.Stdout == nil {
				return nil
			}
			_, err := io.Copy(hc.Stdout, hc.Stdin)
			return err
		}
		for _, arg := range args {
			path := absPath(hc.Dir, arg)
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(hc.Stdout, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	},
	"wc": func(hc interp.HandlerContext, args []string) error {
		bs, err := io.ReadAll(hc.Stdin)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			fmt.Fprintf(hc.Stdout, "%7d", bytes.Count(bs, []byte("\n")))
			fmt.Fprintf(hc.Stdout, "%8d", len(bytes.Fields(bs)))
			fmt.Fprintf(hc.Stdout, "%8d\n", len(bs))
		} else if args[0] == "-c" {
			fmt.Fprintln(hc.Stdout, len(bs))
		} else if args[0] == "-l" {
			fmt.Fprintln(hc.Stdout, bytes.Count(bs, []byte("\n")))
		}
		return nil
	},
	"head": func(hc interp.HandlerContext, args []string) error {
		limit := 10
		if len(args) == 1 && strings.HasPrefix(args[0], "-") {
			if _, err := fmt.Sscanf(args[0], "-%d", &limit); err != nil {
				return err
			}
		} else if len(args) != 0 {
			return fmt.Errorf("unexpected arg: %q", args[0])
		}
		lines, err := readLines(hc)
		if err != nil {
			return err
		}
		for i, line := range lines {
			if i >= limit {
				break
			}
			fmt.Fprintf(hc.Stdout, "%s\n", line)
		}
		return nil
	},
	"sh": func(hc interp.HandlerContext, args []string) error {
		if len(args) != 2 || args[0] != "-c" {
			return fmt.Errorf("unexpected args: %q", args)
		}
		file, err := syntax.NewParser().Parse(strings.NewReader(args[1]), "")
		if err != nil {
			return err
		}
		r, err := interp.New(
			interp.Dir(hc.Dir),
			interp.StdIO(hc.Stdin, hc.Stdout, hc.Stderr),
		)
		if err != nil {
			return err
		}
		return r.Run(context.Background(), file)
	},
	"tr": func(hc interp.HandlerContext, args []string) error {
		if len(args) != 2 || len(args[1]) != 1 {
			return fmt.Errorf("usage: tr [-s -d] [character]")
		}
		squeeze := args[0] == "-s"
		char := args[1][0]
		bs, err := io.ReadAll(hc.Stdin)
		if err != nil {
			return err
		}
		for {
			i := bytes.IndexByte(bs, char)
			if i < 0 {
				hc.Stdout.Write(bs) // remaining
				break
			}
			hc.Stdout.Write(bs[:i]) // up to char
			bs = bs[i+1:]

			bs = bytes.TrimLeft(bs, string(char)) // remove repeats
			if squeeze {
				hc.Stdout.Write([]byte{char})
			}
		}
		return nil
	},
	"sort": func(hc interp.HandlerContext, args []string) error {
		lines, err := readLines(hc)
		if err != nil {
			return err
		}
		slices.SortFunc(lines, bytes.Compare)
		for _, line := range lines {
			fmt.Fprintf(hc.Stdout, "%s\n", line)
		}
		return nil
	},
	"grep": func(hc interp.HandlerContext, args []string) error {
		var rx *regexp.Regexp
		quiet := false
		caseInsensitive := false
		for _, arg := range args {
			if arg == "-q" {
				quiet = true
			} else if arg == "-i" {
				caseInsensitive = true
			} else if arg == "-E" {
			} else if rx == nil {
				if caseInsensitive {
					arg = "(?i)" + arg
				}
				rx = regexp.MustCompile(arg)
			} else {
				return fmt.Errorf("unexpected arg: %q", arg)
			}
		}
		lines, err := readLines(hc)
		if err != nil {
			return err
		}
		anyMatch := false
		for _, line := range lines {
			if rx.Match(line) {
				if quiet {
					return nil
				}
				anyMatch = true
				fmt.Fprintf(hc.Stdout, "%s\n", line)
			}
		}
		if !anyMatch {
			return interp.ExitStatus(1)
		}
		return nil
	},
	"sed": func(hc interp.HandlerContext, args []string) error {
		f := hc.Stdin
		switch len(args) {
		case 1:
		case 2:
			var err error
			f, err = os.Open(absPath(hc.Dir, args[1]))
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("usage: sed pattern [file]")
		}
		expr := args[0]
		if expr == "" || expr[0] != 's' {
			return fmt.Errorf("unimplemented")
		}
		sep := expr[1]
		expr = expr[2:]
		from := expr[:strings.IndexByte(expr, sep)]
		expr = expr[len(from)+1:]
		to := expr[:strings.IndexByte(expr, sep)]
		bs, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		rx := regexp.MustCompile(from)
		bs = rx.ReplaceAllLiteral(bs, []byte(to))
		_, err = hc.Stdout.Write(bs)
		return err
	},
	"mkdir": func(hc interp.HandlerContext, args []string) error {
		for _, arg := range args {
			if arg == "-p" {
				continue
			}
			path := absPath(hc.Dir, arg)
			if err := os.MkdirAll(path, 0o777); err != nil {
				return err
			}
		}
		return nil
	},
	"rm": func(hc interp.HandlerContext, args []string) error {
		for _, arg := range args {
			if arg == "-r" {
				continue
			}
			path := absPath(hc.Dir, arg)
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
		return nil
	},
	"ln": func(hc interp.HandlerContext, args []string) error {
		symbolic := args[0] == "-s"
		if symbolic {
			args = args[1:]
		}
		oldname := absPath(hc.Dir, args[0])
		newname := absPath(hc.Dir, args[1])
		if symbolic {
			return os.Symlink(oldname, newname)
		}
		return os.Link(oldname, newname)
	},
	"touch": func(hc interp.HandlerContext, args []string) error {
		filenames := args // create all arguments as filenames

		newTime := time.Now()
		if args[0] == "-t" {
			if len(args) < 3 {
				return fmt.Errorf("usage: touch [-t [[CC]YY]MMDDhhmm[.SS]] file")
			}
			filenames = args[2:] // treat the rest of the args as filenames

			arg := args[1]
			if len(arg) > 15 {
				return fmt.Errorf("usage: touch [-t [[CC]YY]MMDDhhmm[.SS]] file")
			}
			s, err := time.Parse("200601021504.05", arg)
			if err != nil {
				return err
			}
			newTime = s
		}

		for _, arg := range filenames {
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("usage: touch [-t [[CC]YY]MMDDhhmm[.SS]] file")
			}
			path := absPath(hc.Dir, arg)
			// create the file if it does not exist
			f, err := os.OpenFile(path, os.O_CREATE, 0o666)
			if err != nil {
				return err
			}
			f.Close()
			// change the modification and access time
			if err := os.Chtimes(path, newTime, newTime); err != nil {
				return err
			}
		}
		return nil
	},
	"sleep": func(hc interp.HandlerContext, args []string) error {
		for _, arg := range args {
			// assume and default unit to be in seconds
			d, err := time.ParseDuration(fmt.Sprintf("%ss", arg))
			if err != nil {
				return err
			}
			time.Sleep(d)
		}
		return nil
	},
}

func testExecHandler(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if fn := testBuiltinsMap[args[0]]; fn != nil {
			return fn(interp.HandlerCtx(ctx), args[1:])
		}
		return next(ctx, args)
	}
}

// Same as the syntax package.
var requireShells = os.Getenv("REQUIRE_SHELLS") == "1"

func TestRunnerRunConfirm(t *testing.T) {
	if testing.Short() {
		t.Skip("calling bash is slow")
	}
	if gnuBash53 == "" {
		if requireShells {
			t.Fatal("GNU bash 5.3 required to run")
		} else {
			t.Skip("GNU bash 5.3 required to run")
		}
	}
	t.Parallel()

	if runtime.GOOS == "windows" {
		// For example, it seems to treat environment variables as
		// case-sensitive, which isn't how Windows works.
		t.Skip("bash on Windows emulates Unix-y behavior")
	}
	t.Logf("GNU bash 5.3 comparator: %s (%s)", gnuBash53, strings.TrimSpace(bashVersion(gnuBash53)))
	for i, c := range runTests {
		t.Run(fmt.Sprintf("%04d", i), func(t *testing.T) {
			if strings.Contains(c.want, " #IGNORE") {
				return
			}
			skipIfUnsupported(t, c.in)
			t.Parallel()
			tdir := t.TempDir()
			if strings.Contains(c.in, "strmatch.so") {
				buildStrmatchLoadable(t, tdir)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, gnuBash53)
			cmd.Args[0] = "bash"
			cmd.Dir = tdir
			cmd.Env = bashConfirmEnv(t, tdir, c.in)
			cmd.Stdin = strings.NewReader(c.in)
			out, err := cmd.CombinedOutput()
			if strings.Contains(c.want, " #JUSTERR") {
				// bash sometimes exits with status code 0 and
				// stderr "bash: ..." for an error
				fauxErr := bytes.HasPrefix(out, []byte("bash:"))
				if err == nil && !fauxErr {
					t.Fatalf("wanted bash to error in %q", c.in)
				}
				return
			}
			got := string(out)
			if err != nil {
				got += err.Error()
			}
			want := c.want
			if i == 565 {
				version := exec.Command(gnuBash53, "-c", "set -o posix; readonly -a | grep '^readonly -a BASH_VERSINFO='")
				version.Env = cmd.Env
				out, err := version.CombinedOutput()
				if err != nil {
					t.Fatal(err)
				}
				want = string(out) + "readonly -a arr=([0]=\"x\")\n"
			}
			if i == 674 {
				physicalDir, err := filepath.EvalSymlinks(tdir)
				if err != nil {
					t.Fatal(err)
				}
				want = strings.ReplaceAll(want, "ABS_PATH_D", filepath.Join(physicalDir, "d"))
			}
			if i == 1403 {
				for _, entry := range cmd.Env {
					if pathList, ok := strings.CutPrefix(entry, "PATH="); ok {
						want = typeEchoKinds(pathList)
						break
					}
				}
			}
			if got != want {
				t.Fatalf("wrong bash output in %q:\nwant: %q\ngot:  %q",
					c.in, want, got)
			}
		})
	}
}

func bashConfirmEnv(t *testing.T, tdir, src string) []string {
	t.Helper()
	path := strings.Join([]string{"/bin", "/usr/bin", "/usr/sbin", "/sbin", "/opt/homebrew/bin", "/usr/local/bin"}, string(os.PathListSeparator))
	env := []string{
		"HOME=" + bashConfirmHome(t, tdir, src),
		"PATH=" + path,
		"SHELL=" + gnuBash53,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"BASH_ENV=",
		"ENV=",
		"ENV_PROG=env",
		"PATH_PROG=sh",
		"GO_TEST_DIR=" + os.Getenv("GO_TEST_DIR"),
		"GOSH_PROG=" + os.Getenv("GOSH_PROG"),
		"INTERP_GLOBAL=value",
		"MULTILINE_INTERP_GLOBAL=\nwith\nnewlines\n\n",
	}
	if runtime.GOOS == "windows" {
		env = append(env, "mixedCase_INTERP_GLOBAL=value")
	} else {
		env = append(env, "MIXEDCASE_INTERP_GLOBAL=value")
	}
	return env
}

func bashConfirmHome(t *testing.T, tdir, src string) string {
	t.Helper()
	if !strings.HasPrefix(src, "HOME=") {
		return tdir
	}
	value, _, _ := strings.Cut(strings.TrimPrefix(src, "HOME="), ";")
	value, _, _ = strings.Cut(value, "\n")
	value = strings.TrimSpace(value)
	if value == "$PWD/home" {
		return filepath.Join(tdir, "home")
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		value = unquoted
	} else {
		value = strings.Trim(value, `"'`)
	}
	if filepath.IsAbs(value) {
		return value
	}
	return tdir
}

func bashVersion(path string) string {
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return err.Error()
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return line
}

func buildStrmatchLoadable(t *testing.T, dir string) {
	t.Helper()
	header := filepath.Join(filepath.Dir(filepath.Dir(gnuBash53)), "include", "bash", "builtins", "loadables.h")
	if _, err := os.Stat(header); err != nil {
		if requireShells {
			t.Fatalf("GNU bash loadable headers required for strmatch.so: %v", err)
		}
		t.Skipf("GNU bash loadable headers required for strmatch.so: %v", err)
	}
	src := filepath.Join(dir, "strmatch.c")
	if err := os.WriteFile(src, []byte(strmatchLoadableSource), 0o666); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"-I" + filepath.Join(filepath.Dir(filepath.Dir(gnuBash53)), "include", "bash"),
		"-I" + filepath.Join(filepath.Dir(filepath.Dir(gnuBash53)), "include", "bash", "include"),
		"-o", filepath.Join(dir, "strmatch.so"),
		src,
	}
	if runtime.GOOS == "darwin" {
		args = append(args[:2], append([]string{"-dynamiclib", "-undefined", "dynamic_lookup"}, args[2:]...)...)
	} else {
		args = append(args[:2], append([]string{"-shared", "-fPIC"}, args[2:]...)...)
	}
	cmd := exec.Command("cc", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if requireShells {
			t.Fatalf("building strmatch.so: %v\n%s", err, out)
		}
		t.Skipf("building strmatch.so: %v\n%s", err, out)
	}
}

const strmatchLoadableSource = `
#include <fnmatch.h>
#include <stddef.h>
#include <string.h>
#include "builtins/loadables.h"

static char *strmatch_doc[] = {
	"strmatch STRING PATTERN",
	"Return success if PATTERN matches STRING using pathname glob rules.",
	(char *)NULL
};

int strmatch_builtin(WORD_LIST *list) {
	char *str, *pat;
	if (list == 0 || list->word == 0 || list->next == 0 || list->next->word == 0) {
		builtin_usage();
		return EX_USAGE;
	}
	str = list->word->word;
	pat = list->next->word->word;
	if (strcmp(str, pat) == 0) {
		return EXECUTION_SUCCESS;
	}
	return fnmatch(pat, str, FNM_PATHNAME) == 0 ? EXECUTION_SUCCESS : EXECUTION_FAILURE;
}

struct builtin strmatch_struct = {
	"strmatch",
	strmatch_builtin,
	BUILTIN_ENABLED,
	strmatch_doc,
	"strmatch STRING PATTERN",
	0
};
`

func TestRunnerOpts(t *testing.T) {
	t.Parallel()

	withPath := func(strs ...string) func(*interp.Runner) error {
		prefix := []string{
			"PATH=" + os.Getenv("PATH"),
			"ENV_PROG=" + os.Getenv("ENV_PROG"),
		}
		return interp.Env(expand.ListEnviron(append(prefix, strs...)...))
	}
	opts := func(list ...interp.RunnerOption) []interp.RunnerOption {
		return list
	}
	cases := []struct {
		opts     []interp.RunnerOption
		in, want string
	}{
		{
			nil,
			"$ENV_PROG | grep -i '^interp_global='",
			"INTERP_GLOBAL=value\n",
		},
		{
			opts(withPath()),
			"$ENV_PROG | grep -i '^interp_global='",
			"exit status 1",
		},
		{
			opts(withPath("INTERP_GLOBAL=bar")),
			"$ENV_PROG | grep -i '^interp_global='",
			"INTERP_GLOBAL=bar\n",
		},
		{
			opts(withPath("a=b")),
			"echo $a",
			"b\n",
		},
		{
			opts(withPath("A=b")),
			"$ENV_PROG | grep '^A='; echo $A",
			"A=b\nb\n",
		},
		{
			opts(withPath("A=b", "A=c")),
			"$ENV_PROG | grep '^A='; echo $A",
			"A=c\nc\n",
		},
		{
			opts(withPath("HOME=")),
			"echo $HOME",
			"\n",
		},
		{
			opts(withPath("PWD=foo")),
			"[[ $PWD == foo ]]",
			"exit status 1",
		},
		{
			opts(interp.Params("foo")),
			"echo $@",
			"foo\n",
		},
		{
			opts(interp.Params("-u", "--", "foo")),
			"echo $@; echo $unset",
			"foo\nunset: unbound variable\nexit status 1",
		},
		{
			opts(interp.Params("-u", "--", "foo")),
			"echo $@; echo ${unset:-default}",
			"foo\ndefault\n",
		},
		{
			opts(interp.Params("foo")),
			"set >/dev/null; echo $@",
			"foo\n",
		},
		{
			opts(interp.Params("foo")),
			"set -e; echo $@",
			"foo\n",
		},
		{
			opts(interp.Params("foo")),
			"set --; echo $@",
			"\n",
		},
		{
			opts(interp.Params("foo")),
			"set bar; echo $@",
			"bar\n",
		},
		{
			opts(interp.Env(expand.FuncEnviron(func(name string) string {
				if name == "foo" {
					return "bar"
				}
				return ""
			}))),
			"(echo $foo); echo x | echo $foo",
			"bar\nbar\n",
		},
	}
	p := syntax.NewParser()
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			skipIfUnsupported(t, c.in)
			file := parse(t, p, c.in)
			var cb concBuffer
			r, err := interp.New(append(c.opts,
				interp.StdIO(nil, &cb, &cb),
				interp.ExecHandlers(testExecHandler),
			)...)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.WriteString(err.Error())
			}
			if got := cb.String(); got != c.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q",
					c.in, c.want, got)
			}
		})
	}
}

func TestRunnerContext(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"while true; do true; done",
		"until false; do true; done",
		"sleep 1000",
		"while true; do true; done & wait",
		"sleep 1000 & wait",
		"(while true; do true; done)",
		"$(while true; do true; done)",
		"while true; do true; done | while true; do true; done",
	}
	p := syntax.NewParser()
	for _, in := range cases {
		t.Run("", func(t *testing.T) {
			file := parse(t, p, in)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r, _ := interp.New()
			errChan := make(chan error)
			go func() {
				errChan <- r.Run(ctx, file)
			}()

			timeout := 500 * time.Millisecond
			select {
			case err := <-errChan:
				if err != nil && err != ctx.Err() {
					t.Fatal("Runner did not use ctx.Err()")
				}
			case <-time.After(timeout):
				t.Fatalf("program was not killed in %s", timeout)
			}
		})
	}
}

func TestCancelBlockedStdinRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		// TODO: Why is this? The [os.File.SetReadDeadline] docs seem to imply that it should work
		// across all major platforms, and the file polling  implementation seems to be
		// for all posix platforms including Windows.
		// Our previous logic and tests with muesli/cancelreader did not test an os.Pipe
		// on Windows either, so skipping here is not any worse.
		t.Skip("os.Pipe on windows appears to not support cancellable reads")
	}
	t.Parallel()

	p := syntax.NewParser()
	file := parse(t, p, "read x")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	// Make the linter happy, even though we deliberately wait for the timeout.
	defer cancel()

	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("Error calling os.Pipe: %v", err)
	}
	defer func() {
		stdinWrite.Close()
		stdinRead.Close()
	}()
	r, _ := interp.New(interp.StdIO(stdinRead, nil, nil))
	now := time.Now()
	errChan := make(chan error)
	go func() {
		errChan <- r.Run(ctx, file)
	}()

	timeout := 500 * time.Millisecond
	select {
	case err := <-errChan:
		if err == nil || err.Error() != "exit status 1" || ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("'read x' did not timeout correctly; err: %v, ctx.Err(): %v; dur: %v",
				err, ctx.Err(), time.Since(now))
		}
	case <-time.After(timeout):
		t.Fatalf("program was not killed in %s", timeout)
	}
}

func TestReadTimeoutBlockedStdin(t *testing.T) {
	t.Parallel()

	file := parse(t, nil, "read -t 0.000001 x")
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()

	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("Error calling os.Pipe: %v", err)
	}
	defer func() {
		stdinWrite.Close()
		stdinRead.Close()
	}()

	r, _ := interp.New(interp.StdIO(stdinRead, nil, nil))
	now := time.Now()
	err = r.Run(ctx, file)
	if err == nil || err.Error() != "exit status 142" {
		t.Fatalf("read -t did not return timeout status 142; err: %v", err)
	}
	if dur := time.Since(now); dur > time.Second {
		t.Fatalf("read -t took too long to time out: %v", dur)
	}
}

func TestReadTimeoutFromFdWithoutDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mkfifo is not available on windows")
	}
	t.Parallel()

	file := parse(t, nil, `
mkfifo p
{ printf 'from-fd\n'; sleep 0.2; } >p &
exec 9<p
read -u 9 -t 0.1 x
printf '%s:%s\n' "$?" "$x"
read -u 9 -t 0.001 y
printf '%s:%s\n' "$?" "${y:-empty}"
wait
`)
	var stdout bytes.Buffer
	r, _ := interp.New(
		interp.Dir(t.TempDir()),
		interp.StdIO(strings.NewReader("from-stdin\n"), &stdout, nil),
	)
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	start := time.Now()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "0:from-fd\n142:empty\n"; got != want {
		t.Fatalf("read -u with timeout read the wrong input:\nwant: %q\ngot:  %q", want, got)
	}
	if dur := time.Since(start); dur > time.Second {
		t.Fatalf("read -u with timeout took too long: %v", dur)
	}
}

func TestRunnerAltNodes(t *testing.T) {
	t.Parallel()

	in := "echo foo"
	file := parse(t, nil, in)
	want := "foo\n"
	nodes := []syntax.Node{
		file,
		file.Stmts[0],
		file.Stmts[0].Cmd,
	}
	for _, node := range nodes {
		var cb concBuffer
		r, _ := interp.New(interp.StdIO(nil, &cb, &cb))
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		defer cancel()
		if err := r.Run(ctx, node); err != nil {
			cb.WriteString(err.Error())
		}
		if got := cb.String(); got != want {
			t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q",
				in, want, got)
		}
	}
}

func TestRunnerDir(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Missing", func(t *testing.T) {
		_, err := interp.New(interp.Dir("missing"))
		if err == nil {
			t.Fatal("expected New to error when Dir is missing")
		}
	})
	t.Run("NotDir", func(t *testing.T) {
		_, err := interp.New(interp.Dir("interp_test.go"))
		if err == nil {
			t.Fatal("expected New to error when Dir is not a dir")
		}
	})
	t.Run("NotDirAbs", func(t *testing.T) {
		_, err := interp.New(interp.Dir(filepath.Join(wd, "interp_test.go")))
		if err == nil {
			t.Fatal("expected New to error when Dir is not a dir")
		}
	})
	t.Run("Relative", func(t *testing.T) {
		// On Windows, it's impossible to make a relative path from one
		// drive to another. Use the parent directory, as that's for
		// sure in the same drive as the current directory.
		rel := ".." + string(filepath.Separator)
		r, err := interp.New(interp.Dir(rel))
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(r.Dir) {
			t.Errorf("Runner.Dir is not absolute")
		}
	})
	// Ensure that we treat symlinks and short paths properly, especially
	// with Dir and globbing.
	t.Run("SymlinkOrShortPath", func(t *testing.T) {
		tdir := t.TempDir()

		realDir := filepath.Join(tdir, "real-long-dir-name")
		realFile := filepath.Join(realDir, "realfile")

		if err := os.Mkdir(realDir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realFile, []byte(""), 0o666); err != nil {
			t.Fatal(err)
		}

		var altDir string
		if runtime.GOOS == "windows" {
			short, err := shortPathName(realDir)
			if err != nil {
				t.Fatal(err)
			}
			altDir = short
			// We replace tdir later, and it might have been shortened.
			tdir = filepath.Dir(altDir)
		} else {
			altDir = filepath.Join(tdir, "symlink")
			if err := os.Symlink(realDir, altDir); err != nil {
				t.Fatal(err)
			}
		}

		var b bytes.Buffer
		r, err := interp.New(interp.Dir(altDir), interp.StdIO(nil, &b, &b))
		if err != nil {
			t.Fatal(err)
		}
		file := parse(t, nil, "echo $PWD $PWD/*")
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		defer cancel()
		if err := r.Run(ctx, file); err != nil {
			t.Fatal(err)
		}
		got := b.String()
		got = strings.ReplaceAll(got, tdir, "")
		got = strings.TrimSpace(got)
		want := `/symlink /symlink/realfile`
		if runtime.GOOS == "windows" {
			// Glob words keep `/` (Sprint 245); $PWD itself is in the
			// shell's own spelling.
			want = `REAL.{4} .*REAL.{4}/realfile$`
		}
		if !regexp.MustCompile(want).MatchString(got) {
			t.Fatalf("\nwant regexp: %q\ngot: %q", want, got)
		}
	})
}

func TestRunnerIncremental(t *testing.T) {
	t.Parallel()

	file := parse(t, nil, "echo foo; false; echo bar; exit 0; echo baz")
	want := "foo\nbar\n"
	var b bytes.Buffer
	r, _ := interp.New(interp.StdIO(nil, &b, &b))
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	for _, stmt := range file.Stmts {
		err := r.Run(ctx, stmt)
		if !errors.As(err, new(interp.ExitStatus)) && err != nil {
			// Keep track of unexpected errors.
			b.WriteString(err.Error())
		}
		if r.Exited() {
			break
		}
	}
	if got := b.String(); got != want {
		t.Fatalf("\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunnerSourceAliasCompoundLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want string
	}{
		{
			src:  "set -o posix\nalias forx='for x in 1; ' fory='for y in 2; do echo $y;' for='\n do' dn='\n done'\nforx for echo $x; dn\nfory dn\n",
			want: "1\n2\n",
		},
		{
			src:  "set -o posix\nalias c='case a ' case='\n in a) :' in=\nc case X; echo A; esac\n",
			want: "A\n",
		},
	}
	for _, tt := range tests {
		parser := syntax.NewParser(syntax.RecoverErrors(32))
		var stmts []*syntax.Stmt
		for stmt, err := range parser.StmtsSeq(strings.NewReader(tt.src)) {
			if stmt != nil {
				stmts = append(stmts, stmt)
			}
			if err != nil {
				continue
			}
		}
		var b bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &b, &b), interp.WithBashSource([]byte(tt.src)), interp.WithIncrementalFilename(""))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		for _, stmt := range stmts {
			if consumed := r.ConsumedSourceOffset(); consumed > int(stmt.Pos().Offset()) {
				continue
			}
			err = r.Run(ctx, stmt)
			if !errors.As(err, new(interp.ExitStatus)) && err != nil {
				b.WriteString(err.Error())
			}
		}
		cancel()
		if got := b.String(); got != tt.want {
			t.Fatalf("input: %q\nwant: %q\ngot:  %q", tt.src, tt.want, got)
		}
	}
}

func TestRunnerCommandSubstAliasClosesPhysicalLine(t *testing.T) {
	t.Parallel()

	src := "shopt -s expand_aliases\n" +
		"set -o posix\n" +
		"alias short='echo ok 8 )'\n" +
		"echo \"$( short \"\n"
	parser := syntax.NewParser(syntax.RecoverErrors(32), syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &b, &b),
		interp.WithBashSource([]byte(src)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); !errors.As(err, new(interp.ExitStatus)) && err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "ok 8 \n"; got != want {
		t.Fatalf("\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunnerResetFields(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()
	logPath := filepath.Join(tdir, "log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	r, _ := interp.New(
		interp.Params("-f", "--", "first", tdir, logPath),
		interp.Dir(tdir),
		interp.ExecHandlers(testExecHandler),
	)
	// Check that using option funcs and Runner fields directly is still
	// kept by Reset.
	interp.StdIO(nil, logFile, os.Stderr)(r)
	r.Env = expand.ListEnviron(append(os.Environ(), "GLOBAL=foo")...)

	file := parse(t, nil, `
# Params set 3 arguments
[[ $# -eq 3 ]] || exit 10
[[ $1 == "first" ]] || exit 11

# Params set the -f option (noglob)
[[ -o noglob ]] || exit 12

# $PWD was set via Dir, and should be equal to $2
[[ "$PWD" == "$2" ]] || exit 13

# stdout should go into the log file, which is at $3
echo line1
echo line2
[[ "$(wc -l <$3)" == "2" ]] || exit 14

# $GLOBAL was set directly via the Env field
[[ "$GLOBAL" == "foo" ]] || exit 15

# Change all of the above within the script. Reset should undo this.
set +f -- newargs
cd
exec >/dev/null 2>/dev/null
GLOBAL=
export GLOBAL=
`)
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	for i := range 3 {
		if err := r.Run(ctx, file); err != nil {
			t.Fatalf("run number %d: %v", i, err)
		}
		r.Reset()
		// empty the log file too
		logFile.Truncate(0)
		logFile.Seek(0, io.SeekStart)
	}
}

func TestRunnerManyResets(t *testing.T) {
	t.Parallel()
	r, _ := interp.New()
	for range 5 {
		r.Reset()
	}
}

func TestRunnerAuditHandler(t *testing.T) {
	t.Parallel()
	src := `f(){ :; }; f; echo hi; ls /noexist 2>/dev/null; true`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var events []interp.AuditEvent
	hits := func(e interp.AuditEvent) { events = append(events, e) }
	var out bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &out, &out),
		interp.WithAuditHandler(hits),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawLs, sawEcho, sawTrue bool
	for _, e := range events {
		if len(e.Args) > 0 && e.Args[0] == "ls" {
			sawLs = true
			if e.Kind != "exec" || e.IsBuiltin {
				t.Fatalf("ls event = %+v; want exec/non-builtin", e)
			}
		}
		if len(e.Args) > 0 && e.Args[0] == "echo" {
			sawEcho = true
			if e.Kind != "builtin" || !e.IsBuiltin {
				t.Fatalf("echo event = %+v; want builtin", e)
			}
			if e.EnvDigest == "" || e.CallStackHash == "" {
				t.Fatalf("echo event missing digests: %+v", e)
			}
		}
		if len(e.Args) > 0 && e.Args[0] == "true" {
			sawTrue = true
		}
	}
	if !sawLs || !sawEcho || !sawTrue {
		t.Fatalf("expected audit events for ls/echo/true, got events: %+v", events)
	}
}

func TestRunnerAuditLog(t *testing.T) {
	t.Parallel()
	file, err := syntax.NewParser().Parse(strings.NewReader("echo hi"), "")
	if err != nil {
		t.Fatal(err)
	}
	var out, log bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &out, &out),
		interp.WithDeterministic(1),
		interp.WithAuditLog(&log),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var ev interp.AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(log.Bytes()), &ev); err != nil {
		t.Fatalf("audit log json: %v\n%s", err, log.String())
	}
	if got, want := ev.Kind, "builtin"; got != want {
		t.Fatalf("kind = %q, want %q", got, want)
	}
	if got, want := strings.Join(ev.Args, " "), "echo hi"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

// The BASHY_AUDIT_LOG file records what was executed, so it must not be readable
// or writable by other users — a world-accessible audit trail is not evidence
// (NIST 800-53 AU-9).
func TestBashyAuditLogFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes not applicable")
	}
	t.Parallel()
	// A short path: the value is a filename, not a socket, so length is not a
	// constraint here, but keep it in the test's own temp dir.
	logPath := filepath.Join(t.TempDir(), "audit.log")
	file, err := syntax.NewParser().Parse(strings.NewReader("true"), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := interp.New(interp.Env(expand.ListEnviron("BASHY_AUDIT_LOG=" + logPath)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Run: %v", err)
	}
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("audit log not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("audit log mode %#o is accessible to group/other", perm)
	}
}

func TestRunnerJsonBuiltins(t *testing.T) {
	t.Parallel()
	src := `
foo=bar
arr=(zero one)
declare -A assoc=([k]=v)
f(){ echo ok; }
set --json
declare --json -p foo
declare --json -p arr
declare --json -p assoc
declare --json -F f
`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	var setObj struct {
		Variables []map[string]any `json:"variables"`
	}
	if err := dec.Decode(&setObj); err != nil {
		t.Fatalf("set json: %v\n%s", err, out.String())
	}
	if len(setObj.Variables) == 0 {
		t.Fatalf("set json has no variables")
	}
	var foo, arr, assoc, fn map[string]any
	for _, dst := range []*map[string]any{&foo, &arr, &assoc, &fn} {
		if err := dec.Decode(dst); err != nil {
			t.Fatalf("decode json line: %v\n%s", err, out.String())
		}
	}
	if got, want := foo["name"], "foo"; got != want {
		t.Fatalf("foo name = %v, want %q", got, want)
	}
	if got, want := foo["kind"], "string"; got != want {
		t.Fatalf("foo kind = %v, want %q", got, want)
	}
	if got, want := foo["value"], "bar"; got != want {
		t.Fatalf("foo value = %v, want %q", got, want)
	}
	if got, want := arr["kind"], "indexed"; got != want {
		t.Fatalf("arr kind = %v, want %q", got, want)
	}
	if got, want := assoc["kind"], "associative"; got != want {
		t.Fatalf("assoc kind = %v, want %q", got, want)
	}
	if got, want := fn["name"], "f"; got != want {
		t.Fatalf("function name = %v, want %q", got, want)
	}
}

func TestRunnerJsonBuiltinsPosixMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
	}{
		{"set", "set -o posix\nset --json\n"},
		{"declare", "set -o posix\nfoo=bar\ndeclare --json -p foo\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			r, err := interp.New(interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			if err := r.Run(ctx, file); err == nil {
				t.Fatalf("Run succeeded; want invalid-option failure\n%s", out.String())
			}
			if strings.Contains(out.String(), `"variables"`) || strings.Contains(out.String(), `"name"`) {
				t.Fatalf("POSIX mode emitted JSON:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "invalid option") {
				t.Fatalf("output = %q; want invalid option", out.String())
			}
		})
	}
}

func TestRunnerStructuredErrors(t *testing.T) {
	t.Parallel()
	src := "f(){ return 1 2; }\nf\nmissing-command\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "script.sh")
	if err != nil {
		t.Fatal(err)
	}
	var events []interp.ErrorEvent
	var out bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &out, &out),
		interp.WithBashCompatErrors(true),
		interp.WithStructuredErrors(func(e interp.ErrorEvent) {
			events = append(events, e)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	err = r.Run(ctx, file)
	if status, ok := interp.IsExitStatus(err); !ok || status != 127 {
		t.Fatalf("Run status = %v, %v; want 127", status, err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v; want 2 events", events)
	}
	if got, want := events[0].Kind, "builtin"; got != want {
		t.Fatalf("first kind = %q, want %q", got, want)
	}
	if got, want := events[0].Command, "return"; got != want {
		t.Fatalf("first command = %q, want %q", got, want)
	}
	if got, want := events[0].Function, "f"; got != want {
		t.Fatalf("first function = %q, want %q", got, want)
	}
	if got, want := events[0].ExitStatus, uint8(2); got != want {
		t.Fatalf("first exit status = %d, want %d", got, want)
	}
	if got, want := events[0].Message, "script.sh: line 1: return: too many arguments"; got != want {
		t.Fatalf("first message = %q, want %q", got, want)
	}
	if got, want := events[0].Pos.Line(), uint(1); got != want {
		t.Fatalf("first line = %d, want %d", got, want)
	}

	if got, want := events[1].Kind, "exec"; got != want {
		t.Fatalf("second kind = %q, want %q", got, want)
	}
	if got, want := events[1].Command, "missing-command"; got != want {
		t.Fatalf("second command = %q, want %q", got, want)
	}
	if got, want := events[1].ExitStatus, uint8(127); got != want {
		t.Fatalf("second exit status = %d, want %d", got, want)
	}
	if got, want := events[1].Message, "script.sh: line 3: missing-command: command not found"; got != want {
		t.Fatalf("second message = %q, want %q", got, want)
	}
}

func TestRunnerDeterministic(t *testing.T) {
	t.Parallel()
	run := func(seed int64) string {
		src := `echo $RANDOM $RANDOM $$ $SECONDS`
		file, _ := syntax.NewParser().Parse(strings.NewReader(src), "")
		var out bytes.Buffer
		r, _ := interp.New(
			interp.StdIO(nil, &out, &out),
			interp.WithDeterministic(seed),
		)
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		defer cancel()
		if err := r.Run(ctx, file); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return out.String()
	}
	a := run(42)
	b := run(42)
	if a != b {
		t.Fatalf("deterministic runs disagree: %q vs %q", a, b)
	}
	c := run(7)
	if a == c {
		t.Fatalf("different seeds produced same stream: %q", a)
	}
}

func TestRunnerDeterministicEnvAndSetOption(t *testing.T) {
	t.Parallel()
	src := `echo $RANDOM $$ $SECONDS; set -o deterministic; echo $RANDOM $$ $SECONDS`
	file, _ := syntax.NewParser().Parse(strings.NewReader(src), "")
	run := func() string {
		var out bytes.Buffer
		r, err := interp.New(
			interp.Env(expand.ListEnviron("BASHY_DETERMINISTIC=9")),
			interp.StdIO(nil, &out, &out),
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		defer cancel()
		if err := r.Run(ctx, file); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return out.String()
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("deterministic env runs disagree: %q vs %q", a, b)
	}
}

func TestRunnerLoginShell(t *testing.T) {
	t.Parallel()

	// With WithLoginShell, `logout` should exit cleanly with the
	// caller-provided code.
	file, err := syntax.NewParser().Parse(strings.NewReader("logout 7"), "")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &b, &b), interp.WithLoginShell(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	runErr := r.Run(ctx, file)
	if runErr == nil {
		t.Fatalf("expected exit status from `logout 7`, got nil")
	}
	var status interp.ExitStatus
	if !errors.As(runErr, &status) || uint8(status) != 7 {
		t.Fatalf("want exit status 7, got %v", runErr)
	}
	if got := b.String(); got != "" {
		t.Fatalf("unexpected stderr/stdout: %q", got)
	}
}

func TestRunnerFilename(t *testing.T) {
	t.Parallel()

	want := "f.sh\n"
	file, _ := syntax.NewParser().Parse(strings.NewReader("echo $0"), "f.sh")
	var b bytes.Buffer
	r, _ := interp.New(interp.StdIO(nil, &b, &b))
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != want {
		t.Fatalf("\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunnerSourcePreservesArgv0(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "dual-use-guard")
	src := "case \"$0\" in */dual-use-guard) printf direct;; *) printf 'sourced:%s' \"$0\";; esac\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, name, input, filename, want string, opts ...interp.RunnerOption) {
		t.Helper()
		file, err := syntax.NewParser().Parse(strings.NewReader(input), filename)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		r, err := interp.New(append([]interp.RunnerOption{
			interp.Dir(dir), interp.StdIO(nil, &b, &b),
		}, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		defer cancel()
		if err := r.Run(ctx, file); err != nil {
			t.Fatal(err)
		}
		if got := b.String(); got != want {
			t.Fatalf("%s: want %q, got %q", name, want, got)
		}
	}

	// Direct script execution still derives $0 from its script path.
	run(t, "direct", src, path, "direct")
	// Command-string and interactive shells retain their invocation name
	// when they source a dual-use file.
	run(t, "command-string", ". ./dual-use-guard", "/bin/zsh", "sourced:/bin/zsh")
	run(t, "interactive", ". ./dual-use-guard", "", "sourced:/bin/zsh", interp.WithArgv0("/bin/zsh"))
}

func TestRunnerPosixStdinArgv0(t *testing.T) {
	t.Parallel()

	src := "func() { printf '%s\\n' \"${0##*/}\"; }\nfunc\nfunc 1\n"
	file, _ := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(src), "bash")
	var b bytes.Buffer
	r, _ := interp.New(
		interp.StdIO(nil, &b, &b),
		interp.WithPosixMode(true),
		interp.WithBashSource([]byte(src)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "sh\nsh\n"; got != want {
		t.Fatalf("\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunnerEnvNoModify(t *testing.T) {
	t.Parallel()

	env := expand.ListEnviron("one=1", "two=2")
	file := parse(t, nil, `echo -n "$one $two; "; one=x; unset two`)

	var b bytes.Buffer
	r, _ := interp.New(interp.Env(env), interp.StdIO(nil, &b, &b))
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	for range 3 {
		r.Reset()
		err := r.Run(ctx, file)
		if err != nil {
			t.Fatal(err)
		}
	}

	want := "1 2; 1 2; 1 2; "
	if got := b.String(); got != want {
		t.Fatalf("\nwant: %q\ngot:  %q", want, got)
	}
}

func TestMalformedPathOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Skipping windows test on non-windows GOOS")
	}
	tdir := t.TempDir()
	t.Parallel()

	path := filepath.Join(tdir, "test.cmd")
	script := []byte("@echo foo")
	if err := os.WriteFile(path, script, 0o777); err != nil {
		t.Fatal(err)
	}

	// set PATH to c:\tmp\dir instead of C:\tmp\dir
	volume := filepath.VolumeName(tdir)
	pathList := strings.ToLower(volume) + tdir[len(volume):]

	file := parse(t, nil, "test.cmd")
	var cb concBuffer
	r, _ := interp.New(interp.Env(expand.ListEnviron("PATH="+pathList)), interp.StdIO(nil, &cb, &cb))
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	want := "foo\r\n"
	if got := cb.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestReadShouldNotPanicWithNilStdin(t *testing.T) {
	t.Parallel()

	r, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}

	f := parse(t, nil, "read foobar")
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, f); err == nil {
		t.Fatal("it should have returned an error")
	}
}

func TestRunnerVars(t *testing.T) {
	t.Parallel()

	r, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}

	f := parse(t, nil, "foo=updated; BAR=new")
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, f); err != nil {
		t.Fatal(err)
	}

	if want, got := "updated", r.Vars["foo"].String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunnerSubshell(t *testing.T) {
	t.Parallel()

	r1, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}

	r2 := r1.Subshell()
	f1 := parse(t, nil, "PARENT=foo")
	f2 := parse(t, nil, "CHILD=bar")

	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r1.Run(ctx, f1); err != nil {
		t.Fatal(err)
	}
	if err := r2.Run(ctx, f2); err != nil {
		t.Fatal(err)
	}

	if want, got := "foo", r1.Vars["PARENT"].String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
	if want, got := "bar", r2.Vars["CHILD"].String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}

	r3 := r2.Subshell()
	f3 := parse(t, nil, "CHILD=modified")
	if err := r3.Run(ctx, f3); err != nil {
		t.Fatal(err)
	}
	if want, got := "bar", r2.Vars["CHILD"].String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
	if want, got := "modified", r3.Vars["CHILD"].String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestAssignValNilIndex(t *testing.T) {
	t.Parallel()

	// This is the real trigger from bash 5.3's assoc11.sub:
	// associative pair-list compound assignment where elements
	// without [key]= form are treated as alternating key/value.
	// Previously this caused a nil pointer dereference in assignVal
	// when as.Index was nil but prev.Kind was expand.Associative.
	src := "declare -A foo\nfoo=( 0 'a]=test1;#a' 1 123 )\ndeclare -p foo\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// The output should contain keys 0 and 1 with values, and no panic.
	if !strings.Contains(got, "[0]=") || !strings.Contains(got, "[1]=") {
		t.Fatalf("expected declare -p output with keys 0 and 1, got: %q", got)
	}
}

func TestRunnerNonFileStdin(t *testing.T) {
	// Not t.Parallel: this re-execs $GOSH_PROG and asserts exact stdin
	// draining. In the goroutine-subshell model the process fd table is
	// shared, so a concurrent test's exec can perturb stdin/fd state and
	// flake this case (seen only under CI's 2-core -race). Run it serially.

	var cb concBuffer
	r, err := interp.New(interp.StdIO(strings.NewReader("a\nb\nc\n"), &cb, &cb))
	if err != nil {
		t.Fatal(err)
	}
	file := parse(t, nil, "while read a; do echo $a; GOSH_CMD=print_ok $GOSH_PROG; done")
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.WriteString(err.Error())
	}
	// TODO: just like with heredocs, the first print_ok call consumes all stdin.
	qt.Assert(t, qt.Equals(cb.String(), "a\nexec ok\nb\nexec ok\nc\nexec ok\n"))
}

func TestRunnerSourceReadConsumesBufferedStdin(t *testing.T) {
	t.Parallel()

	src := "printf 'read line\\necho got=$line\\n' >sub\nsource ./sub\npayload\necho after\n"
	file := parse(t, nil, src)
	tdir := t.TempDir()
	var cb concBuffer
	r, err := interp.New(
		interp.Dir(tdir),
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashSource([]byte(src)),
		interp.WithStdinScript(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.WriteString(err.Error())
	}
	qt.Assert(t, qt.Equals(cb.String(), "got=payload\nafter\n"))
}

func TestRunnerExecConsumesBufferedStdin(t *testing.T) {
	t.Parallel()

	src := "consume\npayload\necho after\n"
	file := parse(t, nil, src)
	var cb concBuffer
	readOneLine := func(r io.Reader) string {
		var b strings.Builder
		var one [1]byte
		for {
			n, err := r.Read(one[:])
			if n > 0 {
				if one[0] == '\n' {
					break
				}
				b.WriteByte(one[0])
			}
			if err != nil {
				break
			}
		}
		return b.String()
	}
	r, err := interp.New(
		interp.StdIO(nil, &cb, &cb),
		interp.WithBashSource([]byte(src)),
		interp.WithStdinScript(true),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if args[0] != "consume" {
					return next(ctx, args)
				}
				hc := interp.HandlerCtx(ctx)
				fmt.Fprintf(hc.Stdout, "got=%s\n", readOneLine(hc.Stdin))
				return nil
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.WriteString(err.Error())
	}
	qt.Assert(t, qt.Equals(cb.String(), "got=payload\nafter\n"))
}

func TestCdPosixComponent(t *testing.T) {
	t.Parallel()

	runCd := func(t *testing.T, tdir, src string, opts ...interp.RunnerOption) string {
		t.Helper()
		p := syntax.NewParser()
		file := parse(t, p, src)
		var cb strings.Builder
		baseOpts := []interp.RunnerOption{
			interp.Dir(tdir),
			interp.StdIO(nil, &cb, &cb),
			interp.ExecHandlers(testExecHandler),
			interp.Params("-o", "posix"),
		}
		baseOpts = append(baseOpts, opts...)
		r, err := interp.New(baseOpts...)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		defer cancel()
		if err := r.Run(ctx, file); err != nil {
			cb.WriteString(err.Error() + "\n")
		}
		return cb.String()
	}

	// Physical cd resolves relative operands from the live directory, even if
	// another process renamed that directory and made PWD stale.
	t.Run("physical-current-directory-renamed", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("requires a live directory path from a retained descriptor")
		}
		tdir := t.TempDir()
		got := runCd(t, tdir, `
mkdir old
cd -P old
mv ../old ../new
cd -P .
printf 'status=%s\nPWD=%s\nOLDPWD=%s\n' "$?" "$PWD" "$OLDPWD"
`)
		physicalDir, err := filepath.EvalSymlinks(tdir)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(got, "status=0\nPWD="+filepath.Join(physicalDir, "new")+
			"\nOLDPWD="+filepath.Join(physicalDir, "old")+"\n"))
	})

	// non-directory file in operand component (-L)
	t.Run("non-directory-component-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		os.WriteFile(filepath.Join(tdir, "file"), []byte("x"), 0o644)
		got := runCd(t, tdir, "cd -L ./file/../dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "Not a directory")))
	})

	// non-directory file in operand component (-P)
	t.Run("non-directory-component-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		os.WriteFile(filepath.Join(tdir, "file"), []byte("x"), 0o644)
		got := runCd(t, tdir, "cd -P ./file/../dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "Not a directory")))
	})

	// non-existing file in operand component (-L)
	t.Run("nonexistent-component-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "cd -L ./_no_such_file_/../dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "No such file or directory")))
	})

	// non-existing file in operand component (-P)
	t.Run("nonexistent-component-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "cd -P ./_no_such_file_/../dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "No such file or directory")))
	})

	// CDPATH: found in cd path (-L)
	t.Run("cdpath-found-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath1 := filepath.Join(tdir, "cdpath1")
		os.MkdirAll(filepath.Join(cdpath1, "foo"), 0o755)
		got := runCd(t, tdir, "CDPATH="+cdpath1+":; cd -L foo >/dev/null; echo status=$?; echo PWD=$PWD")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
	})

	// CDPATH: found in cd path (-P) with symlinks resolved
	t.Run("cdpath-found-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath1 := filepath.Join(tdir, "cdpath1")
		os.MkdirAll(filepath.Join(cdpath1, "foo"), 0o755)
		got := runCd(t, tdir, "CDPATH="+cdpath1+":; cd -P foo >/dev/null; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
	})

	// CDPATH: found in dot cd path (-L), should print in POSIX mode
	t.Run("cdpath-dot-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "CDPATH=.:; cd -L dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
		qt.Assert(t, qt.IsTrue(strings.Contains(got, tdir+"/dev\n")))
	})

	// CDPATH: found in dot cd path (-P), should print in POSIX mode
	t.Run("cdpath-dot-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "CDPATH=.:; cd -P dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
		qt.Assert(t, qt.IsTrue(strings.Contains(got, tdir+"/dev\n")))
	})

	// symlink resolution in old PWD under -P
	t.Run("symlink-in-old-PWD-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath2 := filepath.Join(tdir, "cdpath2")
		os.MkdirAll(filepath.Join(cdpath2, "foo"), 0o755)
		os.MkdirAll(filepath.Join(cdpath2, "dev"), 0o755)
		os.Symlink(filepath.Join("cdpath2", "foo"), filepath.Join(tdir, "link"))
		got := runCd(t, tdir, "cd -L link && cd -P ./../dev/. && printf 'PWD=%s\\n' \"$PWD\"")
		// On macOS /var is a symlink to /private/var, so the physical
		// PWD may include the /private prefix.
		physTdir := tdir
		if r, err := filepath.EvalSymlinks(tdir); err == nil {
			physTdir = r
		}
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "PWD="+physTdir+"/cdpath2/dev\n")))
	})

	// default operand is HOME (-L)
	t.Run("default-HOME-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "homedir"), 0o755)
		got := runCd(t, tdir, "HOME="+tdir+"/homedir; cd -L; echo status=$?; pwd")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
		qt.Assert(t, qt.IsTrue(strings.Contains(got, tdir+"/homedir")))
	})

	// default operand is HOME (-P)
	t.Run("default-HOME-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "homedir"), 0o755)
		got := runCd(t, tdir, "HOME="+tdir+"/homedir; cd -P; echo status=$?; pwd")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
		qt.Assert(t, qt.IsTrue(strings.Contains(got, tdir+"/homedir")))
	})

	// cd path ending with slash (-L)
	t.Run("cdpath-slash-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "CDPATH=/; cd -L dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
	})

	// cd path ending with slash (-P)
	t.Run("cdpath-slash-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "CDPATH=/; cd -P dev; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
	})

	// cd paths ignored for absolute operand (-L)
	t.Run("cdpath-ignored-abs-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath1 := filepath.Join(tdir, "cdpath1")
		os.MkdirAll(cdpath1, 0o755)
		got := runCd(t, tdir, "CDPATH="+cdpath1+":; cd -L /; echo status=$?; pwd")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "/\n")))
	})

	// cd paths ignored for absolute operand (-P)
	t.Run("cdpath-ignored-abs-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath1 := filepath.Join(tdir, "cdpath1")
		os.MkdirAll(cdpath1, 0o755)
		got := runCd(t, tdir, "CDPATH="+cdpath1+":; cd -P /; echo status=$?; pwd")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "/\n")))
	})

	// directory not found with unset CDPATH (-L)
	t.Run("not-found-unset-cdpath-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		got := runCd(t, tdir, "unset CDPATH; cd -L _no_such_path_; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "No such file or directory")))
	})

	// directory not found with unset CDPATH (-P)
	t.Run("not-found-unset-cdpath-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		got := runCd(t, tdir, "unset CDPATH; cd -P _no_such_path_; echo status=$?")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "No such file or directory")))
	})

	// found in empty cd path (-L), no print for empty entry
	t.Run("cdpath-empty-L", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath1 := filepath.Join(tdir, "cdpath1")
		os.MkdirAll(filepath.Join(cdpath1, "foo"), 0o755)
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "CDPATH="+cdpath1+"::; cd -L dev; echo status=$?; pwd")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
	})

	// found in empty cd path (-P), no print for empty entry
	t.Run("cdpath-empty-P", func(t *testing.T) {
		t.Parallel()
		tdir := t.TempDir()
		cdpath1 := filepath.Join(tdir, "cdpath1")
		os.MkdirAll(filepath.Join(cdpath1, "foo"), 0o755)
		os.MkdirAll(filepath.Join(tdir, "dev"), 0o755)
		got := runCd(t, tdir, "CDPATH="+cdpath1+"::; cd -P dev; echo status=$?; pwd")
		qt.Assert(t, qt.IsTrue(strings.Contains(got, "status=0")))
	})
}

func TestCdPwdPosixSymlinkSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires symbolic links")
	}
	t.Parallel()

	tdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tdir, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tdir, "cdroot", "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(tdir, "relative")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tdir, "real"), filepath.Join(tdir, "absolute")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tdir, "cdroot"), filepath.Join(tdir, "cdpath")); err != nil {
		t.Fatal(err)
	}

	const src = `
root=$PWD
cd relative/sub
logical=$PWD old=$OLDPWD
pwd -L >/dev/null
test "$PWD" = "$logical" && test "$OLDPWD" = "$old" || exit 1
pwd -P >/dev/null
physical=$PWD
test "$PWD" = "$(pwd -P)" && test "$OLDPWD" = "$old" || exit 1
cd "$root"
cd absolute/sub
test "$PWD" = "$root/absolute/sub" || exit 1
cd "$root"
CDPATH="$root/cdpath"
cd -P target >/dev/null
test "$PWD" = "$(pwd -P)" && test "$OLDPWD" = "$root" || exit 1
`
	file := parse(t, syntax.NewParser(syntax.Variant(syntax.LangPOSIX)), src)
	var output bytes.Buffer
	r, err := interp.New(
		interp.Dir(tdir),
		interp.StdIO(nil, &output, &output),
		interp.ExecHandlers(testExecHandler),
		interp.WithPosixMode(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run failed: %v\noutput: %s", err, output.String())
	}
}
