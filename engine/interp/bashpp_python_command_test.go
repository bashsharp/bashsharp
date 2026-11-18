package interp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B8 — an island function as a command word. Provenance: xonsh callable
// alias cases, see polyglot/python_command_test.go.

const commandFixture = `import os, signal, sys
def hello(): print('hello')
def echo(*args): print(' '.join(args))
def argv(*args): print(repr(list(args)))
def status(code): return int(code)
def stream():
    print('hallo on err', file=sys.stderr)
    print('hallo on out', file=sys.stdout)
    return 1
def big():
    for i in range(1000): print('x' * 100)
def system_exit(): sys.exit(42)
def divide(): return 1/0
def interrupt(): raise KeyboardInterrupt
def sigterm(): os.kill(os.getpid(), signal.SIGTERM)
count = 0
def bump():
    global count
    count += 1
    print(count)
value = 'not callable'
`

func commandProject(t *testing.T) (string, []string) {
	t.Helper()
	dir, environ := pythonProject(t)
	writeFile(t, filepath.Join(dir, "tools", "cmd.py"), commandFixture)
	return dir, environ
}

func runCommandScript(t *testing.T, dir string, environ []string, body string) (string, string, int) {
	t.Helper()
	return runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, "import python \"./tools/cmd.py\" as py\n"+body)
}

// xonsh "test calling a function alias": `f` prints hello. The command word
// form, an `alias` (the catalog's spelling) and a shell-function wrapper all
// reach the island with argv intact — words with spaces stay one word.
func TestBashPPPythonCommandArgv(t *testing.T) {
	dir, environ := commandProject(t)
	stdout, stderr, status := runCommandScript(t, dir, environ, `shopt -s expand_aliases
alias greet=py.echo
py.hello
greet --option1 --option2
greet missing EOL
wrap() { py.argv "$@"; }
wrap "a b" "" '*.tst' 42
words=(x y)
py.argv "${words[@]}"
echo "status=$?"
`)
	if status != 0 || stderr != "" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	want := "hello\n--option1 --option2\nmissing EOL\n['a b', '', '*.tst', '42']\n['x', 'y']\nstatus=0\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

// xonsh "test uncaptured streaming alias" and the redirect cases: what the
// function prints goes to the command's stdout and stderr, so shell
// redirections, 2>&1 and pipelines apply; the return value is the status.
// "test output larger than most pipe buffers": 100,000 bytes through a pipe.
func TestBashPPPythonCommandStreamsRedirectsAndPipes(t *testing.T) {
	dir, environ := commandProject(t)
	if _, err := exec.LookPath("tr"); err != nil {
		t.Skip("tr unavailable")
	}
	stdout, stderr, status := runCommandScript(t, dir, environ, `py.stream > out.txt 2> err.txt
echo "stream=$?"
py.stream 2>&1 | tr a-z A-Z
py.hello | tr a-z A-Z
py.big | wc -c | tr -d ' '
`)
	if status != 0 {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	// Output is captured per call and relayed stdout first, then stderr; the
	// relative order of the two streams inside one call is not preserved
	// (xonsh's own case notes the order is non-deterministic).
	want := "stream=1\nHALLO ON OUT\nHALLO ON ERR\nHELLO\n101000\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out.txt"))
	errText, _ := os.ReadFile(filepath.Join(dir, "err.txt"))
	if string(out) != "hallo on out\n" || string(errText) != "hallo on err\n" {
		t.Fatalf("redirected out=%q err=%q", out, errText)
	}
}

// Status: an int return is $? ("test system exit in function alias": 42);
// an exception prints its traceback and reports 1 while the script goes on
// (ALIASES_THREADABLE_PRINT_CASES); set -e honours the status like any
// command's; module state persists between commands.
func TestBashPPPythonCommandStatus(t *testing.T) {
	dir, environ := commandProject(t)
	stdout, stderr, status := runCommandScript(t, dir, environ, `py.status 7; echo "status=$?"
py.system_exit; echo "exit=$?"
echo f1f1f1 ; py.divide ; echo "f2f2f2=$?"
py.bump; py.bump; py.bump
if ! py.status 3; then echo "negated"; fi
py.status 0 && echo "and"
set -e
py.status 5
echo unreachable
`)
	if status != 5 {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	want := "status=7\nexit=42\nf1f1f1\nf2f2f2=1\n1\n2\n3\nnegated\nand\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if !regexp.MustCompile(`(?s)^Traceback.*ZeroDivisionError: division by zero\n$`).MatchString(stderr) {
		t.Fatalf("stderr = %q", stderr)
	}
}

// Lookup: a name the module lacks is the shell's command-not-found (127), a
// non-callable attribute cannot execute (126), `command NAME` bypasses the
// island (an external lookup that fails: 127, nothing printed by the island),
// and a shell function of the same word wins as it would over a builtin.
func TestBashPPPythonCommandLookup(t *testing.T) {
	dir, environ := commandProject(t)
	stdout, stderr, status := runCommandScript(t, dir, environ, `py.nope a b; echo "missing=$?"
py.value; echo "uncallable=$?"
command py.hello; echo "command=$?"
py.hello() { echo shadowed; }
py.hello; echo "function=$?"
unset -f py.hello
py.hello
`)
	if status != 0 {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	want := "missing=127\nuncallable=126\ncommand=127\nshadowed\nfunction=0\nhello\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	for _, line := range []string{"py.nope: command not found", "py.value: cannot execute", `"py.hello": executable file not found`} {
		if !strings.Contains(stderr, line) {
			t.Fatalf("stderr = %q, want %q", stderr, line)
		}
	}
}

// A KeyboardInterrupt inside the call — the worker's share of a terminal
// Ctrl-C — is status 130, printed nothing, and the worker keeps its state.
func TestBashPPPythonCommandInterrupt(t *testing.T) {
	dir, environ := commandProject(t)
	stdout, stderr, status := runCommandScript(t, dir, environ, `py.bump
py.interrupt; echo "interrupt=$?"
py.bump
`)
	if status != 0 || stderr != "" || stdout != "1\ninterrupt=130\n2\n" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
}

// A source-block island's exported function is a command word too, through
// its block alias.
func TestBashPPPythonCommandFromSourceBlock(t *testing.T) {
	dir, environ := pythonProject(t)
	source := "~~~python as isl\ndef shout(*words):\n    print(' '.join(words).upper())\n    return 3\n~~~\nisl.shout hello world; echo \"status=$?\"\n"
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, source)
	if status != 0 || stderr != "" || stdout != "HELLO WORLD\nstatus=3\n" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
}

// Classic dialects are untouched: `py.hello` in a bash script is an external
// command lookup, and no Python environment is discovered.
func TestBashPPPythonCommandClassicUnchanged(t *testing.T) {
	dir, environ := commandProject(t)
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.sh"), dir, environ, "py.hello; echo \"status=$?\"\n")
	if status != 0 || stdout != "status=127\n" || !strings.Contains(stderr, `"py.hello": executable file not found`) {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
}
