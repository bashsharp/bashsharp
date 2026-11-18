//go:build full

package interp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPPythonHandleIterator(t *testing.T) {
	source, err := os.ReadFile("../internal/testdata/python-iterators.bpp")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../internal/testdata/python-iterators.stdout")
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, status := runBashPP(t, filepath.Join(t.TempDir(), "iterators.bpp"), t.TempDir(), os.Environ(), string(source))
	if status != 0 || stderr != "" || out != string(want) {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, out, stderr)
	}
}

func TestBashPPPythonUsesSourceEnvironmentPlan(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	launcher := internal.PythonTestLauncher(t, python, filepath.Join(root, "planned-python"), "yes")
	if err := os.WriteFile(filepath.Join(root, "bashpp.yaml"), []byte("runtime: "+filepath.Base(launcher)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "program.bpp")
	source := "~~~python\ndef planned() -> str:\n    import os\n    return os.environ.get('BASHPP_SELECTED_RUNTIME', '') + ':' + os.getcwd()\n~~~\nvalue := planned()\necho \"$value\"\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), name)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	// The runtime comes from the source's environment; the call runs in the
	// shell's directory, like any other command on that line.
	caller := t.TempDir()
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(caller), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	canonicalCaller, err := filepath.EvalSymlinks(caller)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), file); err != nil || stderr.Len() != 0 || stdout.String() != "yes:"+canonicalCaller+"\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
}

// A Python fence call runs in the calling shell's current directory, not the
// project root its environment was discovered from: relative paths inside
// the fence follow a cd, the way they would for any command (todo 7ebb8f28).
func TestBashPPPythonCallFollowsShellCwd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "data.txt"), []byte("here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(sub, "program.bsh")
	source := "~~~py as p\ndef read(path: str) -> str:\n    return open(path).read().strip()\ndef cwd() -> str:\n    import os\n    return os.getcwd()\n~~~\n" +
		"v := p.read(data.txt)\necho \"$v\"\ncd ..\nc := p.cwd()\necho \"$c\"\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), name)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(sub), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(root, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), file); err != nil || stderr.Len() != 0 || stdout.String() != "here\n"+parent+"\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
}

func TestBashPPNonPythonDoesNotDiscoverEnvironment(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("echo ok\n"), filepath.Join(t.TempDir(), "missing", "script.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	env := expand.ListEnviron("BASHPP_PYTHON=/definitely/missing", "PATH="+os.Getenv("PATH"))
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Env(env), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), file); err != nil || stderr.Len() != 0 || stdout.String() != "ok\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
}

func runPolyglot(t *testing.T, source string) (string, string, error) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "polyglot.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(t.Context(), file)
	return stdout.String(), stderr.String(), err
}

func requireRustToolchain(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"rustc", "cargo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
}

func TestBashPPRustDirectAndQualifiedCalls(t *testing.T) {
	requireRustToolchain(t)
	direct := `~~~rust
pub fn add(a: i64, b: i64) -> i64 { println!("rust"); a + b }
~~~
x := add(20, 22)
echo "x=$x"
`
	out, diagnostic, err := runPolyglot(t, direct)
	if err != nil || out != "rust\nx=42\n" || diagnostic != "" {
		t.Fatalf("direct: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	qualified := `~~~rs as native
pub fn greet(name: &str) -> String { format!("hello {name}") }
~~~
value := native.greet(world)
echo "$value"
`
	out, diagnostic, err = runPolyglot(t, qualified)
	if err != nil || out != "hello world\n" || diagnostic != "" {
		t.Fatalf("qualified: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

// Ordinary serde structs and Vec<Struct> cross the Bash# Object mapping, the
// one worker keeps state across calls, and island stdout/stderr stay on the
// shell's streams. Fixture shapes follow serde_json v1.0.151 tests/test.rs
// `test_parse_struct` (MIT OR Apache-2.0); see polyglot.TestRustSerdeStructsPersistentWorker.
func TestBashPPRustSerdeStructObjectRoundTrip(t *testing.T) {
	requireRustToolchain(t)
	out, diagnostic, err := runPolyglot(t, rustSerdeStructScript)
	if err != nil || out != rustSerdeStructStdout || diagnostic != rustSerdeStructStderr {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	// Failure: a value that is not the struct is serde's own diagnostic, and
	// the worker survives it.
	out, diagnostic, err = runPolyglot(t, rustSerdeStructFence+`
sum := rs.total('"hello"')
echo "status=$?"
n := rs.bump()
echo "n=$n"
`)
	if err != nil || out != "status=2\nn=1\n" || !strings.Contains(diagnostic, `invalid type: string "hello", expected struct Outer`) {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

const rustSerdeStructFence = `~~~rust as rs
use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize)]
pub struct Inner { pub b: usize, pub c: Vec<String> }

#[derive(Serialize, Deserialize)]
pub struct Outer { pub inner: Vec<Inner> }

pub fn make(b: usize) -> Outer { println!("made"); Outer { inner: vec![Inner { b, c: vec!["abc".into(), "xyz".into()] }] } }
pub fn total(outer: Outer) -> usize { eprintln!("totalling"); outer.inner.iter().map(|inner| inner.b).sum() }
pub fn bounce(outers: Vec<Outer>) -> Vec<Outer> { outers }
pub fn empty() -> Vec<Outer> { Vec::new() }
pub fn bump() -> i64 {
    static COUNT: std::sync::atomic::AtomicI64 = std::sync::atomic::AtomicI64::new(0);
    COUNT.fetch_add(1, std::sync::atomic::Ordering::SeqCst) + 1
}
~~~
`

const rustSerdeStructScript = rustSerdeStructFence + `
value := rs.make(2)
sum := rs.total(value)
none := rs.empty()
again := rs.bounce(none)
a := rs.bump()
b := rs.bump()
echo "$value|$sum|$none|$again|$a$b"
`

const (
	rustSerdeStructStdout = "made\n{\"inner\":[{\"b\":2,\"c\":[\"abc\",\"xyz\"]}]}|2|[]|[]|12\n"
	rustSerdeStructStderr = "totalling\n"
)

func TestBashPPGoDirectAndQualifiedCalls(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	direct := `~~~go
import "fmt"
func Add(a int64, b int64) int64 { fmt.Println("go"); return a+b }
~~~
x := Add(20, 22)
echo "x=$x"
`
	out, diagnostic, err := runPolyglot(t, direct)
	if err != nil || out != "go\nx=42\n" || diagnostic != "" {
		t.Fatalf("direct: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	qualified := `~~~go as go
func Greet(name string) string { return "hello "+name }
~~~
value := go.Greet(world)
echo "$value"
`
	out, diagnostic, err = runPolyglot(t, qualified)
	if err != nil || out != "hello world\n" || diagnostic != "" {
		t.Fatalf("qualified: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPGoLibraryPackageExposesExports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	source := `~~~go as library
package library

func Exported(value string) string { return "exported:" + value }
~~~
value := library.Exported(ok)
echo "$value"
`
	out, diagnostic, err := runPolyglot(t, source)
	if err != nil || out != "exported:ok\n" || diagnostic != "" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPGoMainFenceRunsProgramContract(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	source := `~~~go
package main
import (
	"bufio"
	"fmt"
	"os"
)
func main() {
	in, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintf(os.Stdout, "stdout:%s:%s", os.Args[1], in)
	fmt.Fprintln(os.Stderr, "stderr")
	os.Exit(23)
}
~~~
echo unreachable
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "polyglot.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Params("--", "arg"), interp.StdIO(strings.NewReader("input\n"), &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(t.Context(), file)
	status, ok := interp.IsExitStatus(err)
	if !ok || status != 23 || stdout.String() != "stdout:arg:input\n" || stderr.String() != "stderr\n" {
		t.Fatalf("status=%d ok=%v stdout=%q stderr=%q err=%v", status, ok, stdout.String(), stderr.String(), err)
	}
}

func TestBashPPShellDialectIslands(t *testing.T) {
	bashSource := `~~~bash as island
var() { printf '%s:%s:%s' "$1" "$2" "$3"; }
Show() { counter=$(( ${counter:-0} + 1 )); var "$1" = "$counter"; }
~~~
counter=99
first := island.Show(alpha)
second := island.Show(alpha)
printf '%s|%s|%s\n' "$first" "$second" "$counter"
`
	out, diagnostic, err := runPolyglot(t, bashSource)
	if err != nil || out != "alpha:=:1|alpha:=:1|99\n" || diagnostic != "" {
		t.Fatalf("bash island: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}

	shSource := `~~~sh as posix
Join() { printf '%s/%s/%s' "$#" "$1" "$2"; }
~~~
value := posix.Join("a b", c)
echo "$value"
`
	out, diagnostic, err = runPolyglot(t, shSource)
	if err != nil || out != "2/a b/c\n" || diagnostic != "" {
		t.Fatalf("POSIX island: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPShellDialectIslandStatusIsCallError(t *testing.T) {
	source := `~~~sh as posix
Fail() { echo detail >&2; return 7; }
~~~
value := posix.Fail()
`
	out, diagnostic, err := runPolyglot(t, source)
	if err == nil || out != "" || !strings.Contains(diagnostic, "detail") || !strings.Contains(diagnostic, "status 7") {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPCAndCPPDirectAndQualifiedCalls(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang unavailable")
	}
	direct := `~~~c
#include <stdint.h>
int64_t add(int64_t a, int64_t b) { return a + b; }
~~~
x := add(20, 22)
echo "c=$x"
`
	out, diagnostic, err := runPolyglot(t, direct)
	if err != nil || out != "c=42\n" || diagnostic != "" {
		t.Fatalf("C: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	qualified := `~~~cxx as native
#include <string>
std::string greet(const std::string& name) { return "hello "+name; }
~~~
value := native.greet(world)
echo "$value"
`
	out, diagnostic, err = runPolyglot(t, qualified)
	if err != nil || out != "hello world\n" || diagnostic != "" {
		t.Fatalf("C++: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPPythonSourcedFileIsolation(t *testing.T) {
	child := filepath.Join(t.TempDir(), "child.bpp")
	err := os.WriteFile(child, []byte("~~~python\ndef answer() -> int:\n    return 2\n~~~\nchildX := answer()\necho child=$childX\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	outer := "~~~python\ndef answer() -> int:\n    return 1\n~~~\n. " + strconv.Quote(child) + "\nx := answer()\necho outer=$x\n"
	out, diagnostic, err := runPolyglot(t, outer)
	if err != nil || out != "child=2\nouter=1\n" || diagnostic != "" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPPythonDirectAndQualifiedCalls(t *testing.T) {
	direct := `~~~python
def add(a: int, b: int) -> int:
    return a+b
def announce(value: str) -> None:
    print(value)
def answer() -> int:
    return 42
~~~
x := add(2, 3)
echo "x=$x"
announce(done)
y := answer()
echo "y=$y"
`
	out, diagnostic, err := runPolyglot(t, direct)
	if err != nil || out != "x=5\ndone\ny=42\n" || diagnostic != "" {
		t.Fatalf("direct: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}

	qualified := `~~~python as py
def add(a: int, b: int) -> int:
    return a+b
~~~
x := py.add(4, 5)
echo "x=$x"
`
	out, diagnostic, err = runPolyglot(t, qualified)
	if err != nil || out != "x=9\n" || diagnostic != "" {
		t.Fatalf("qualified: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

// `~~~py` is an alias spelling of `~~~python`: both the aliased and the naked
// forms run through the same Python module plan, and a `py` alias on a `py`
// fence is the documented `py.main()` shape.
func TestBashPPPythonPyAliasSpelling(t *testing.T) {
	qualified := `~~~py as py
def main() -> str:
    return "launched"
~~~
value := py.main()
echo "value=$value"
`
	out, diagnostic, err := runPolyglot(t, qualified)
	if err != nil || out != "value=launched\n" || diagnostic != "" {
		t.Fatalf("qualified: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}

	naked := `~~~py
def answer() -> int:
    return 42
~~~
x := answer()
echo "x=$x"
`
	out, diagnostic, err = runPolyglot(t, naked)
	if err != nil || out != "x=42\n" || diagnostic != "" {
		t.Fatalf("naked: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}

	// One module per language per source unit: a `py` block and a `python`
	// block are the SAME language, so they must agree on the alias.
	mixed := `~~~python as py
def one() -> int:
    return 1
~~~
~~~py
def two() -> int:
    return 2
~~~
echo unreachable
`
	if _, _, err := runPolyglot(t, mixed); err == nil || !strings.Contains(err.Error(), "inconsistent aliases") {
		t.Fatalf("mixed spellings with different aliases: err=%v", err)
	}
}

func TestBashPPPythonDynamicAndCollisions(t *testing.T) {
	dynamic := `~~~python
def loose(value):
    return value+"!"
def fail():
    raise ValueError("boom")
~~~
value, callErr := loose(ok)
echo "$value:${callErr:+error}"
failed, failErr := fail()
echo "$failErr"
`
	out, diagnostic, err := runPolyglot(t, dynamic)
	if err != nil || out != "ok!:\nValueError: boom\n" || diagnostic != "" {
		t.Fatalf("dynamic: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}

	collisions := []string{`~~~python
def same(a: int) -> int:
    return a
~~~
func same(a int) int { return a }
	`, `same() { :; }
~~~python
def same(a: int) -> int:
    return a
~~~
`, `~~~python
def same(a: int) -> int:
    return a
~~~
var same = 1
`}
	for _, collision := range collisions {
		_, _, err = runPolyglot(t, collision)
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("collision error = %v for %q", err, collision)
		}
	}
}

func TestBashPPPythonModuleIsNotShellState(t *testing.T) {
	source := `~~~python as py
def value() -> int:
    return 1
~~~
env | grep '^py=' || true
`
	out, _, err := runPolyglot(t, source)
	if err != nil || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashPPPythonStructuredObjectRoundTrip(t *testing.T) {
	source := `~~~python
def record() -> dict[str, list[int]]:
    return {"items": [3, 4]}
def bounce(value: dict[str, list[int]]) -> dict[str, list[int]]:
    return value
~~~
value := record()
again := bounce(value)
echo "$value|$again"
`
	out, diagnostic, err := runPolyglot(t, source)
	if err != nil || out != "{\"items\":[3,4]}|{\"items\":[3,4]}\n" || diagnostic != "" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPTypeScriptDirectQualifiedAndCoexistence(t *testing.T) {
	if os.Getenv("BASHPP_TYPESCRIPT_MODULE") == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to an official TypeScript compiler module")
	}
	source := `~~~python as py
def twice(value: int) -> int:
    return value * 2
~~~
~~~typescript
interface Label { value: string }
export function add(a: number, b: number): number { console.log("ts"); return a + b }
function answer(): number { return 42 }
~~~
x := add(20, 22)
y := answer()
z := py.twice(3)
echo "$x:$y:$z"
`
	out, diagnostic, err := runPolyglot(t, source)
	if err != nil || out != "ts\n42:42:6\n" || diagnostic != "" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPPythonModuleLevelImportsAndConstants(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	source := `~~~python
import json
from math import sqrt
SCALE = 3
NAME: str = "fence"
def scaled(n: int) -> int:
    return n * SCALE
def describe(value: int) -> str:
    return json.dumps({"name": NAME, "root": sqrt(value)}, sort_keys=True)
~~~
x := scaled(14)
y := describe(16)
echo "$x"
echo "$y"
`
	out, diagnostic, err := runPolyglot(t, source)
	if err != nil || out != "42\n{\"name\": \"fence\", \"root\": 4.0}\n" || diagnostic != "" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	_, diagnostic, err = runPolyglot(t, "~~~python\nimport bashpp_missing_package\ndef f() -> int:\n    return 1\n~~~\nx := f()\n")
	if err == nil || !strings.Contains(diagnostic, "bashpp_missing_package") {
		t.Fatalf("missing package: diagnostic=%q err=%v", diagnostic, err)
	}
}

func TestBashPPTypeScriptAndJavaScriptNodeImports(t *testing.T) {
	if os.Getenv("BASHPP_TYPESCRIPT_MODULE") == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to an official TypeScript compiler module")
	}
	source := `~~~typescript as ts
import { basename } from "node:path"
import os from "os"
const suffix = "!"
export function base(path: string): string { return basename(path) + suffix }
export function platform(): boolean { return typeof os.platform() === "string" }
~~~
~~~js as js
import { join } from "node:path"
/** @param {string} a @param {string} b @returns {string} */
export function joined(a, b) { return join(a, b) }
function loose(value) { return Number(value) + 1 }
~~~
x := ts.base("a/b.txt")
p := ts.platform()
y := js.joined("a", "b")
z, zErr := js.loose(41)
echo "$x:$p:$y:$z:${zErr:+failed}"
`
	out, diagnostic, err := runPolyglot(t, source)
	want := "b.txt!:true:" + filepath.Join("a", "b") + ":42:\n"
	if err != nil || out != want || diagnostic != "" {
		t.Fatalf("out=%q want=%q diagnostic=%q err=%v", out, want, diagnostic, err)
	}
}

func TestBashPPGoVoidCalls(t *testing.T) {
	out, diagnostic, err := runPolyglot(t, `~~~go as g
import "fmt"
func Main() { fmt.Println("hello world") }
func Partial() { fmt.Print("no newline") }
func Checked(fail bool) error {
	fmt.Println("checked")
	if fail { return fmt.Errorf("void failure") }
	return nil
}
~~~
g.Main()
g.Main()
g.Partial()
g.Checked(false)
g.Checked(true)
echo "status=$?"
g.Main()
`)
	if want := "hello world\nhello world\nno newlinechecked\nchecked\nstatus=1\nhello world\n"; err != nil || out != want || diagnostic != "bash++: foreign call g.Checked failed: void failure\n" {
		t.Fatalf("out=%q diagnostic=%q err=%v, want %q", out, diagnostic, err, want)
	}
}

func TestBashPPGoModuleInternalFunctions(t *testing.T) {
	for _, tc := range []struct{ declaration, call string }{
		{"func SlicesIndex[S ~[]E, E comparable](s S, v E) int { for i := range s { if s[i] == v { return i } }; return -1 }", "SlicesIndex([]string{\"a\", \"b\"}, \"b\")"},
		{"func Variadic(v ...int) int { return len(v) }", "Variadic(1)"},
		{"func Unsupported(v []int) int { return len(v) }", "Unsupported([]int{1})"},
	} {
		out, diagnostic, err := runPolyglot(t, "~~~go as g\nimport \"fmt\"\n"+tc.declaration+"\nfunc Main() { fmt.Println("+tc.call+") }\n~~~\ng.Main()\n")
		if err != nil || diagnostic != "" || out != "1\n" {
			t.Errorf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
		}
	}
}

func TestBashPPPythonFenceHandleMethods(t *testing.T) {
	source, err := os.ReadFile("../internal/testdata/python-handles.bpp")
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(string(source)), filepath.Join(t.TempDir(), "handles.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), file); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	want, err := os.ReadFile("../internal/testdata/python-handles.stdout")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) || stderr.String() != "method-err\n" {
		t.Fatalf("out=%q stderr=%q", out.String(), stderr.String())
	}
}
