// Copyright (c) 2019, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestReadFromInheritedDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("inherited input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd := int(f.Fd())
	for _, script := range []string{
		fmt.Sprintf("read -u %d line; printf '%%s\\n' \"$line\"", fd),
		fmt.Sprintf("mapfile -u %d lines; printf '%%s' \"${lines[0]}\"", fd),
	} {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		file := parse(t, nil, script)
		var out, stderr bytes.Buffer
		r, err := interp.New(interp.StdIO(nil, &out, &stderr), interp.WithInheritedFds([]int{fd}))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), file); err != nil {
			t.Fatalf("%q: %v; stderr=%q", script, err, stderr.String())
		}
		if got := out.String(); got != "inherited input\n" {
			t.Fatalf("%q: got %q; stderr=%q", script, got, stderr.String())
		}
	}
}

func TestRunnerTerminalStdIO(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files func(*testing.T) (secondary io.Writer, primary io.Reader)
		want  string
	}{
		{"Nil", func(t *testing.T) (io.Writer, io.Reader) {
			return nil, strings.NewReader("\n")
		}, "\n"},
		{"Pipe", func(t *testing.T) (io.Writer, io.Reader) {
			pr, pw := io.Pipe()
			return pw, pr
		}, "end\n"},
		{"Pseudo", func(t *testing.T) (io.Writer, io.Reader) {
			primary, secondary, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			return secondary, primary
		}, "012end\r\n"},
	}
	file := parse(t, nil, `
		for n in 0 1 2 3; do if [[ -t $n ]]; then echo -n $n; fi; done; echo end
	`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			secondary, primary := test.files(t)
			// some secondary ends can be used as stdin too
			secondaryReader, _ := secondary.(io.Reader)

			r, _ := interp.New(interp.StdIO(secondaryReader, secondary, secondary))
			go func() {
				// To mimic [os/exec.Cmd.Start], use a goroutine.
				if err := r.Run(context.Background(), file); err != nil {
					t.Error(err)
				}
			}()

			got, err := bufio.NewReader(primary).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("\nwant: %q\ngot:  %q", test.want, got)
			}
			if closer, ok := secondary.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if closer, ok := primary.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestInteractiveForegroundJobOwnsTerminal verifies the public job-control
// boundary used by interactive consumers: terminal INTR belongs to the
// foreground external command, and the shell does not advance to its next
// prompt until that command exits. The helper child handles SIGINT and stays
// alive briefly, which exposes the old failure where the shell kept terminal
// ownership and re-prompted immediately.
func TestInteractiveForegroundJobOwnsTerminal(t *testing.T) {
	cmd := exec.Command(os.Getenv("GOSH_PROG"))
	cmd.Env = append(os.Environ(), "GOSH_CMD=foreground_job_shell")
	primary, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(primary)
		for scanner.Scan() {
			lines <- scanner.Text() + "\n"
		}
		close(lines)
	}()

	var output strings.Builder
	waitFor := func(want string, timeout time.Duration) {
		t.Helper()
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		for !strings.Contains(output.String(), want) {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("PTY closed before %q; output=%q", want, output.String())
				}
				output.WriteString(line)
			case <-deadline.C:
				t.Fatalf("timed out waiting for %q; output=%q", want, output.String())
			}
		}
	}

	waitFor("ready\n", 5*time.Second)
	if _, err := primary.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}

	// The child deliberately remains alive for 300ms after acknowledging the
	// interrupt. Its completion marker must precede the next prompt.
	waitFor("caught\n", 5*time.Second)
	waitFor("PROMPT\n", 5*time.Second)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("interactive helper: %v; output=%q", err, output.String())
	}
	got := output.String()
	for _, marker := range []string{"caught\n", "done\n", "PROMPT\n"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("missing %q from output %q", marker, got)
		}
	}
	if strings.Index(got, "done\n") > strings.Index(got, "PROMPT\n") {
		t.Fatalf("prompt preceded foreground child completion: %q", got)
	}
}

// TestInteractiveForegroundJobSurvivesUnmonitoredSignals is the `set +m`
// counterpart to TestInteractiveForegroundJobOwnsTerminal. With job control
// off, prepareForegroundJobCmd never creates a new process group for the
// foreground child (see os_unix.go), so it shares the shell's own pgrp; a
// terminal INTR/QUIT then target the shell process too, not just the child.
// The helper first runs an unmonitored asynchronous external job, exercising
// the exact-disposition restore before the foreground guard is installed.
func TestInteractiveForegroundJobSurvivesUnmonitoredSignals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		control  byte
		pipeline bool
	}{
		{"INTSimple", 3, false},
		{"QUITSimple", 28, false},
		{"INTPipeline", 3, true},
		{"QUITPipeline", 28, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testInteractiveForegroundJobSurvivesUnmonitoredSignal(t, tc.control, tc.pipeline)
		})
	}
}

func testInteractiveForegroundJobSurvivesUnmonitoredSignal(t *testing.T, control byte, pipeline bool) {
	t.Helper()
	cmd := exec.Command(os.Getenv("GOSH_PROG"))
	cmd.Env = append(os.Environ(), "GOSH_CMD=foreground_job_shell_unmonitored")
	if pipeline {
		cmd.Env = append(cmd.Env, "GOSH_PIPELINE=1")
	}
	primary, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	processDone := false
	var processErr error

	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(primary)
		for scanner.Scan() {
			lines <- scanner.Text() + "\n"
		}
		close(lines)
	}()

	var output strings.Builder
	waitFor := func(want string, timeout time.Duration) {
		t.Helper()
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		for !strings.Contains(output.String(), want) {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("PTY closed before %q; output=%q", want, output.String())
				}
				output.WriteString(line)
			case <-deadline.C:
				t.Fatalf("timed out waiting for %q; output=%q", want, output.String())
			case err := <-waitDone:
				processDone = true
				processErr = err
				waitDone = nil
				if err != nil {
					t.Fatalf("interactive helper exited before %q: %v; output=%q", want, err, output.String())
				}
			}
		}
	}

	waitFor("ready\n", 5*time.Second)
	if _, err := primary.Write([]byte{control}); err != nil {
		t.Fatal(err)
	}

	// The child deliberately remains alive for 300ms after acknowledging the
	// interrupt. Its completion marker must precede the next prompt, and the
	// shell itself must not have died from the same terminal SIGINT.
	waitFor("caught\n", 5*time.Second)
	waitFor("PROMPT\n", 5*time.Second)
	if !processDone {
		processErr = <-waitDone
	}
	if processErr != nil {
		t.Fatalf("interactive helper: %v; output=%q", processErr, output.String())
	}
	got := output.String()
	for _, marker := range []string{"caught\n", "done\n", "PROMPT\n"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("missing %q from output %q", marker, got)
		}
	}
	if strings.Index(got, "done\n") > strings.Index(got, "PROMPT\n") {
		t.Fatalf("prompt preceded foreground child completion: %q", got)
	}
}

func TestRunnerTerminalExec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start func(*testing.T, *exec.Cmd) io.Reader
		want  string
	}{
		{"Nil", func(t *testing.T, cmd *exec.Cmd) io.Reader {
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return strings.NewReader("\n")
		}, "\n"},
		{"Pipe", func(t *testing.T, cmd *exec.Cmd) io.Reader {
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = cmd.Stdout
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return out
		}, "end\n"},
		{"Pseudo", func(t *testing.T, cmd *exec.Cmd) io.Reader {
			// Note that we avoid pty.Start,
			// as it closes the secondary terminal via a defer,
			// possibly before the command has finished.
			// That can lead to "signal: hangup" flakes.
			primary, secondary, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = secondary
			cmd.Stdout = secondary
			cmd.Stderr = secondary
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return primary
		}, "012end\r\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command(os.Getenv("GOSH_PROG"),
				"for n in 0 1 2 3; do if [[ -t $n ]]; then echo -n $n; fi; done; echo end")
			primary := test.start(t, cmd)

			got, err := bufio.NewReader(primary).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("\nwant: %q\ngot:  %q", test.want, got)
			}
			if closer, ok := primary.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestRunnerTerminalExecInheritedSparseFD compares external terminal commands
// under bash and gosh, then checks Bashy's sparse inherited-FD path. FD 8 has
// no open shell-managed descriptors below it: this is the shape inherited from
// a parent that has kept a terminal on a high descriptor.
func TestRunnerTerminalExecInheritedSparseFD(t *testing.T) {
	const script = "tty >/dev/null && /bin/echo terminal"

	// A closed *os.File reports fd -1 to os.StartProcess, and Unix forkExec
	// closes that child slot rather than inventing a descriptor there. That is
	// how ExtraFiles expresses a SPARSE fd 8: slots 3 through 7 are closed in
	// the child, not manufactured. It is the same mechanism the runner's own
	// execExtraFiles uses to hand a sparse fd to an external command.
	//
	// Never place fd 8 by dup2'ing over descriptor 8 of the test process
	// itself. A process does not own an arbitrary descriptor number just
	// because it can name it: on darwin the Go runtime's signal pipe
	// (sigNoteSetup, taken at the first signal.Notify) can be sitting there,
	// and hijacking it makes the blocked read inside signal_recv return a byte
	// no sigsend ever queued. The runtime then throws "signal_recv:
	// inconsistent state" and takes the whole test binary down, arbitrarily
	// far from this test.
	closedSlot, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedSlot.Close(); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, cmd *exec.Cmd) string {
		t.Helper()
		primary, secondary, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer primary.Close()
		defer secondary.Close()
		cmd.Stdin = secondary
		cmd.Stdout = secondary
		cmd.Stderr = secondary
		// Child fds 3..7 closed, child fd 8 = the already-open terminal.
		cmd.ExtraFiles = []*os.File{
			closedSlot, closedSlot, closedSlot, closedSlot, closedSlot,
			secondary,
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		got, err := bufio.NewReader(primary).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		return got
	}

	goshCmd := func(src string) *exec.Cmd {
		gotCmd := exec.Command(os.Getenv("GOSH_PROG"), src)
		gotCmd.Env = append(os.Environ(), interp.BashyInheritedFdsEnv+"=8")
		return gotCmd
	}
	want := run(t, exec.Command("bash", "-c", script))
	got := run(t, goshCmd(script))
	if got != want {
		t.Fatalf("external command output differs with inherited sparse FD 8:\nwant: %q\n got: %q", want, got)
	}

	// The first external command does not mention fd 8. It must not make
	// that already-open terminal disappear before the later external command
	// receives it. This is the sparse-fd path which ExtraFiles cannot encode.
	got = run(t, goshCmd("tty >/dev/null && GOSH_CMD=fd8_is_terminal $GOSH_PROG"))
	if got != "terminal\r\n" {
		t.Fatalf("external command lost inherited terminal fd 8: got %q", got)
	}

	// An explicit shell close must override inheritance and remain effective
	// across a preceding external command; otherwise the raw ambient fd would
	// leak into children even though the shell considers it closed.
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	truePath = "'" + strings.ReplaceAll(truePath, "'", "'\"'\"'") + "'"
	got = run(t, goshCmd("exec 8>&-; "+truePath+"; GOSH_CMD=fd8_is_terminal $GOSH_PROG"))
	if got != "not-terminal\r\n" {
		t.Fatalf("explicitly closed inherited fd 8 leaked to external command: got %q", got)
	}
}

func TestExternalCommandKeepsRenamedWorkingDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux provides a child-safe /proc/self/fd cwd handle")
	}
	root := t.TempDir()
	old := filepath.Join(root, "old")
	newPath := filepath.Join(root, "new")
	if err := os.Mkdir(old, 0o755); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`cd %q
mv %q %q
GOSH_CMD=getwd "$GOSH_PROG"
`, old, old, newPath)
	file := parse(t, nil, src)
	var output bytes.Buffer
	runner, err := interp.New(interp.StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; output: %s", err, output.String())
	}
	if got, want := output.String(), newPath+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAwdRestoresRenamedWorkingDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux provides a child-safe /proc/self/fd cwd handle")
	}
	root := t.TempDir()
	old := filepath.Join(root, "old")
	newPath := filepath.Join(root, "new")
	other := filepath.Join(root, "other")
	for _, dir := range []string{old, other} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := fmt.Sprintf(`awd %q mv %q %q
GOSH_CMD=getwd "$GOSH_PROG"
`, other, old, newPath)
	file := parse(t, nil, src)
	var output bytes.Buffer
	runner, err := interp.New(interp.Dir(old), interp.StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; output: %s", err, output.String())
	}
	if got, want := output.String(), newPath+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestRunnerTerminalExecVirtualSparseFD covers a descriptor created by the
// interpreted shell itself, rather than one inherited ambiently at startup.
// The nested process must receive it at fd 8 even though virtual fds 3..7 are
// absent. Terminal harnesses use this shape to save fd 0 before feeding a
// nested shell through a pipe, then restore the terminal with `exec 0<&8`.
func TestRunnerTerminalExecVirtualSparseFD(t *testing.T) {
	primary, secondary, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	defer secondary.Close()

	cmd := exec.Command(os.Getenv("GOSH_PROG"),
		"exec 8<&0; GOSH_CMD=fd8_is_terminal $GOSH_PROG")
	cmd.Stdin = secondary
	cmd.Stdout = secondary
	cmd.Stderr = secondary
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(primary).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got != "terminal\r\n" {
		t.Fatalf("external command lost virtual sparse terminal fd 8: got %q", got)
	}
}

// TestRunnerTerminalExecFunctionPersistentFD covers the VSC terminal restore
// shape: an asynchronous test-purpose function calls a helper whose bare exec
// redirects stdin from a saved terminal descriptor. The redirect must outlive
// the helper function call, just as it does in bash.
func TestRunnerTerminalExecFunctionPersistentFD(t *testing.T) {
	primary, secondary, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	defer secondary.Close()

	script := `
		exec 8<&0
		restore() {
			if test -t 0; then return 0
			elif test -t 8; then exec 0<&8
			else return 1
			fi
		}
		tp() {
			restore || return
			if test -t 0; then echo terminal-after-return
			else echo lost-after-return
			fi
		}
		tp & wait
	`
	cmd := exec.Command(os.Getenv("GOSH_PROG"), script)
	cmd.Stdin = secondary
	cmd.Stdout = secondary
	cmd.Stderr = secondary
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(primary).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got != "terminal-after-return\r\n" {
		t.Fatalf("function-local exec redirect did not persist: got %q", got)
	}
}

// TestRunnerExternalTerminalTools checks the shell-owned boundary shared by
// terminal-aware system tools. The tools themselves remain host providers:
// this only compares the environment, session, and controlling terminal they
// receive when launched through gosh with those received through bash.
func TestRunnerExternalTerminalTools(t *testing.T) {
	for _, name := range []string{"logname", "mesg", "write"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not available: %v", name, err)
		}
	}
	// Some sandboxed hosts permit a shell to invoke write but deny the Go
	// process that backs the interpreter from doing so. That is a host policy,
	// rather than a terminal/session result, so do not compare providers there.
	if err := exec.Command("write", "__gosh_no_such_login__").Run(); err != nil {
		if _, exited := err.(*exec.ExitError); !exited {
			t.Skipf("write cannot be launched by this host: %v", err)
		}
	}

	const script = `
for tool in logname mesg write; do
	case $tool in
	logname) logname ;;
	mesg) mesg ;;
	write) write __gosh_no_such_login__ ;;
	esac
	printf '<%s:%s>\n' "$tool" "$?"
done
`
	run := func(t *testing.T, cmd *exec.Cmd) string {
		t.Helper()
		primary, err := pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(primary)
		// Linux PTYs report EIO, rather than EOF, after the slave side exits.
		// Both mean that all terminal output has been consumed.
		if errors.Is(readErr, unix.EIO) {
			readErr = nil
		}
		if closeErr := primary.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("%v; output: %q", err, got)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(got)
	}

	want := run(t, exec.Command("bash", "-c", script))
	got := run(t, exec.Command(os.Getenv("GOSH_PROG"), script))
	if got != want {
		t.Fatalf("terminal-aware system tools differ across the shell boundary:\nwant: %q\n got: %q", want, got)
	}
}

// TestRunnerExecStartFailureContinues verifies that a kernel exec failure is
// an ordinary command failure. A FIFO with execute permission passes shell
// lookup but cannot be executed, so it isolates that launch boundary without
// depending on a particular system utility.
func TestRunnerExecStartFailureContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-program")
	if err := unix.Mkfifo(path, 0o755); err != nil {
		t.Fatal(err)
	}

	file := parse(t, nil, "./not-a-program; echo survived")
	var out strings.Builder
	r, err := interp.New(interp.Dir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("runner stopped after an exec failure: %v; output: %q", err, out.String())
	}
	if !strings.Contains(out.String(), "survived\n") {
		t.Fatalf("later command did not run after an exec failure: %q", out.String())
	}
}

func TestRunnerDevTTYFallbackOnTerminal(t *testing.T) {
	t.Parallel()

	const src = "test -t 0 < /dev/tty; printf 'test=%s ' $?; " +
		"a=4; read -t 0.000001 a < /dev/tty; printf 'read=%s %s\\n' $? ${a:-unset}"
	primary, secondary, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	r, err := interp.New(
		interp.StdIO(secondary, secondary, secondary),
		interp.OpenHandler(unavailableTTYOpen),
	)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := r.Run(context.Background(), parse(t, nil, src)); err != nil {
			t.Error(err)
		}
		secondary.Close()
	}()

	got, err := bufio.NewReader(primary).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if want := "test=0 read=142 unset\r\n"; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// TestRunnerTerminalTestHighFd verifies that terminal tests resolve the
// runner-owned descriptor tables for fds created by shell redirections.
func TestRunnerTerminalTestHighFd(t *testing.T) {
	t.Parallel()

	primary, secondary, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	file := parse(t, nil, `
		exec 3<&0 4>&1
		for n in 0 1 2 3 4 5; do if [ -t $n ]; then printf %s "$n"; fi; done
		exec 3<&- 4>&-
		if [ -t 3 ]; then printf X; fi
		if [ -t 4 ]; then printf Y; fi
		echo end
	`)
	r, err := interp.New(interp.StdIO(secondary, secondary, secondary))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := r.Run(context.Background(), file); err != nil {
			t.Error(err)
		}
	}()

	got, err := bufio.NewReader(primary).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if want := "01234end\r\n"; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
	if err := secondary.Close(); err != nil {
		t.Fatal(err)
	}
}

func shortPathName(path string) (string, error) {
	panic("only works on windows")
}
