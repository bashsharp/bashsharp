package polyglot

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Fixture provenance (Sprint 221, story 574 / 84d746b3db12, B8):
//
//   xonsh/xonsh commit e2b76f7fa54d2272c0b5a84b77a816fe882bee48 (0.24.2-13),
//   BSD 2-Clause (LICENSE at that commit).
//   - xonsh/procs/proxies.py parse_proxy_return: str → stdout, int → status,
//     sequence → (stdout, stderr-with-newline, status), None → nothing,
//     other → str(r); ProcProxy.run / ProcProxyThread.run: SystemExit → its
//     code, Exception → traceback + 1.
//   - tests/xintegration/test_integrations.py ALL_PLATFORMS: "test calling a
//     function alias" (hello), "test redirecting a function alias from stderr",
//     "test system exit in function alias" (42), "test uncaptured streaming
//     alias" (stderr+stdout, returncode 1), "test output larger than most pipe
//     buffers" (1000 × 100 x), "test ambiguous globs"/"line continuations"
//     (_echo(args) prints ' '.join(args)); ALIASES_THREADABLE_PRINT_CASES
//     (lambda: 1/0 → ZeroDivisionError on stderr, later commands still run;
//     (None, "I failed", 2) → "I failed" on stderr); ALL_PLATFORMS_STDERR
//     "test redirecting a function alias" (stdout print lands on stdout).
//   - tests/procs/test_specs.py test_interrupted_process_returncode (SIGINT →
//     signal status), test_procproxy_not_captured (lambda: 0 → status 0).
//
// Intentional adaptations: argv words are passed as positional strings
// (`fn(*argv)`) rather than xonsh's single `args` list bound by parameter
// name, so `def echo(*args)` is the port of `def _echo(args)`; stdin/stdout/
// stderr file arguments are not injected (the pipeline-filter story owns
// stdin); output is captured per call and relayed, not streamed; SystemExit
// with a non-int code prints it and reports 1 (CPython interpreter semantics,
// xonsh drops it); KeyboardInterrupt reports 130 as bash does for a
// foreground SIGINT death; exceptions print the plain traceback without
// xonsh's "Exception in thread" banner.

const commandFixture = `
import os, signal, sys
def hello(): print('hello')
def echo(*args): print(' '.join(args))
def argv(*args): return repr(list(args))
def text(): return 'hello\n'
def zero(): return 0
def status(code): return int(code)
def truth(): return True
def tuple3(): return (None, "I failed", 2)
def tuple_out(): return ("out", "err", 3)
def obj(): return 4.5
def stream():
    print('hallo on stream', file=sys.stderr)
    print('hallo on stream', file=sys.stdout)
    return 1
def big():
    for i in range(1000): print('x' * 100)
def system_exit(): sys.exit(42)
def system_exit_none(): sys.exit()
def system_exit_text(): sys.exit('usage: fixture')
def divide(): return 1/0
def interrupt(): raise KeyboardInterrupt
def sigint(): os.kill(os.getpid(), signal.SIGINT)
def sigterm(): os.kill(os.getpid(), signal.SIGTERM)
def slow():
    import time
    time.sleep(30)
count = 0
def bump():
    global count
    count += 1
    print(count)
value = 'not callable'
`

func commandModule(t *testing.T) *Module {
	t.Helper()
	dir := t.TempDir()
	writePython(t, filepath.Join(dir, "cmdfixture.py"), commandFixture)
	return fileImport(t, dir, "./cmdfixture.py", "fx")
}

func runCommand(t *testing.T, m *Module, name string, argv ...string) CommandResult {
	t.Helper()
	result, err := m.Command(context.Background(), name, argv, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

// xonsh "test calling a function alias" / "test ambiguous line
// continuations": argv reaches the function word for word and stdout is the
// function's print; the return value rules of parse_proxy_return.
func TestPythonCommandArgvAndReturnValues(t *testing.T) {
	m := commandModule(t)
	if got := runCommand(t, m, "hello"); got.Status != 0 || got.Stdout != "hello\n" || got.Stderr != "" {
		t.Fatalf("hello = %#v", got)
	}
	if got := runCommand(t, m, "echo", "--option1", "--option2"); got.Stdout != "--option1 --option2\n" {
		t.Fatalf("echo = %#v", got)
	}
	if got := runCommand(t, m, "echo", "missing", "EOL"); got.Stdout != "missing EOL\n" {
		t.Fatalf("echo = %#v", got)
	}
	// Words with spaces, empty words and globs are argv, never re-split.
	if got := runCommand(t, m, "argv", "a b", "", "*.tst", "42"); got.Stdout != "['a b', '', '*.tst', '42']" {
		t.Fatalf("argv = %#v", got)
	}
	cases := map[string]CommandResult{
		"text":      {Stdout: "hello\n"},
		"zero":      {},
		"truth":     {Status: 1},
		"tuple3":    {Stderr: "I failed\n", Status: 2},
		"tuple_out": {Stdout: "out", Stderr: "err\n", Status: 3},
		"obj":       {Stdout: "4.5"},
	}
	for name, want := range cases {
		if got := runCommand(t, m, name); got != want {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}
	if got := runCommand(t, m, "status", "7"); got.Status != 7 {
		t.Fatalf("status 7 = %#v", got)
	}
}

// xonsh "test uncaptured streaming alias": both streams carry their text and
// the return value is the status; "test output larger than most pipe
// buffers": 100,000 bytes arrive intact.
func TestPythonCommandStreamsAndLargeOutput(t *testing.T) {
	m := commandModule(t)
	if got := runCommand(t, m, "stream"); got.Stdout != "hallo on stream\n" || got.Stderr != "hallo on stream\n" || got.Status != 1 {
		t.Fatalf("stream = %#v", got)
	}
	want := strings.Repeat(strings.Repeat("x", 100)+"\n", 1000)
	if got := runCommand(t, m, "big"); got.Stdout != want || got.Status != 0 {
		t.Fatalf("big: %d bytes, status %d", len(got.Stdout), got.Status)
	}
}

// xonsh "test system exit in function alias": sys.exit(42) is status 42;
// ALIASES_THREADABLE_PRINT_CASES: an exception prints its traceback to stderr
// and the command reports 1, and the worker keeps serving afterwards.
func TestPythonCommandExitAndExceptions(t *testing.T) {
	m := commandModule(t)
	if got := runCommand(t, m, "system_exit"); got.Status != 42 || got.Stdout != "" || got.Stderr != "" {
		t.Fatalf("system_exit = %#v", got)
	}
	if got := runCommand(t, m, "system_exit_none"); got.Status != 0 {
		t.Fatalf("system_exit_none = %#v", got)
	}
	if got := runCommand(t, m, "system_exit_text"); got.Status != 1 || got.Stderr != "usage: fixture\n" {
		t.Fatalf("system_exit_text = %#v", got)
	}
	got := runCommand(t, m, "divide")
	if got.Status != 1 || got.Stdout != "" {
		t.Fatalf("divide = %#v", got)
	}
	if !regexp.MustCompile(`(?s)Traceback.*ZeroDivisionError: division by zero\n$`).MatchString(got.Stderr) {
		t.Fatalf("divide stderr = %q", got.Stderr)
	}
	if got := runCommand(t, m, "hello"); got.Stdout != "hello\n" {
		t.Fatalf("after exception hello = %#v", got)
	}
}

// Lookup failures are the shell's business (127/126), not Python failures.
func TestPythonCommandLookupFailures(t *testing.T) {
	m := commandModule(t)
	if _, err := m.Command(context.Background(), "nope", nil, nil); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := m.Command(context.Background(), "value", nil, nil); !errors.Is(err, ErrCommandNotCallable) {
		t.Fatalf("uncallable = %v", err)
	}
	if _, err := m.Command(context.Background(), "_private", nil, nil); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("private = %v", err)
	}
}

// Module state persists across commands: the worker is one process and a
// command is one call into it, not a fresh interpreter.
func TestPythonCommandKeepsModuleState(t *testing.T) {
	m := commandModule(t)
	for _, want := range []string{"1\n", "2\n", "3\n"} {
		if got := runCommand(t, m, "bump"); got.Stdout != want {
			t.Fatalf("bump = %#v, want %q", got, want)
		}
	}
	if got := runCommand(t, m, "echo", "still", "alive"); got.Stdout != "still alive\n" {
		t.Fatalf("echo = %#v", got)
	}
}

// A KeyboardInterrupt inside the call — a terminal Ctrl-C reaching the worker
// — ends the command with 130, bash's status for a foreground SIGINT death,
// without a traceback; the worker and its state survive.
func TestPythonCommandKeyboardInterrupt(t *testing.T) {
	m := commandModule(t)
	runCommand(t, m, "bump")
	if got := runCommand(t, m, "interrupt"); got.Status != 130 || got.Stderr != "" {
		t.Fatalf("interrupt = %#v", got)
	}
	if got := runCommand(t, m, "bump"); got.Stdout != "2\n" {
		t.Fatalf("state after interrupt: bump = %#v", got)
	}
}

// Cancellation (the shell's own interrupt path) kills the worker and reports
// the context error, never a WorkerExit.
func TestPythonCommandCancellation(t *testing.T) {
	m := commandModule(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := m.Command(ctx, "slow", nil, nil)
	var exit *WorkerExit
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &exit) {
		t.Fatalf("slow error = %v", err)
	}
	if got := runCommand(t, m, "hello"); got.Stdout != "hello\n" {
		t.Fatalf("restart after cancellation: %#v", got)
	}
}

func TestPythonCommandNativeWorkerExit(t *testing.T) {
	plan := pythonPlanWithRuntime(t, "def die() -> int:\n    import os\n    os._exit(3)\ndef alive() -> int:\n    return 7\n", Python{})
	m := Start(plan, Python{})
	defer m.Close()
	_, err := m.Call(t.Context(), "die")
	var exit *WorkerExit
	if !errors.As(err, &exit) || exit.Code != 3 || exit.Signal != 0 {
		t.Fatalf("worker exit = %#v, error = %v", exit, err)
	}
	result, err := m.Call(t.Context(), "alive")
	if err != nil || result.Value != int64(7) {
		t.Fatalf("restart = %+v, error = %v", result, err)
	}
}
