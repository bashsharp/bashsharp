//go:build unix && full

package interp_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A portable child helper makes process-group assertions independent of ps,
// sh, or host utility output. It is spawned by the actual default exec handler.
func TestStreamGroupChildHelper(t *testing.T) {
	mode := os.Getenv("BASHPP_STREAM_GROUP_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "identity":
		fmt.Printf("child=%d group=%d\n", os.Getpid(), syscall.Getpgrp())
	case "copy":
		fmt.Printf("child=%d group=%d\n", os.Getpid(), syscall.Getpgrp())
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "first":
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Print(line)
	}
	os.Exit(0)
}

func streamGroupChild(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "GOSH_PROG= GOSH_CMD= BASHPP_STREAM_GROUP_HELPER=" + mode + " " + strconv.Quote(exe) + " -test.run=^TestStreamGroupChildHelper$"
}

const streamGroupFixture = `~~~python as py
def report(stdin: TextIO) -> Iterator[str]:
    import os
    globals()['count'] = globals().get('count', 0) + 1
    yield 'worker=%d group=%d\n' % (os.getpid(), os.getpgrp())
    for line in stdin:
        yield line
def state() -> int:
    return globals().get('count', 0)
~~~
`

func requireStreamGroupPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
}

func TestForeignStreamPipelineActualGroupAndPersistentState(t *testing.T) {
	requireStreamGroupPython(t)
	source := streamGroupFixture + streamGroupChild(t, "identity") + " | py.report | " + streamGroupChild(t, "copy") + "\n" + `n := py.state()
echo "state=$n"
`
	out, stderr, err := runStreamGroup(t, source)
	if err != nil || stderr != "" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
	group := 0
	identities := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "group=") {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(fields[1], "group="))
		if err != nil {
			t.Fatal(err)
		}
		if group == 0 {
			group = id
		}
		if id != group {
			t.Fatalf("different pipeline groups: %q", out)
		}
		identities++
	}
	if identities != 3 || group == syscall.Getpgrp() || !strings.Contains(out, "state=1\n") {
		t.Fatalf("identities=%d group=%d output=%q", identities, group, out)
	}
}

func TestForeignStreamPipelineWrapperAndDynamicRefusal(t *testing.T) {
	requireStreamGroupPython(t)
	t.Run("wrapper", func(t *testing.T) {
		out, stderr, err := runStreamGroup(t, streamGroupFixture+"wrapped() { py.report; }\nprintf 'input\\n' | wrapped\n")
		if err != nil || stderr != "" || !strings.Contains(out, "input\n") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
	t.Run("sequential wrapper calls", func(t *testing.T) {
		out, stderr, err := runStreamGroup(t, streamGroupFixture+"wrapped() { py.report; py.report; }\nprintf 'input\\n' | wrapped\nn := py.state()\necho state=$n\n")
		if err != nil || stderr != "" || !strings.Contains(out, "state=2\n") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
	t.Run("alias", func(t *testing.T) {
		out, stderr, err := runStreamGroup(t, streamGroupFixture+"shopt -s expand_aliases\nalias wrapped=py.report\nprintf 'input\\n' | wrapped\n")
		if err != nil || stderr != "" || !strings.Contains(out, "input\n") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
	t.Run("dynamic", func(t *testing.T) {
		out, stderr, err := runStreamGroup(t, streamGroupFixture+"name=py.report\nprintf 'input\\n' | $name\n")
		if err == nil || !strings.Contains(stderr, "dynamic pipeline command") || strings.Contains(out, "worker=") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
	t.Run("same worker twice", func(t *testing.T) {
		out, stderr, err := runStreamGroup(t, streamGroupFixture+"py.report | py.report\n")
		if err == nil || !strings.Contains(stderr, "multiple pipeline stages") || out != "" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

func TestForeignStreamPipelineEarlyConsumerExit(t *testing.T) {
	requireStreamGroupPython(t)
	source := `~~~python as py
def rows() -> Iterator[str]:
    try:
        for i in range(1000000):
            yield 'row'
    finally:
        globals()['closed'] = globals().get('closed', 0) + 1
def state() -> int:
    return globals().get('closed', 0)
~~~
py.rows | ` + streamGroupChild(t, "first") + `
n := py.state()
echo "closed=$n"
`
	out, stderr, err := runStreamGroup(t, source)
	if err != nil || !strings.Contains(out, "closed=1\n") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

type streamGroupBuffer struct {
	mu    sync.Mutex
	b     strings.Builder
	ready chan struct{}
	once  sync.Once
}

func (b *streamGroupBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.b.Write(p)
	if strings.Contains(b.b.String(), "worker=") {
		b.once.Do(func() { close(b.ready) })
	}
	return n, err
}
func (b *streamGroupBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func TestForeignStreamPipelineCancellationReapsGroup(t *testing.T) {
	requireStreamGroupPython(t)
	source := `~~~python as py
def rows() -> Iterator[str]:
    import os, time
    yield 'worker=%d group=%d' % (os.getpid(), os.getpgrp())
    time.sleep(60)
    yield 'late'
~~~
py.rows | ` + streamGroupChild(t, "copy") + "\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "stream-cancel.bpp")
	if err != nil {
		t.Fatal(err)
	}
	out := &streamGroupBuffer{ready: make(chan struct{})}
	stderr := &streamGroupBuffer{ready: make(chan struct{})}
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, out, stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, file) }()
	select {
	case <-out.ready:
	case err := <-done:
		t.Fatalf("exited before ready: %v/%q/%q", err, out.String(), stderr.String())
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not join pipeline")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := strings.TrimPrefix(strings.TrimPrefix(fields[0], "worker="), "child=")
		pid, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("process %d not reaped: %v; output=%q", pid, err, out.String())
		}
	}
}

func TestClassicPipelineHasNoForeignGroup(t *testing.T) {
	source := streamGroupChild(t, "identity") + " | " + streamGroupChild(t, "copy") + "\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(source), "classic.sh")
	if err != nil {
		t.Fatal(err)
	}
	out := &streamGroupBuffer{ready: make(chan struct{})}
	stderr := &streamGroupBuffer{ready: make(chan struct{})}
	runner, err := interp.New(interp.StdIO(nil, out, stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(t.Context(), file); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasSuffix(line, fmt.Sprintf("group=%d", syscall.Getpgrp())) {
			t.Fatalf("classic group changed: %q", out.String())
		}
	}
}

func runStreamGroup(t *testing.T, source string) (string, string, error) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "stream-group.bpp")
	if err != nil {
		t.Fatal(err)
	}
	out := &streamGroupBuffer{ready: make(chan struct{})}
	stderr := &streamGroupBuffer{ready: make(chan struct{})}
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, out, stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err = runner.Run(ctx, file)
	return out.String(), stderr.String(), err
}

func TestForeignStreamPipelineBackgroundSignalReapsWorker(t *testing.T) {
	requireStreamGroupPython(t)
	marker := t.TempDir() + "/ready"
	source := fmt.Sprintf(`~~~python as py
def rows() -> Iterator[str]:
    import os, time
    with open(%q, 'w') as marker:
        marker.write(str(os.getpid()))
    yield 'ready'
    time.sleep(60)
    yield 'late'
~~~
py.rows | %s &
while [ ! -f %q ]; do sleep 0.01; done
kill -TERM %%1
wait
`, marker, streamGroupChild(t, "copy"), marker)
	out, stderr, err := runStreamGroup(t, source)
	if err != nil {
		t.Fatalf("background job did not finish after signal: %v; out=%q stderr=%q", err, out, stderr)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("marker: %v; out=%q stderr=%q run=%v", readErr, out, stderr, err)
	}
	pid, parseErr := strconv.Atoi(string(data))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if status := syscall.Kill(pid, 0); status != syscall.ESRCH {
		t.Fatalf("background worker %d survived job signal: %v; out=%q stderr=%q run=%v", pid, status, out, stderr, err)
	}
	if strings.Contains(out, "late") {
		t.Fatalf("signal did not terminate worker: %q", out)
	}
}

// Like TestForegroundCommandStartsWithTerminal, this executes a separate shell
// in a new controlling PTY. Input is a terminal VINTR byte, never a context
// cancellation or direct kill, so foreground ownership determines delivery.
func TestForeignStreamPipelineTerminalInterrupt(t *testing.T) {
	const helperKey = "BASHPP_STREAM_TTY_HELPER"
	if os.Getenv(helperKey) == "1" {
		source := `~~~python as py
def rows(stdin: TextIO) -> Iterator[str]:
    import os, signal, time
    tty = os.open('/dev/tty', os.O_RDWR)
    def interrupt(signum, frame):
        os.write(tty, b'TTY_INTERRUPT\n')
        raise KeyboardInterrupt
    signal.signal(signal.SIGINT, interrupt)
    try:
        yield 'TTY_WORKER pid=%d pgrp=%d foreground=%d\n' % (os.getpid(), os.getpgrp(), os.tcgetpgrp(tty))
        while True:
            time.sleep(60)
    finally:
        os.close(tty)
~~~
set -m
py.rows | ` + streamGroupChild(t, "copy") + `
printf 'TTY_STATUS=%s\n' "$?"
`
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "stream-tty.bpp")
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Interactive(true), interp.StdIO(os.Stdin, os.Stdout, os.Stderr))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err = runner.Run(ctx, file); err != nil {
			t.Fatal(err)
		}
		foreground, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
		if err != nil || foreground != syscall.Getpgrp() {
			t.Fatalf("terminal not restored: foreground=%d shell=%d err=%v", foreground, syscall.Getpgrp(), err)
		}
		fmt.Printf("TTY_RESTORED shell=%d foreground=%d\n", syscall.Getpgrp(), foreground)
		return
	}
	requireStreamGroupPython(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForeignStreamPipelineTerminalInterrupt$", "-test.timeout=18s")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOSH_PROG=") && !strings.HasPrefix(entry, "GOSH_CMD=") && !strings.HasPrefix(entry, helperKey+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, helperKey+"=1")
	primary, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(primary)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text() + "\n":
			case <-ctx.Done():
				return
			}
		}
	}()
	var output strings.Builder
	waitFor := func(want string) {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for !strings.Contains(output.String(), want) {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("PTY closed before %q: %s", want, output.String())
				}
				output.WriteString(line)
			case <-timer.C:
				t.Fatalf("PTY timeout before %q: %s", want, output.String())
			}
		}
	}
	waitFor("TTY_WORKER")
	identity := regexp.MustCompile(`TTY_WORKER pid=([0-9]+) pgrp=([0-9]+) foreground=([0-9]+)`).FindStringSubmatch(output.String())
	if identity == nil || identity[1] != identity[2] || identity[2] != identity[3] || identity[2] == strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("worker does not own foreground job: %s", output.String())
	}
	workerPID, _ := strconv.Atoi(identity[1])
	defer func() {
		// A failed assertion must not orphan the isolated test worker.
		if group, err := syscall.Getpgid(workerPID); err == nil && group == workerPID {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	}()
	// The PTY's default VINTR is ^C; this exercises the terminal line discipline.
	if _, err = primary.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitFor("TTY_INTERRUPT")
	waitFor("TTY_RESTORED")
	if err = cmd.Wait(); err != nil {
		t.Fatalf("PTY shell: %v; %s", err, output.String())
	}
	for line := range lines {
		output.WriteString(line)
	}
	text := normalizePTYOutput(output.String())
	if !strings.Contains(text, "TTY_STATUS=130\n") {
		t.Fatalf("terminal interrupt status not preserved: %s", text)
	}
	child := regexp.MustCompile(`child=([0-9]+) group=([0-9]+)`).FindStringSubmatch(text)
	if child == nil || child[2] != identity[2] {
		t.Fatalf("pipeline child not in foreground worker group: %s", text)
	}
	for _, id := range []string{identity[1], child[1]} {
		pid, _ := strconv.Atoi(id)
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("terminal-interrupted child %d not reaped: %v; %s", pid, err, text)
		}
	}
	restored := regexp.MustCompile(`TTY_RESTORED shell=([0-9]+) foreground=([0-9]+)`).FindStringSubmatch(text)
	if restored == nil || restored[1] != restored[2] || restored[1] != strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("terminal ownership not restored to original shell: %s", text)
	}
}
