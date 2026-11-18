package polyglot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiscoverPowerShellEnvironmentResolverAndOverride(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	pwsh := powerShellFixtureExecutable(bin, "pwsh")
	writeEnvironmentFile(t, pwsh, "fixture pwsh")
	plan, err := DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "src", "program.bpp"), Language: "powershell",
		Environ: powerShellFixtureEnviron(bin),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalPwsh, _ := canonicalExecutable(pwsh, nil)
	if plan.Language != "powershell" || plan.Manager != "pwsh" || plan.Runtime != "pwsh" || plan.Executable != canonicalPwsh || plan.Fingerprint == "" {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan.ResolutionFiles) != 0 {
		t.Fatalf("PowerShell fence has no project manifest, got ResolutionFiles = %v", plan.ResolutionFiles)
	}
	// An alias reaches the same discovery row.
	if _, err := DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "program.bpp"), Language: "pwsh", Environ: powerShellFixtureEnviron(bin),
	}); err != nil {
		t.Fatalf("alias discovery: %v", err)
	}
	override := powerShellFixtureExecutable(root, "custom-pwsh")
	writeEnvironmentFile(t, override, "custom pwsh")
	overridden, err := DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "program.bpp"), Language: "powershell",
		Environ: append(powerShellFixtureEnviron(""), "BASHPP_PWSH="+override),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalOverride, _ := canonicalExecutable(override, nil)
	if overridden.Executable != canonicalOverride {
		t.Fatalf("override = %#v", overridden)
	}
	if !slices.Contains(overridden.Explanation, "runtime executable overridden") {
		t.Fatalf("override explanation missing: %v", overridden.Explanation)
	}
}

func TestDiscoverPowerShellEnvironmentUnavailable(t *testing.T) {
	root := t.TempDir()
	_, err := DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "program.bpp"), Language: "powershell", Environ: []string{"PATH="},
	})
	if err == nil || !strings.Contains(err.Error(), "PowerShell runtime unavailable") {
		t.Fatalf("unavailable error = %v", err)
	}
}

func TestDiscoverPowerShellEnvironmentCarriesProvisionedRuntimeControls(t *testing.T) {
	root := t.TempDir()
	pwsh := powerShellFixtureExecutable(root, "pwsh")
	writeEnvironmentFile(t, pwsh, "fixture pwsh")
	t.Setenv("LD_LIBRARY_PATH", "")
	t.Setenv("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "")
	saved := ToolResolver
	ToolResolver = func(name string) ([]string, string, error) {
		if err := os.Setenv("LD_LIBRARY_PATH", filepath.Join(root, "libs")); err != nil {
			return nil, "", err
		}
		if err := os.Setenv("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "1"); err != nil {
			return nil, "", err
		}
		return []string{pwsh}, "pinned PowerShell", nil
	}
	t.Cleanup(func() { ToolResolver = saved })
	plan, err := DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "program.bpp"), Language: "powershell",
		Environ: powerShellFixtureEnviron(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LD_LIBRARY_PATH=" + filepath.Join(root, "libs"),
		"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1",
	} {
		if !slices.Contains(plan.Env, want) {
			t.Fatalf("PowerShell worker lost %q: %v", want, plan.Env)
		}
	}
}

func TestPowerShellArgumentsAndLoadRequest(t *testing.T) {
	ps := PowerShell{Command: "pwsh"}
	args := ps.arguments(Plan{})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-NoProfile", "-NonInteractive", "-Command"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("arguments missing %q: %v", want, args)
		}
	}
	if args[len(args)-1] != powershellWorker {
		t.Fatalf("worker script is not the final argument")
	}
	load := ps.loadRequest(Plan{Source: "function Square { 1 }"})
	if load["op"] != "load" || load["source"] != "function Square { 1 }" {
		t.Fatalf("load request = %#v", load)
	}
}

func TestPowerShellUnavailable(t *testing.T) {
	ps := PowerShell{Command: t.TempDir() + "/missing-pwsh"}
	if _, err := ps.Analyze(context.Background(), "function F { 1 }\n"); err == nil || !strings.Contains(err.Error(), "PowerShell runtime unavailable") {
		t.Fatalf("analyze error = %v", err)
	}
	m := Start(Plan{Language: "powershell", Source: "function F { 1 }\n"}, ps)
	defer m.Close()
	if _, err := m.Call(context.Background(), "F"); err == nil || !strings.Contains(err.Error(), "PowerShell runtime unavailable") {
		t.Fatalf("call error = %v", err)
	}
}

// requirePowerShell gates the end-to-end tests on a real pwsh 7. Absent it, the
// language-table, discovery and argument tests above still cover the Go seam;
// the cross-OS runtime proof is the sprint's shared host/image evidence.
func requirePowerShell(t *testing.T) string {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh unavailable")
	}
	return pwsh
}

func powerShellPlan(t *testing.T, source string) (Plan, PowerShell) {
	t.Helper()
	runtime := PowerShell{Command: requirePowerShell(t)}
	plans, err := Prepare(context.Background(), []Block{{Language: "powershell", Source: source}}, map[string]Analyzer{"powershell": runtime})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %d", len(plans))
	}
	return plans[0], runtime
}

func TestPowerShellAnalyzeExportsAndSignatures(t *testing.T) {
	plan, _ := powerShellPlan(t, `
function Square {
    [OutputType([long])]
    param([long]$n)
    $n * $n
}
function Greet($name) {
    Write-Output ("hi " + $name)
}
function _hidden { 1 }
`)
	if len(plan.Exports) != 2 {
		t.Fatalf("exports = %#v", plan.Exports)
	}
	byName := map[string]Export{}
	for _, e := range plan.Exports {
		byName[e.Name] = e
	}
	if sq, ok := byName["Square"]; !ok || sq.Signature.Dynamic || strings.Join(sq.Signature.Params, ",") != "int" || strings.Join(sq.Signature.Results, ",") != "int" {
		t.Fatalf("Square export = %#v", byName["Square"])
	}
	if greet, ok := byName["Greet"]; !ok || !greet.Signature.Dynamic || strings.Join(greet.Signature.Params, ",") != "any" {
		t.Fatalf("Greet export = %#v", byName["Greet"])
	}
}

func TestPowerShellPersistentCallsTypedValuesAndOutput(t *testing.T) {
	plan, runtime := powerShellPlan(t, `
$script:calls = 0
function Square {
    [OutputType([long])]
    param([long]$n)
    Write-Host "called"
    $script:calls++
    $n * $n
}
function Blob {
    [OutputType([byte[]])]
    param([byte[]]$data)
    # PowerShell enumerates arrays on the success stream unless wrapped.
    return ,([byte[]]($data + [byte[]]@(33)))
}
function Flag {
    [OutputType([bool])]
    param()
    $true
}
function TypedText {
    [OutputType([long])]
    param()
    Write-Host "powershell`+"`r`n"+`second"
    42
}
function Calls { [OutputType([long])] param() $script:calls }
function Boom { throw "boom" }
`)
	m := Start(plan, runtime)
	defer m.Close()

	got, err := m.Call(context.Background(), "Square", int64(3))
	if err != nil || got.Value != int64(9) {
		t.Fatalf("Square = %#v, %v", got, err)
	}
	if !strings.Contains(got.Stdout, "called") {
		t.Fatalf("Write-Host did not reach stdout: %q", got.Stdout)
	}

	// Typed-call capture is text, so it uses the same portable LF contract as
	// command output. Keep the typed result separate from the captured stream.
	text, err := m.Call(context.Background(), "TypedText")
	if err != nil || text.Value != int64(42) || text.Stdout != "powershell\nsecond\n" {
		t.Fatalf("TypedText = %#v, %v", text, err)
	}

	blob, err := m.Call(context.Background(), "Blob", []byte("x"))
	if err != nil {
		t.Fatalf("Blob error: %v", err)
	}
	if b, ok := blob.Value.([]byte); !ok || string(b) != "x!" {
		t.Fatalf("Blob = %#v", blob.Value)
	}

	flag, err := m.Call(context.Background(), "Flag")
	if err != nil || flag.Value != true {
		t.Fatalf("Flag = %#v, %v", flag, err)
	}

	// Static script state persists across calls in the one worker.
	if _, err := m.Call(context.Background(), "Square", int64(2)); err != nil {
		t.Fatal(err)
	}
	calls, err := m.Call(context.Background(), "Calls")
	if err != nil || calls.Value != int64(2) {
		t.Fatalf("Calls = %#v, %v", calls, err)
	}

	if _, err := m.Call(context.Background(), "Boom"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Boom error = %v", err)
	}

	// After a cancellation the worker restarts from the immutable plan; its
	// persistent state resets but it serves calls again.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _ = m.Call(ctx, "Square", int64(1))
	restart, err := m.Call(context.Background(), "Square", int64(4))
	if err != nil || restart.Value != int64(16) {
		t.Fatalf("restart = %#v, %v", restart, err)
	}
}

func TestPowerShellObjectValuesRoundTrip(t *testing.T) {
	plan, runtime := powerShellPlan(t, `
function Merge {
    param([hashtable]$a, [hashtable]$b)
    $out = @{}
    foreach ($k in $a.Keys) { $out[$k] = $a[$k] }
    foreach ($k in $b.Keys) { $out[$k] = $b[$k] }
    $out
}
`)
	m := Start(plan, runtime)
	defer m.Close()
	got, err := m.Call(context.Background(), "Merge",
		map[string]any{"x": int64(1)}, map[string]any{"y": int64(2)})
	if err != nil {
		t.Fatalf("Merge error: %v", err)
	}
	out, ok := got.Value.(map[string]any)
	if !ok || out["x"] != int64(1) || out["y"] != int64(2) {
		t.Fatalf("Merge = %#v", got.Value)
	}
}

// Discovery fixtures are files, not launched programs. Their names still must
// satisfy the platform executable lookup contract.
func powerShellFixtureExecutable(dir, name string) string {
	if runtime.GOOS == "windows" {
		name += ".EXE"
	}
	return filepath.Join(dir, name)
}

func powerShellFixtureEnviron(path string) []string {
	environ := []string{"PATH=" + path}
	if runtime.GOOS == "windows" {
		environ = append(environ, "PATHEXT=.EXE")
	}
	return environ
}
