//go:build full

package lower_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestPythonFenceInterpretedNativeParity(t *testing.T) {
	tests := map[string]string{
		"typed direct": `~~~python
def add(a: int, b: int) -> int:
    print("python")
    return a+b
~~~
x := add(2, 3)
echo "x=$x"
`,
		"typed qualified": `~~~python as py
def add(a: int, b: int) -> int:
    return a+b
~~~
x := py.add(4, 5)
echo "x=$x"
`,
		"dynamic result": `~~~python
def loose(value):
    return value+"!"
~~~
x, callErr := loose(ok)
echo "$x:$callErr"
`,
		"dynamic structured error": `~~~python
def fail():
    raise ValueError("boom")
~~~
value, callErr := fail()
echo "$value:$callErr"
`,
		"py alias launcher": `~~~py as py
def main() -> str:
    return "launched"
~~~
value := py.main()
echo "value=$value"
`,
		"command form": `~~~python as py
def greet(name: str) -> int:
    print("hello " + name)
    return 0
~~~
py.greet world
echo "status=$?"
`,
		"structured object round trip": `~~~python
def record() -> dict[str, list[int]]:
    return {"items": [3, 4]}
def bounce(value: dict[str, list[int]]) -> dict[str, list[int]]:
    return value
~~~
value := record()
again := bounce(value)
echo "$value|$again"
`,
		"structured object qualified": `~~~python as py
def record() -> dict[str, list[int]]:
    return {"items": [3, 4]}
~~~
value := py.record()
echo "$value"
`,
		"module imports and constants": `~~~python as py
import math
from os import path
LIMIT = 40
NAME = "n"
def total(a: int) -> int:
    return a + LIMIT + int(math.floor(2.5)) + len(path.join("a", "b"))
~~~
x := py.total(0)
echo "x=$x"
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) { testPythonFenceInterpretedNativeParity(t, source) })
	}
}

func TestTypeScriptFenceInterpretedNativeParity(t *testing.T) {
	if os.Getenv("BASHPP_TYPESCRIPT_MODULE") == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to an official TypeScript compiler module")
	}
	testPythonFenceInterpretedNativeParity(t, `~~~typescript as ts
type Numeric = number
export function add(a: Numeric, b: Numeric): number { console.log("typescript"); return a + b }
~~~
x := ts.add(20, 22)
echo "x=$x"
`)
}

func TestJavaScriptFenceInterpretedNativeParity(t *testing.T) {
	if os.Getenv("BASHPP_TYPESCRIPT_MODULE") == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to an official TypeScript compiler module")
	}
	testPythonFenceInterpretedNativeParity(t, `~~~javascript as js
import path from "node:path"
/** @param {number} a @param {number} b @returns {number} */
export function add(a, b) { return a + b + path.join("a", "b").length - 3 }
~~~
x := js.add(20, 22)
echo "x=$x"
`)
}

func requireRustToolchain(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"rustc", "cargo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
}

func TestRustFenceInterpretedNativeParity(t *testing.T) {
	requireRustToolchain(t)
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~rust as rs
pub fn add(a: i64, b: i64) -> i64 { println!("rust"); a + b }
pub fn checked(value: i64) -> Result<i64, String> { if value < 0 { Err("negative".into()) } else { Ok(value) } }
~~~
x := rs.add(20, 22)
echo "x=$x"
`, "input.bpp")
	if got != "rust\nx=42\n" {
		t.Fatalf("output = %q", got)
	}
}

// Ordinary serde structs and Vec<Struct> cross the Object mapping identically
// in the interpreter and the lowered program, island stdout/stderr included,
// and the one persistent worker keeps its state in both. Fixture shapes follow
// serde_json v1.0.151 tests/test.rs `test_parse_struct` (MIT OR Apache-2.0).
func TestRustFenceSerdeStructParity(t *testing.T) {
	requireRustToolchain(t)
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~rust as rs
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
value := rs.make(2)
sum := rs.total(value)
none := rs.empty()
again := rs.bounce(none)
a := rs.bump()
b := rs.bump()
echo "$value|$sum|$none|$again|$a$b"
`, "input.bpp")
	if got != "made\n{\"inner\":[{\"b\":2,\"c\":[\"abc\",\"xyz\"]}]}|2|[]|[]|12\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestGoFenceInterpretedNativeParity(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~go as go
import "fmt"
func Add(a int64, b int64) int64 { fmt.Println("go"); return a+b }
func Checked(value int64) (int64, error) { if value < 0 { return 0, fmt.Errorf("negative") }; return value, nil }
~~~
x := go.Add(20, 22)
echo "x=$x"
`, filepath.Join("testdata", "input.bpp"))
	if got != "go\nx=42\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestGoCommandInterpretedNativeParity(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~go as go
import ("fmt"; "os")
func Show() { fmt.Printf("argv=%q\n", os.Args[1:]) }
func Exit() { os.Exit(3) }
func Value() string { return "typed" }
~~~
go.Show one "two words"
go.Exit
echo "status=$?"
value := go.Value()
echo "value=$value"
`, filepath.Join("testdata", "input.bpp"))
	if got != "argv=[\"one\" \"two words\"]\nstatus=3\nvalue=typed\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestGoCommandUsesCurrentShellContextInBothModes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.local/go-command-context\n\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "sub")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~go as go
import ("fmt"; "os"; "path/filepath")
func Show() { wd, _ := os.Getwd(); fmt.Printf("%s:%s\n", filepath.Base(wd), os.Getenv("BPP_GO_CALL_CONTEXT")) }
~~~
if true; then export BPP_GO_CALL_CONTEXT=first; fi
go.Show
cd sub
if true; then export BPP_GO_CALL_CONTEXT=second; fi
go.Show
`, filepath.Join(root, "entry.bpp"))
	if got != ""+filepath.Base(root)+":first\nsub:second\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestShellFenceInterpretedNativeParity(t *testing.T) {
	for name, source := range map[string]string{
		"bash": `~~~bash as island
Show() { local value=$1; printf 'bash:%s:%s' "$#" "$value"; }
~~~
value := island.Show("a b", c)
echo "$value"
`,
		"posix": `~~~sh as island
Show() { value=$1; printf 'posix:%s:%s' "$#" "$value"; }
~~~
value := island.Show("a b", c)
echo "$value"
`,
	} {
		t.Run(name, func(t *testing.T) {
			got := testPythonFenceInterpretedNativeParityAt(t, source, filepath.Join("testdata", "input.bpp"))
			if want := name + ":2:a b\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}

func TestCAndCPPFenceInterpretedNativeParity(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang unavailable")
	}
	for name, source := range map[string]string{
		"c": `~~~c as native
#include <stdint.h>
int64_t add(int64_t a, int64_t b) { return a+b; }
~~~
x := native.add(20, 22)
echo "c=$x"
`,
		"cpp": `~~~cpp as native
#include <string>
std::string greet(const std::string& name) { return "hello "+name; }
~~~
value := native.greet(world)
echo "$value"
`,
	} {
		t.Run(name, func(t *testing.T) { testPythonFenceInterpretedNativeParity(t, source) })
	}
}

func TestPythonFenceUsesSourceEnvironmentPlan(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	launcher := internal.PythonTestLauncher(t, python, filepath.Join(root, "planned-python"), "yes")
	if err := os.WriteFile(filepath.Join(root, "bashpp.yaml"), []byte("runtime: "+filepath.Base(launcher)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := "~~~python\ndef planned() -> str:\n    import os\n    return os.environ.get('BASHPP_SELECTED_RUNTIME', '') + ':' + os.getcwd()\n~~~\nvalue := planned()\necho \"$value\"\n"
	got := testPythonFenceInterpretedNativeParityAt(t, source, filepath.Join(root, "program.bpp"))
	if got != "yes:"+canonicalRoot+"\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestPythonFenceAbsoluteArtifactCwdParity(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := "~~~python\ndef cwd() -> str:\n    import os\n    return os.getcwd()\n~~~\nvalue := cwd()\necho \"$value\"\n"
	got := testPythonFenceInterpretedNativeParityAt(t, source, filepath.Join(dir, "entry.bpp"))
	if got != canonicalDir+"\n" {
		t.Fatalf("output = %q, want %q", got, canonicalDir+"\n")
	}
}

func TestLowerNonPythonDoesNotDiscoverEnvironment(t *testing.T) {
	t.Setenv("BASHPP_PYTHON", filepath.Join(t.TempDir(), "missing-python"))
	file := parse(t, "echo ok\n", filepath.Join(t.TempDir(), "missing", "input.bpp"))
	if _, err := lower.Compile(file, lower.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestPythonFenceEnvironmentUsesOrigin(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	launcher := internal.PythonTestLauncher(t, python, filepath.Join(root, "origin-python"), "")
	if err := os.WriteFile(filepath.Join(root, "bashpp.yaml"), []byte("runtime: "+filepath.Base(launcher)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(root, "program.bpp")
	if err := os.WriteFile(origin, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	file := parse(t, "~~~python\ndef answer() -> int:\n    return 42\n~~~\n", "<stdin>")
	result, err := lower.Compile(file, lower.Options{Origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalLauncher, err := filepath.EvalSymlinks(launcher)
	if err != nil {
		t.Fatal(err)
	}
	generated := string(result.Source)
	if !strings.Contains(generated, "Dir: "+strconv.Quote(canonicalRoot)) || !strings.Contains(generated, "Executable: "+strconv.Quote(canonicalLauncher)) {
		t.Fatalf("generated plan did not use Origin project:\n%s", generated)
	}
}

func testPythonFenceInterpretedNativeParity(t *testing.T, source string) {
	testPythonFenceInterpretedNativeParityAt(t, source, "input.bpp")
}

func testPythonFenceInterpretedNativeParityAt(t *testing.T, source, filename string, buildFlags ...string) string {
	t.Helper()
	return testForeignParityArtifactAt(t, source, filename, "", buildFlags...)
}

func testForeignParityArtifactAt(t *testing.T, source, filename, nativePostlude string, buildFlags ...string) string {
	t.Helper()
	stdout, _ := testForeignParityArtifactEnvironment(t, source, filename, nativePostlude, nil, 0, buildFlags...)
	return stdout
}

func testForeignParityArtifactEnvironment(t *testing.T, source, filename, nativePostlude string, compileEnv map[string]string, expectedExit int, buildFlags ...string) (string, string) {
	t.Helper()
	file := parse(t, source, filename)
	var result *lower.Result
	var err error
	compile := func(t *testing.T) {
		for key, value := range compileEnv {
			t.Setenv(key, value)
		}
		result, err = lower.Compile(file, lower.Options{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if compileEnv == nil {
		compile(t)
	} else if !t.Run("build-environment", compile) {
		t.FailNow()
	}
	if !strings.Contains(string(result.Source), "polyglot.Start") {
		t.Fatalf("generated source omitted module:\n%s", result.Source)
	}

	var interpreted, interpretedErr bytes.Buffer
	targetDir := filepath.Dir(filename)
	var runnerOpts []interp.RunnerOption
	runnerOpts = append(runnerOpts, interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &interpreted, &interpretedErr))
	if filepath.IsAbs(targetDir) {
		runnerOpts = append(runnerOpts, interp.Dir(targetDir))
	}
	runner, err := interp.New(runnerOpts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	runErr := runner.Run(ctx, file)
	status := 0
	if runErr != nil {
		if code, ok := interp.IsExitStatus(runErr); ok {
			status = int(code)
		} else {
			t.Fatal(runErr)
		}
	}
	if status != expectedExit {
		t.Fatalf("interpreted status=%d want=%d: %s", status, expectedExit, interpretedErr.String())
	}

	dir := t.TempDir()
	generated := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(generated, append(result.Source, []byte(nativePostlude)...), 0600); err != nil {
		t.Fatal(err)
	}
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(this)))
	module := "module polyglotfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "program")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildArgs := append([]string{"build", "-mod=mod"}, buildFlags...)
	buildArgs = append(buildArgs, "-o", binary, "generated.go")
	// Compilation must not consume the interpreted or native execution deadline.
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	cmd := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), buildArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, output, result.Source)
	}
	nativeCtx, cancelNative := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancelNative()
	cmd = exec.CommandContext(nativeCtx, binary)
	if filepath.IsAbs(targetDir) {
		cmd.Dir = targetDir
	} else {
		cmd.Dir = t.TempDir()
	}
	var native, nativeErr bytes.Buffer
	cmd.Stdout = &native
	cmd.Stderr = &nativeErr
	runErr = cmd.Run()
	status = 0
	if runErr != nil {
		if exit, ok := runErr.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else {
			t.Fatal(runErr)
		}
	}
	if status != expectedExit {
		t.Fatalf("compiled status=%d want=%d: %s", status, expectedExit, nativeErr.String())
	}
	if native.String() != interpreted.String() || nativeErr.String() != interpretedErr.String() {
		t.Fatalf("interpreted=(%q,%q) native=(%q,%q)", interpreted.String(), interpretedErr.String(), native.String(), nativeErr.String())
	}
	return native.String(), nativeErr.String()
}

// requirePowerShell gates the PowerShell and C# parity tests on a real pwsh 7:
// both fences run through it (C# compiles via Add-Type), in the interpreter
// and in the lowered binary alike. The cross-OS proof is the sprint's shared
// host evidence; BASHPP_PWSH names a provisioned runtime explicitly.
func requirePowerShell(t *testing.T) {
	t.Helper()
	if command := os.Getenv("BASHPP_PWSH"); command != "" {
		if _, err := exec.LookPath(command); err == nil {
			return
		}
	}
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh unavailable")
	}
}

func TestPowerShellFenceInterpretedNativeParity(t *testing.T) {
	requirePowerShell(t)
	bin := t.TempDir()
	internal.PowerShellTestSort(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tests := map[string]struct{ source, want string }{
		// A Verb-Noun export is no Bash# call name; it lowers as a fence-local
		// helper and the program still builds.
		"typed qualified": {`~~~powershell as ps
function Add-Numbers {
    [OutputType([long])]
    param([long]$a, [long]$b)
    $a + $b
}
function Add {
    [OutputType([long])]
    param([long]$a, [long]$b)
    Write-Host "powershell"
    Add-Numbers $a $b
}
~~~
x := ps.Add(20, 22)
echo "x=$x"
`, "powershell\nx=42\n"},
		"typed direct state": {`~~~pwsh
$script:calls = 0
function Bump {
    [OutputType([long])]
    param()
    $script:calls++
    $script:calls
}
function Greet {
    [OutputType([string])]
    param([string]$name)
    "hello $name"
}
~~~
a := Bump()
b := Bump()
g := Greet("a b")
echo "$a$b|$g"
`, "12|hello a b\n"},
		"structured object round trip": {`~~~powershell as ps
function Make {
    [OutputType([hashtable])]
    param([long]$n)
    @{ items = @($n, ($n + 1)) }
}
function Bounce {
    [OutputType([hashtable])]
    param([hashtable]$value)
    $value
}
~~~
value := ps.Make(3)
again := ps.Bounce(value)
echo "$value|$again"
`, `{"items":[3,4]}|{"items":[3,4]}` + "\n"},
		// The command form: argv words, the success stream as bytes on
		// stdout, a pipeline-reading function as a stdin filter, and a
		// terminating error as status 1 — the same in a lowered region.
		"command form": {`~~~powershell as ps
function Greet {
    param([string]$name)
    "hello $name"
}
function Shout {
    process { $_.ToUpper() }
}
function Fail { throw 'nope' }
~~~
ps.Greet world
printf 'beta\nalpha\n' | ps.Shout | sort
ps.Fail 2>/dev/null
echo "failed=$?"
`, "hello world\nALPHA\nBETA\nfailed=1\n"},
		"dynamic structured error": {`~~~powershell as ps
function Fail { throw "boom" }
~~~
value, callErr := ps.Fail()
echo "$value:$callErr"
`, ":RuntimeException: boom\n"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := testPythonFenceInterpretedNativeParityAt(t, tc.source, "input.bsh")
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// The sprint-358 both-fence smoke fixture, verbatim: one program carrying a
// PowerShell fence and a C# fence, a foreign double result interpolated as a
// word ($half), the checked error form's ${failErr:+error} expansion, and a
// final shell region matching the foreign error's text. The interpreted
// stdout is pinned by SHA-256
// 5e0a962c1b15a14dd48f33eb19e9144f08441c667a102bf479c638fd2df973c3; the
// lowered binary must reproduce stdout, stderr and exit byte for byte.
func TestBothFenceSmokeParity(t *testing.T) {
	requirePowerShell(t)
	testBothFenceSmoke(t)
}

// bothFenceSmokeSource is script/sprint-358-both-smoke.bsh, verbatim.
const bothFenceSmokeSource = `~~~pwsh as ps
function Square {
    [OutputType([long])]
    param([long]$n)
    return ($n * $n)
}
~~~
x := ps.Square(6)
echo "square=$x"

~~~csharp as cs
using System;
using System.Linq;

public static long Square(long x) => x * x;
public static string Join(string sep, long n) => string.Join(sep, Enumerable.Range(1, (int)n));
public static double Half(double x) => x / 2;
public static string Fail(string why) => throw new InvalidOperationException(why);
~~~
sq := cs.Square(6)
echo "square=$sq"
joined := cs.Join("-", 3)
echo "join=$joined"
five := 5.0
half := cs.Half(five)
echo "half=$half"
failed, failErr := cs.Fail("nope")
echo "fail=$failed:${failErr:+error}"
[[ $failErr == *InvalidOperationException*nope* ]] && echo "message=nope"
`

func testBothFenceSmoke(t *testing.T) {
	t.Helper()
	got := testPythonFenceInterpretedNativeParityAt(t, bothFenceSmokeSource, "input.bsh")
	want := "square=36\nsquare=36\njoin=1-2-3\nhalf=2.5\nfail=:error\nmessage=nope\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// A framework-dependent PowerShell launch — the arm64 musl image's route,
// where the resolver answers pwsh with [dotnet, pwsh.dll] — must survive
// lowering: the generated EnvironmentPlan once dropped ExecutableArgs, so the
// native binary ran bare dotnet and failed "load PowerShell module: EOF"
// while the interpreter passed. BASHPP_TEST_PWSH_DLL names a pwsh.dll to run
// under the dotnet on PATH; no pwsh is needed on PATH.
func TestBothFenceSmokeFrameworkDependentParity(t *testing.T) {
	dll := os.Getenv("BASHPP_TEST_PWSH_DLL")
	if dll == "" {
		t.Skip("BASHPP_TEST_PWSH_DLL unset")
	}
	dotnet, err := exec.LookPath("dotnet")
	if err != nil {
		t.Skip("dotnet unavailable")
	}
	t.Setenv("BASHPP_PWSH", "")
	previous := polyglot.ToolResolver
	t.Cleanup(func() { polyglot.ToolResolver = previous })
	polyglot.ToolResolver = func(name string) ([]string, string, error) {
		if name == "pwsh" {
			return []string{dotnet, dll}, "framework-dependent pwsh", nil
		}
		path, err := exec.LookPath(name)
		return []string{path}, "PATH", err
	}
	testBothFenceSmoke(t)
}

func TestCSharpFenceInterpretedNativeParity(t *testing.T) {
	requirePowerShell(t)
	tests := map[string]struct{ source, want string }{
		"typed qualified": {`~~~csharp as cs
using System;
public static long Add(long a, long b) { Console.WriteLine("csharp"); return a + b; }
public static string Greet(string name) => "hello " + name;
~~~
x := cs.Add(20, 22)
g := cs.Greet("a b")
echo "x=$x|$g"
`, "csharp\nx=42|hello a b\n"},
		"static state": {`~~~cs
static int calls;
public static int Count() => ++calls;
~~~
a := Count()
b := Count()
echo "$a$b"
`, "12\n"},
		"structured object": {`~~~csharp as cs
using System.Collections.Generic;
public class Point { public int X { get; set; } public string Label = ""; }
public static Point Make(int x, string label) => new Point { X = x, Label = label };
public static List<int> Range(int n) { var r = new List<int>(); for (var i = 1; i <= n; i++) r.Add(i); return r; }
~~~
p := cs.Make(7, seven)
r := cs.Range(3)
echo "$p|$r"
`, `{"Label":"seven","X":7}|[1,2,3]` + "\n"},
		"structured error": {`~~~csharp as cs
using System;
public static string Fail(string message) => throw new InvalidOperationException(message);
~~~
value, callErr := cs.Fail(nope)
echo "$value:$callErr"
`, ":InvalidOperationException: nope\n"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := testPythonFenceInterpretedNativeParityAt(t, tc.source, "input.bsh")
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoVoidFenceInterpretedNativeParity(t *testing.T) {
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~go as g
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
g.Main()
`, filepath.Join("testdata", "input.bpp"))
	if want := "hello world\nhello world\nno newlinechecked\nhello world\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPythonFenceHandleMethods(t *testing.T) {
	source, err := os.ReadFile("../internal/testdata/python-handles.bpp")
	if err != nil {
		t.Fatal(err)
	}
	got := testPythonFenceInterpretedNativeParityAt(t, string(source), filepath.Join(t.TempDir(), "handles.bpp"))
	want, err := os.ReadFile("../internal/testdata/python-handles.stdout")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("output=%q", got)
	}
}
