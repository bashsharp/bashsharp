//go:build full

package lower_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The agentic acceptance corpus is the public bashpp-tests/tests/agentic suite.
// Its thirteen cases are the contract denominator; the sources below are byte
// exact copies, never adapted to what the compiler currently accepts. Each one
// is measured three ways for both ambient BASHY_AGENTIC settings:
//
//  1. the public expectation recorded in cases.tsv (exit class, stdout file,
//     stderr category), asserted against the source interpreter,
//  2. the exact source stdout/stderr/status the interpreter produces, which
//     becomes the parity oracle, and
//  3. an actual lower.Compile -> go build -> execute-with-no-source-present
//     native artifact, diffed against (2).
//
// Cases that the parser rejects stay a distinct outcome: they are never
// compiled and must never produce an artifact.

// scopeDiagnostic is the public agentic-scope refusal shared by the "scope"
// stderr category.
const scopeDiagnostic = "agentic action requires an explicit agentic { ...; } scope"

// agenticFixtures holds byte-exact copies of the public
// bashpp-tests/tests/agentic corpus, keyed by file name. TestAgenticFixtureCorpusUnchanged
// re-verifies them against a real checkout when BASHPP_TESTS_AGENTIC_DIR names one.
var agenticFixtures = map[string]string{
	"actions.bpp":               "# Every presentation retains ordinary input/output conventions.\nagentic func twice(n int) int {\n    return $((n * 2))\n}\ntype Value int\nagentic func (v Value) Show() {\n    echo \"method:$v\"\n}\ntype Shower interface { Show() }\nagentic function shell_action() {\n    printf 'shell:%s\\n' \"$1\"\n}\nfunc ordinary(n int) int {\n    return $((n + 1))\n}\ncallback := func() {\n    agentic {\n        shell_action closure\n    }\n}\nagentic {\n    n := twice(21)\n    echo \"typed:$n\"\n    action := twice\n    m := action(4)\n    echo \"value:$m\"\n    var v Value = 7\n    v.Show()\n    method := v.Show\n    method()\n    var view Shower = v\n    view.Show()\n    shell_action \"$1\"\n    callback()\n    x := ordinary(8)\n    echo \"ordinary:$x\"\n    printf '%s\\n' tool | /usr/bin/tr a-z A-Z\n}\n",
	"actions.out":               "typed:42\nvalue:8\nmethod:7\nmethod:7\nmethod:7\nshell:input value\nshell:closure\nordinary:9\nTOOL\n",
	"cases.tsv":                 "# id\tfixture\texit\tstdout\tstderr\nactions\tactions.bpp\t0\tactions.out\tempty\ndynamic-scope\tdynamic-scope.bpp\t0\tdynamic-scope.out\tempty\noutside-value\toutside-value.bpp\tnonzero\tempty\tscope\noutside-interface\toutside-interface.bpp\tnonzero\tempty\tscope\noutside-shell\toutside-shell.bpp\tnonzero\tempty\tscope\nordinary-helper\tordinary-helper.bpp\tnonzero\tempty\tscope\nordinary-shell-helper\tordinary-shell-helper.bpp\tnonzero\tempty\tscope\nordinary-closure\tordinary-closure.bpp\tnonzero\tempty\tscope\nrestoration\trestoration.bpp\tnonzero\tempty\tscope\nreturn-restoration\treturn-restoration.bpp\tnonzero\tempty\tscope\nsource-reset\tsource-reset.bpp\tnonzero\tempty\tscope\nnumeric\tnumeric.bpp\tnonzero\tempty\tsyntax\ncompatibility\tcompatibility.bpp\t0\tcompatibility.out\tempty\n",
	"compatibility.bpp":         "agentic() { printf '<%s>\\n' \"$@\"; }\nagentic word\nagentic func f\nagentic function f\nagentic {suffix\n\"agentic\" {\nagentic \"{\"\necho agentic {\nagentic 1\n",
	"compatibility.out":         "<word>\n<func>\n<f>\n<function>\n<f>\n<{suffix>\n<{>\n<{>\nagentic {\n<1>\n",
	"dynamic-scope.bpp":         "agentic function marked() { echo \"$1\"; }\nbefore=$PWD\nagentic {\n    value=retained\n    eval 'marked eval'\n    source ./source-marked.sh\n    marked restored-caller\n    cd ..\n}\nprintf 'value:%s\\n' \"$value\"\ntest \"$PWD\" != \"$before\" || exit 9\n",
	"dynamic-scope.out":         "eval\nsource\nrestored-caller\nvalue:retained\n",
	"numeric.bpp":               "agentic(1) func marked() { echo body-must-not-run; }\n",
	"ordinary-closure.bpp":      "agentic func marked() { echo body-must-not-run; }\nagentic {\n    helper := func() { marked(); }\n    helper()\n}\n",
	"ordinary-helper.bpp":       "agentic func marked() { echo body-must-not-run; }\nfunc helper() { marked(); }\nagentic { helper(); }\n",
	"ordinary-shell-helper.bpp": "agentic function marked() { echo body-must-not-run; }\nhelper() { marked; }\nagentic { helper; }\n",
	"outside-interface.bpp":     "type Value int\nagentic func (v Value) Show() { echo body-must-not-run; }\ntype Shower interface { Show() }\nvar v Value = 1\nvar view Shower = v\nview.Show()\n",
	"outside-shell.bpp":         "agentic function marked() { echo body-must-not-run; }\nmarked\n",
	"outside-value.bpp":         "agentic func marked() { echo body-must-not-run; }\naction := marked\naction()\n",
	"restoration.bpp":           "agentic function marked() { echo body-must-not-run; }\nagentic { false; }\nmarked\n",
	"return-restoration.bpp":    "agentic function marked() { echo body-must-not-run; }\nhelper() {\n    agentic { return 0; }\n}\nhelper\nmarked\n",
	"source-marked.sh":          "agentic { marked source; }\n",
	"source-reset.bpp":          "agentic function marked() { echo body-must-not-run; }\nagentic { source ./source-unmarked.sh; }\n",
	"source-unmarked.sh":        "marked\n",
}

// agenticCase is one row of the public cases.tsv.
type agenticCase struct {
	id, fixture, exit, stdout, stderr string
}

// agenticCases parses the embedded cases.tsv exactly as the public suite does,
// so the expectations under test are read rather than transcribed.
func agenticCases(t *testing.T) []agenticCase {
	t.Helper()
	reader := csv.NewReader(strings.NewReader(agenticFixtures["cases.tsv"]))
	reader.Comma, reader.Comment, reader.FieldsPerRecord = '\t', '#', 5
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 13 {
		t.Fatalf("acceptance denominator changed: %d cases", len(rows))
	}
	seen := map[string]bool{}
	cases := make([]agenticCase, 0, len(rows))
	for _, row := range rows {
		if seen[row[0]] {
			t.Fatalf("duplicate case id %q", row[0])
		}
		seen[row[0]] = true
		if filepath.Base(row[1]) != row[1] || agenticFixtures[row[1]] == "" {
			t.Fatalf("case %s names unknown fixture %q", row[0], row[1])
		}
		cases = append(cases, agenticCase{row[0], row[1], row[2], row[3], row[4]})
	}
	return cases
}

// wantStdout resolves the stdout column: either "empty" or a companion file.
func (c agenticCase) wantStdout(t *testing.T) string {
	t.Helper()
	if c.stdout == "empty" {
		return ""
	}
	body, ok := agenticFixtures[c.stdout]
	if !ok {
		t.Fatalf("case %s names unknown stdout file %q", c.id, c.stdout)
	}
	return body
}

// TestAgenticFixtureCorpusUnchanged fails if the embedded copies have drifted
// from a real bashpp-tests checkout. It is a no-op without one, which keeps the
// matrix runnable in this repo alone while still refusing silent divergence.
func TestAgenticFixtureCorpusUnchanged(t *testing.T) {
	dir := os.Getenv("BASHPP_TESTS_AGENTIC_DIR")
	if dir == "" {
		t.Skip("set BASHPP_TESTS_AGENTIC_DIR=<bashpp-tests>/tests/agentic to verify the embedded corpus")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "README.md" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		onDisk[entry.Name()] = string(body)
	}
	names := map[string]bool{}
	for name := range agenticFixtures {
		names[name] = true
	}
	for name := range onDisk {
		names[name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		if onDisk[name] != agenticFixtures[name] {
			t.Errorf("embedded %s differs from %s; re-copy the fixture verbatim, do not edit it to fit the compiler", name, dir)
		}
	}
}

// agenticWorkDir materializes only the sidecars a case may source at runtime.
// The .bpp source is deliberately absent from the artifact execution directory.
func agenticWorkDir(t *testing.T, withSource string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range agenticFixtures {
		if !strings.HasSuffix(name, ".sh") {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if withSource != "" {
		if err := os.WriteFile(filepath.Join(dir, withSource), []byte(agenticFixtures[withSource]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// agenticEnv is the ambient environment for one matrix cell. Absolute tool
// paths in the corpus (/usr/bin/tr) require the real system directories.
func agenticEnv(ambient string) []string {
	env := []string{"PATH=/bin:/usr/bin", "BASHY_HINTS=off"}
	if ambient == "1" {
		env = append(env, "BASHY_AGENTIC=1")
	}
	return env
}

type agenticStreams struct {
	out, err string
	status   int
}

func agenticStatus(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if status, ok := interp.IsExitStatus(err); ok {
		return int(status)
	}
	return -1
}

// TestAgenticContractAcceptance is the permanent compiled matrix: thirteen
// public cases by two ambient settings, each compiled for real and executed
// with neither the original nor the generated source on disk.
func TestAgenticContractAcceptance(t *testing.T) {
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Skipf("no go toolchain at %s: %v", goBinary, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	for _, tc := range agenticCases(t) {
		for _, ambient := range []string{"unset", "1"} {
			t.Run(tc.id+"/BASHY_AGENTIC="+ambient, func(t *testing.T) {
				source := agenticFixtures[tc.fixture]
				env := agenticEnv(ambient)

				// (1) Parse. A rejected source is a distinct terminal outcome:
				// it is never compiled and must never yield an artifact.
				file, parseErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), tc.fixture)
				if parseErr != nil {
					if tc.stderr != "syntax" {
						t.Fatalf("case declares stderr=%q but the parser rejected it: %v", tc.stderr, parseErr)
					}
					if tc.exit == "0" || tc.stdout != "empty" {
						t.Fatalf("parser-rejected case must declare a nonzero exit and empty stdout, got exit=%q stdout=%q", tc.exit, tc.stdout)
					}
					if _, err := lower.Compile(&syntax.File{Name: tc.fixture}, lower.Options{Origin: tc.fixture}); err != nil {
						t.Fatalf("empty-file control compile: %v", err)
					}
					// No artifact exists for a source that never parsed.
					return
				}
				if tc.stderr == "syntax" {
					t.Fatalf("case declares a syntax rejection but %s parsed", tc.fixture)
				}

				// (2) Source oracle: exact stdout/stderr/status from the interpreter.
				sourceDir := agenticWorkDir(t, tc.fixture)
				var baseOut, baseErr bytes.Buffer
				runner, err := interp.New(
					interp.Lang(syntax.LangBashPP),
					interp.Dir(sourceDir),
					interp.Params("--", "input value"),
					interp.StdIO(nil, &baseOut, &baseErr),
					interp.Env(expand.ListEnviron(env...)),
				)
				if err != nil {
					t.Fatal(err)
				}
				runCtx, runCancel := context.WithTimeout(ctx, 30*time.Second)
				baseline := agenticStreams{status: agenticStatus(runner.Run(runCtx, file))}
				runCancel()
				baseline.out, baseline.err = baseOut.String(), baseErr.String()

				// (3) The public expectation, asserted against that oracle.
				switch tc.exit {
				case "0":
					if baseline.status != 0 {
						t.Errorf("public exit=0, source status=%d (stderr %q)", baseline.status, baseline.err)
					}
				case "nonzero":
					if baseline.status == 0 {
						t.Errorf("public exit=nonzero, source status=0 (stdout %q)", baseline.out)
					}
				default:
					t.Fatalf("unknown exit column %q", tc.exit)
				}
				if want := tc.wantStdout(t); baseline.out != want {
					t.Errorf("public stdout %s mismatch:\n got %q\nwant %q", tc.stdout, baseline.out, want)
				}
				switch tc.stderr {
				case "empty":
					if baseline.err != "" {
						t.Errorf("public stderr=empty, got %q", baseline.err)
					}
				case "scope":
					if !strings.Contains(baseline.err, scopeDiagnostic) {
						t.Errorf("public stderr=scope, got %q", baseline.err)
					}
				default:
					t.Fatalf("unknown stderr column %q", tc.stderr)
				}
				if t.Failed() {
					return
				}

				// (4) Actual compilation of the unchanged source.
				compiled, err := lower.Compile(file, lower.Options{Origin: tc.fixture, Dir: sourceDir})
				if err != nil {
					t.Fatalf("GAP lower.Compile(%s): %v", tc.fixture, err)
				}
				if bytes.Contains(compiled.Source, []byte("agentic {")) && bytes.Contains(compiled.Source, []byte(strings.TrimSpace(source))) {
					t.Fatalf("generated Go smuggles the whole Bash++ source; execution would not be source-free")
				}

				// (5) Real go build in an importable module bound to this checkout.
				buildDir := t.TempDir()
				writeAgenticModule(t, buildDir, root)
				generated := filepath.Join(buildDir, "main.go")
				if err := os.WriteFile(generated, compiled.Source, 0o600); err != nil {
					t.Fatal(err)
				}
				binary := filepath.Join(buildDir, "program")
				build := exec.CommandContext(ctx, goBinary, "build", "-mod=mod", "-o", binary, ".")
				build.Dir = buildDir
				build.Env = append(os.Environ(), "GOWORK=off")
				if output, err := build.CombinedOutput(); err != nil {
					t.Fatalf("GAP go build(%s): %v\n%s\n--- generated ---\n%s", tc.fixture, err, output, compiled.Source)
				}

				// (6) Source removal: neither the original nor the generated Go
				// may be present anywhere the artifact runs.
				if err := os.Remove(generated); err != nil {
					t.Fatal(err)
				}
				execDir := agenticWorkDir(t, "")
				assertNoSource(t, execDir)
				var out, diag bytes.Buffer
				execCtx, execCancel := context.WithTimeout(ctx, 30*time.Second)
				defer execCancel()
				command := exec.CommandContext(execCtx, binary, "input value")
				command.Dir = execDir
				command.Env = env
				command.Stdout, command.Stderr = &out, &diag
				artifact := agenticStreams{status: agenticStatus(command.Run())}
				artifact.out, artifact.err = out.String(), diag.String()
				if artifact != baseline {
					t.Fatalf("native artifact diverges from source oracle:\n artifact out=%q err=%q status=%d\n   source out=%q err=%q status=%d",
						artifact.out, artifact.err, artifact.status, baseline.out, baseline.err, baseline.status)
				}
			})
		}
	}
}

// assertNoSource proves the execution directory carries no Bash++ or generated
// Go source; the sidecars a case may source at runtime are shell data, not the
// compiled program, and remain.
func assertNoSource(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".bpp") || strings.HasSuffix(entry.Name(), ".go") {
			t.Fatalf("execution directory still holds source %s", entry.Name())
		}
	}
}

func writeAgenticModule(t *testing.T, dir, root string) {
	t.Helper()
	body := "module agenticacceptance\n\ngo 1.25\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + filepath.ToSlash(root) + "\n"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// agenticNoFallbackFixtures are the corpus cases whose refusal is decided on a
// typed dispatch path — a typed value call, a method value, an interface
// dispatch, and an ordinary Go helper reached from a typed closure. None of
// them may reach a shell region, so a compiled Entry that honors the contract
// natively never builds a shell runner and never issues a provider request.
var agenticNoFallbackFixtures = []string{"outside-value.bpp", "outside-interface.bpp", "ordinary-helper.bpp", "ordinary-closure.bpp"}

// TestAgenticCompiledEntryProviderNoInterpFallback compiles those unchanged
// sources as an importable Entry, links them into a host that injects a
// provider and a shell factory, and requires the artifact to decide the
// contract without constructing an interpreter at all. It complements
// TestCompiledProviderEntryAcceptance, which measures the provider request
// itself; here the claim is the absence of any interpreter fallback.
func TestAgenticCompiledEntryProviderNoInterpFallback(t *testing.T) {
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Skipf("no go toolchain at %s: %v", goBinary, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, fixture := range agenticNoFallbackFixtures {
		t.Run(fixture, func(t *testing.T) {
			source := agenticFixtures[fixture]
			if source == "" {
				t.Fatalf("unknown fixture %s", fixture)
			}
			file := parse(t, source, fixture)

			// Source oracle, so the artifact is held to the exact diagnostic.
			dir := agenticWorkDir(t, fixture)
			var baseOut, baseErr bytes.Buffer
			runner, err := interp.New(
				interp.Lang(syntax.LangBashPP),
				interp.Dir(dir),
				interp.Params("--", "input value"),
				interp.StdIO(nil, &baseOut, &baseErr),
				interp.Env(expand.ListEnviron(agenticEnv("1")...)),
			)
			if err != nil {
				t.Fatal(err)
			}
			status := agenticStatus(runner.Run(ctx, parse(t, source, fixture)))
			if status != 1 || baseOut.Len() != 0 || !strings.Contains(baseErr.String(), scopeDiagnostic) {
				t.Fatalf("source oracle: status=%d stdout=%q stderr=%q", status, baseOut.String(), baseErr.String())
			}

			compiled, err := lower.Compile(file, lower.Options{Package: "generated", Entry: "Execute", Origin: fixture, Dir: dir})
			if err != nil {
				t.Fatalf("GAP lower.Compile(%s) as importable entry: %v", fixture, err)
			}
			module := t.TempDir()
			writeAgenticModule(t, module, root)
			generated := filepath.Join(module, "generated", "program.go")
			host := filepath.Join(module, "host", "fallback_test.go")
			writeAgenticFile(t, generated, string(compiled.Source))
			writeAgenticFile(t, host, agenticFallbackHarness)
			binary := filepath.Join(module, "fallback.test")
			build := exec.CommandContext(ctx, goBinary, "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
			build.Dir = module
			build.Env = append(os.Environ(), "GOWORK=off")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("GAP building importable entry for %s: %v\n%s\n--- generated ---\n%s", fixture, err, output, compiled.Source)
			}
			for _, path := range []string{generated, host} {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			execDir := agenticWorkDir(t, "")
			assertNoSource(t, execDir)
			command := exec.CommandContext(ctx, binary, "-test.v", "-test.timeout=60s")
			command.Dir = execDir
			command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1", "AGENTIC_WANT_STDERR=" + baseErr.String()}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("compiled entry fallback contract for %s: %v\n%s", fixture, err, output)
			}
		})
	}
}

func writeAgenticFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The host retains the real provider hook and shell factory an embedder would
// install. A session builds its shell runner once by design (the same count
// TestCompiledProviderEntryAcceptance asserts even for a denied action), so the
// factory is counted rather than poisoned; the provider hook is poisoned,
// because any request reaching it means the compiled program handed the typed
// source to an interpreter instead of deciding the contract natively.
const agenticFallbackHarness = `package host_test
import("bytes";"context";"os";"strings";"sync/atomic";"testing";"agenticacceptance/generated";"github.com/bashsharp/bashsharp/engine/interp";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt";"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec")
func TestCompiledEntryDecidesWithoutInterpreter(t *testing.T){
 want:=os.Getenv("AGENTIC_WANT_STDERR")
 if want==""{t.Fatal("missing AGENTIC_WANT_STDERR")}
 var builds,requests atomic.Int32
 base:=shellexec.New(shellexec.BashPP(),shellexec.RunnerOptions(interp.ExecHandler(func(ctx context.Context,args []string)error{
  requests.Add(1)
  t.Errorf("provider reached with %q: typed refusal took an interpreter path",args)
  return nil
 })))
 factory:=func(state rt.State,stdio rt.Stdio)(rt.ShellRunner,error){builds.Add(1);return base(state,stdio)}
 const sessions=8
 for i:=0;i<sessions;i++{
  var out,diagnostic bytes.Buffer
  code,err:=generated.Execute(rt.WithContext(context.Background()),rt.WithDir("."),rt.WithEnviron("BASHY_AGENTIC=1"),rt.WithParams("input value"),rt.WithStdio(strings.NewReader(""),&out,&diagnostic),rt.WithShellFactory(factory))
  if code!=1||err!=nil||out.Len()!=0||diagnostic.String()!=want{
   t.Fatalf("iteration %d: code=%d err=%v stdout=%q stderr=%q want stderr %q",i,code,err,out.String(),diagnostic.String(),want)
  }
 }
 if builds.Load()!=sessions{t.Errorf("shell runners built=%d, want one per session (%d)",builds.Load(),sessions)}
 if requests.Load()!=0{t.Fatalf("interpreter requests=%d, want none: the typed refusal must be decided natively",requests.Load())}
}
`
