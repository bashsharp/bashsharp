//go:build full

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The explicit foreign error opt-in: a short declaration that names one
// binding more than a typed export declares results receives the worker's own
// failure as a trailing error result with status 0. These tests pin the exact
// output of both engines, the interpreter and the lowered program, for the
// same script, so a change to one side that the other does not follow fails
// here rather than only in the parity check.

type foreignOutcome struct {
	stdout, stderr string
}

// foreignOutcomes runs source through the interpreter and through the lowered
// native program and returns both outcomes. Unlike the parity helper it
// tolerates a non-zero process status, which the failure shapes rely on.
func foreignOutcomes(t *testing.T, source string, args ...string) (interpreted, native foreignOutcome) {
	t.Helper()
	file := parse(t, source, "input.bpp")
	result, err := lower.Compile(file, lower.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errOut), interp.Params(args...))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, file); err != nil {
		var status interp.ExitStatus
		if !errors.As(err, &status) {
			t.Fatalf("interpreted: %v: %s", err, errOut.String())
		}
	}
	interpreted = foreignOutcome{out.String(), errOut.String()}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "generated.go"), result.Source, 0600); err != nil {
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
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-o", binary, "generated.go")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, output, result.Source)
	}
	cmd = exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.TempDir()
	var nativeOut, nativeErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &nativeOut, &nativeErr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("native: %v: %s", err, nativeErr.String())
		}
	}
	return interpreted, foreignOutcome{nativeOut.String(), nativeErr.String()}
}

func expectForeignOutcomes(t *testing.T, source string, want foreignOutcome) {
	t.Helper()
	interpreted, native := foreignOutcomes(t, source)
	if interpreted != want {
		t.Errorf("interpreted = %+q, want %+q", interpreted, want)
	}
	if native != want {
		t.Errorf("native = %+q, want %+q", native, want)
	}
}

const pythonErrorFence = `~~~python
def ok() -> int:
    return 7
def fail() -> int:
    raise ValueError("boom")
def lie() -> int:
    return "nope"
def die() -> int:
    import os
    os._exit(3)
def touch() -> None:
    print("touched")
def boom() -> None:
    raise RuntimeError("no")
def record() -> dict[str, list[int]]:
    return {"items": [3, 4]}
def loose(value):
    return value+"!"
~~~
`

// Each case runs twice: as written (the static lowering calls the adapter
// directly) and behind a subshell, which activates the execution runtime and
// frames the call through the short declaration's result presence.
func TestForeignErrorOptInPython(t *testing.T) {
	cases := []struct {
		name, script string
		want         foreignOutcome
	}{
		{"success", "value, callErr := ok()\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=7 err= status=0\n", ""}},
		{"domain error is a status-0 result", "value, callErr := fail()\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=0 err=ValueError: boom status=0\n", ""}},
		{"success resets a failing status", "false\nvalue, callErr := ok()\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=7 err= status=0\n", ""}},
		{"annotation violation stays infrastructure", "value, callErr := lie()\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=0 err= status=1\n", "bash++: foreign call lie failed: Python function lie violated its int result annotation: got string\n"}},
		{"worker death stays infrastructure", "value, callErr := die()\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=0 err= status=1\n", "bash++: foreign call die failed: Python worker exited with status 3\n"}},
		{"zero result success", "err := touch()\necho \"err=$err status=$?\"\n",
			foreignOutcome{"touched\nerr= status=0\n", ""}},
		{"zero result domain error", "err := boom()\necho \"err=$err status=$?\"\n",
			foreignOutcome{"err=RuntimeError: no status=0\n", ""}},
		{"object result", "value, callErr := record()\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value={\"items\":[3,4]} err= status=0\n", ""}},
		{"inside a function", "func run() {\n    value, callErr := fail()\n    echo \"value=$value err=$callErr status=$?\"\n}\nrun()\n",
			foreignOutcome{"value=0 err=ValueError: boom status=0\n", ""}},
		// Legacy shapes the opt-in must leave alone: the one-value typed
		// form, the zero-result statement and the dynamic two-value form.
		{"legacy one value", "x := ok()\necho \"x=$x status=$?\"\n",
			foreignOutcome{"x=7 status=0\n", ""}},
		{"legacy zero result statement", "touch()\necho \"status=$?\"\n",
			foreignOutcome{"touched\nstatus=0\n", ""}},
		{"legacy dynamic two values", "x, callErr := loose(hi)\necho \"$x:$callErr:$?\"\n",
			foreignOutcome{"hi!::0\n", ""}},
	}
	for _, c := range cases {
		for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				expectForeignOutcomes(t, pythonErrorFence+prelude+c.script, c.want)
			})
		}
	}
}

// The entry's own error result must not capture a top-level source binding
// named err. A differently named error binding is the control for that scope.
func TestForeignErrorBindingNamedErr(t *testing.T) {
	const fence = `~~~python as py
def Run(fail: bool) -> int:
    if fail:
        raise ValueError("boom")
    return 7
~~~
`
	for _, tc := range []struct {
		name, script string
		want         foreignOutcome
	}{
		{"error name failure", "rc, err := py.Run(true)\necho \"rc=$rc err=$err status=$?\"\n", foreignOutcome{"rc=0 err=ValueError: boom status=0\n", ""}},
		{"renamed failure", "rc, exception := py.Run(true)\necho \"rc=$rc err=$exception status=$?\"\n", foreignOutcome{"rc=0 err=ValueError: boom status=0\n", ""}},
		{"error name success", "rc, err := py.Run(false)\necho \"rc=$rc err=$err status=$?\"\n", foreignOutcome{"rc=7 err= status=0\n", ""}},
		{"renamed success", "rc, exception := py.Run(false)\necho \"rc=$rc err=$exception status=$?\"\n", foreignOutcome{"rc=7 err= status=0\n", ""}},
	} {
		for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				expectForeignOutcomes(t, fence+prelude+tc.script, tc.want)
			})
		}
	}
}

func TestGoFenceExitStatus(t *testing.T) {
	for _, status := range []int{3, 42} {
		for _, call := range []string{"g.Main()", "callErr := g.Main()"} {
			for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
				t.Run(fmt.Sprintf("%d/%s/%s", status, call, mode), func(t *testing.T) {
					tail, want := "echo \"status=$?\"\n", fmt.Sprintf("status=%d\n", status)
					if strings.HasPrefix(call, "callErr") {
						tail, want = "echo \"err=$callErr status=$?\"\n", "err= "+want
					}
					source := fmt.Sprintf("~~~go as g\nimport \"os\"\nfunc Main() { os.Exit(%d) }\n~~~\n%s%s\n%s", status, prelude, call, tail)
					interpreted, native := foreignOutcomes(t, source)
					for engine, got := range map[string]foreignOutcome{"interpreted": interpreted, "native": native} {
						if got.stdout != want || !strings.Contains(got.stderr, "Go worker") {
							t.Errorf("%s: %+q", engine, got)
						}
					}
				})
			}
		}
	}
}

// The legacy one-value failure keeps its pre-existing shape on each engine:
// the interpreter declares nothing and reports the arity mismatch, the static
// lowering binds the zero value with the failure status. Neither is changed
// by the opt-in, which is the only form that turns the failure into a result.
func TestForeignLegacyFailureUnchanged(t *testing.T) {
	interpreted, native := foreignOutcomes(t, pythonErrorFence+"x := fail()\necho \"x=$x status=$?\"\n")
	if want := (foreignOutcome{"x= status=2\n", "bash++: foreign call fail failed: ValueError: boom\nassignment mismatch: 1 variable(s) but 0 value(s)\n"}); interpreted != want {
		t.Errorf("interpreted = %+q, want %+q", interpreted, want)
	}
	if want := (foreignOutcome{"x=0 status=1\n", "ValueError: boom\n"}); native != want {
		t.Errorf("native = %+q, want %+q", native, want)
	}
}

// Rust reaches the same contract through Result<T, E>: Err is the worker's own
// failure, and Result<(), E> opts in with a single error binding.
func TestForeignErrorOptInRust(t *testing.T) {
	requireRustToolchain(t)
	const fence = `~~~rust as rs
pub fn checked(value: i64) -> Result<i64, String> { if value > 3 { Err("too large".into()) } else { Ok(value) } }
pub fn run(flag: bool) -> Result<(), String> { if flag { Ok(()) } else { Err("refused".into()) } }
~~~
`
	cases := []struct {
		name, script string
		want         foreignOutcome
	}{
		{"success", "value, callErr := rs.checked(2)\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=2 err= status=0\n", ""}},
		{"domain error is a status-0 result", "value, callErr := rs.checked(5)\necho \"value=$value err=$callErr status=$?\"\n",
			foreignOutcome{"value=0 err=RUST-ECALL: too large status=0\n", ""}},
		{"zero result success", "err := rs.run(true)\necho \"err=$err status=$?\"\n",
			foreignOutcome{"err= status=0\n", ""}},
		{"zero result domain error", "err := rs.run(false)\necho \"err=$err status=$?\"\n",
			foreignOutcome{"err=RUST-ECALL: refused status=0\n", ""}},
		{"legacy one value", "x := rs.checked(3)\necho \"x=$x status=$?\"\n",
			foreignOutcome{"x=3 status=0\n", ""}},
	}
	for _, c := range cases {
		for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				expectForeignOutcomes(t, fence+prelude+c.script, c.want)
			})
		}
	}
}

// The generated shape: one adapter per typed export beside the legacy
// wrapper, the transport/domain split inside it, and — under the execution
// runtime — the module binding kept as a native package variable, every
// result slot recorded and the outcome carried into the program's status.
func TestForeignErrorOptInGeneratedShape(t *testing.T) {
	const fence = "~~~python\ndef fail() -> int:\n    raise ValueError(\"boom\")\ndef touch() -> None:\n    pass\n~~~\n"
	const script = "value, callErr := fail()\nerr := touch()\necho \"$value$callErr$err\"\n"
	for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
		t.Run(mode, func(t *testing.T) {
			file := parse(t, fence+prelude+script, "input.bpp")
			result, err := lower.Compile(file, lower.Options{})
			if err != nil {
				t.Fatal(err)
			}
			generated := strings.ReplaceAll(strings.ReplaceAll(string(result.Source), "\t", ""), " ", "")
			want := []string{
				"var__bpp0_foreign0=__bpp0_polyglot.Start(",
				"if_,foreign:=__bpp0_polyglot.ForeignErrorDetail(err);!foreign{",
				"__bpp0_rt.Fail(__bpp0_fmt.Errorf(\"bash++:foreigncallfailfailed:%w\",err))",
			}
			if mode == "execution" {
				// The accepted callback bridge carries context and status on
				// each calling Program, including these error-opt-in wrappers.
				want = append(want,
					"type__bpp0_foreignProgramState=__bpp0_rt.Program",
					"func__bpp0_foreignErr0_fail(__bpp0_foreignProgram*__bpp0_foreignProgramState)(int,error){",
					"func__bpp0_foreignErr0_touch(__bpp0_foreignProgram*__bpp0_foreignProgramState)error{",
					"__bpp0_foreign0.Call(__bpp0_foreignProgram.Context,\"fail\")",
					"__bpp0_foreign0.Call(__bpp0_foreignProgram.Context,\"touch\")",
					"__bpp0_foreignErr0_fail(__bpp0_program)\n",
					"__bpp0_foreignErr0_touch(__bpp0_program)\n",
					"__bpp0_foreignProgram.SetStatus(__bpp0_rt.ExitCode(err))",
					"__bpp0_program.SetStatus(__bpp0_foreignExit)",
					",0,__bpp0_foreignResult",
					",1,err))",
					",0,err))",
				)
			} else {
				want = append(want,
					"func__bpp0_foreignErr0_fail()(int,error){",
					"func__bpp0_foreignErr0_touch()error{",
					"__bpp0_foreign0.Call(__bpp0_context.Background(),\"fail\")",
					"__bpp0_foreign0.Call(__bpp0_context.Background(),\"touch\")",
					"__bpp0_foreignErr0_fail()\n", "__bpp0_foreignErr0_touch()\n",
				)
			}
			for _, fragment := range want {
				if !strings.Contains(generated, fragment) {
					t.Errorf("generated source lacks %q:\n%s", fragment, result.Source)
				}
			}
		})
	}
}

func TestPythonFenceHandleTransportFailure(t *testing.T) {
	source := `~~~python as py
def make():
    class Item:
        def die(self):
            import os
            os._exit(3)
    return Item()
~~~
h, err := py.make()
v, err := h.die()
echo "domain=$err status=$?"
`
	for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
		t.Run(mode, func(t *testing.T) {
			interpreted, native := foreignOutcomes(t, prelude+source)
			for name, got := range map[string]foreignOutcome{"interpreted": interpreted, "native": native} {
				if got.stdout != "domain= status=1\n" || !strings.Contains(got.stderr, "Python worker exited with status 3") {
					t.Errorf("%s: %+q", name, got)
				}
			}
		})
	}
}

func TestPythonFenceScalarIsNotHandle(t *testing.T) {
	source := `~~~python as py
def make(): return "<_io.StringIO object at 0x123>"
~~~
h, err := py.make()
v, err := h.read()
`
	interpreted, native := foreignOutcomes(t, source)
	if !strings.Contains(interpreted.stderr, "ESELECTOR-TYPE") {
		t.Fatalf("interpreted=%+q", interpreted)
	}
	if !strings.Contains(native.stderr, "not an object") {
		t.Fatalf("native=%+q", native)
	}
}
