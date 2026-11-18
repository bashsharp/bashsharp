package polyglot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCSharpLanguageRow(t *testing.T) {
	for _, spelling := range []string{"csharp", "cs", "CS"} {
		if got := CanonicalLanguage(spelling); got != "csharp" {
			t.Fatalf("CanonicalLanguage(%q) = %q", spelling, got)
		}
	}
	row, ok := LookupLanguage("cs")
	if !ok || !row.NeedsEnvironment || !row.LineDirectives || row.LoweredRuntime == nil {
		t.Fatalf("csharp row = %#v", row)
	}
	if got := row.LoweredRuntime("p.", "nil"); got != "p.polyglot.CSharp{Environment:nil}" {
		t.Fatalf("lowered runtime = %q", got)
	}
}

// C# resolves the same pwsh as the PowerShell row: one pinned archive serves
// both fences, and there is no project manifest.
func TestDiscoverCSharpEnvironmentUsesPwsh(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	pwshName := "pwsh"
	environ := []string{"PATH=" + bin}
	if runtime.GOOS == "windows" {
		pwshName += ".EXE"
		environ = append(environ, "PATHEXT=.EXE")
	}
	pwsh := filepath.Join(bin, pwshName)
	writeEnvironmentFile(t, pwsh, "fixture pwsh")
	plan, err := DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "program.bpp"), Language: "cs", Environ: environ,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canonicalExecutable(pwsh, nil)
	if plan.Language != "csharp" || plan.Runtime != "pwsh" || plan.Executable != canonical || len(plan.ResolutionFiles) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
	_, err = DiscoverEnvironment(EnvironmentRequest{
		Source: filepath.Join(root, "program.bpp"), Language: "csharp", Environ: []string{"PATH="},
	})
	if err == nil || !strings.Contains(err.Error(), "C# runtime (pinned PowerShell) unavailable") {
		t.Fatalf("unavailable error = %v", err)
	}
}

// The generated unit keeps leading using lines before a generated namespace,
// wraps the members in a static class, and maps every line back to the fence.
func TestCSharpGenerateMapsFenceLines(t *testing.T) {
	source := aggregateSource("csharp", []Block{
		{Language: "csharp", Filename: `C:\work\flow "a".bsh`, Line: 4, Source: "using System;\nusing System.Linq;\n\npublic static long Square(long x) => x * x;\n"},
		{Language: "csharp", Filename: "flow.bsh", Line: 20, Source: "using static System.Math;\npublic static double Root(double x) => Sqrt(x);\n"},
	})
	generated, typeName, err := csharpGenerate(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(typeName, "BashPP.CSharp.M") || !strings.HasSuffix(typeName, ".Fence") {
		t.Fatalf("type name = %q", typeName)
	}
	want := []string{
		`#line 4 "C:\work\flow 'a'.bsh"`,
		"using System;",
		"using System.Linq;",
		"",
		`#line 20 "flow.bsh"`,
		"using static System.Math;",
		csharpBodyMarker(typeName),
		`#line 7 "C:\work\flow 'a'.bsh"`,
		"public static long Square(long x) => x * x;",
		`#line 21 "flow.bsh"`,
		"public static double Root(double x) => Sqrt(x);",
	}
	if got := strings.Split(strings.TrimSuffix(generated, "\n"), "\n"); !slices.Equal(got, want) {
		t.Fatalf("generated:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The namespace is per source: another fence gets another type.
	_, other, err := csharpGenerate("public static int One() => 1;\n")
	if err != nil || other == typeName {
		t.Fatalf("namespace not per source: %q %q %v", other, typeName, err)
	}
}

const cSharpBodyMarkerCollisionFixture = `// <bashpp-csharp-body-v2> trailing user text
using System;

public static int Run() => 42;
`

// A user comment may contain the old fixed delimiter text. It remains
// original source and cannot move the generated header/body boundary.
func TestCSharpGeneratePreservesBodyMarkerComment(t *testing.T) {
	generated, typeName, err := csharpGenerate(cSharpBodyMarkerCollisionFixture)
	if err != nil {
		t.Fatal(err)
	}
	commentAt := strings.Index(generated, "// <bashpp-csharp-body-v2> trailing user text")
	markerAt := strings.Index(generated, csharpBodyMarker(typeName))
	if commentAt < 0 || markerAt <= commentAt {
		t.Fatalf("generated source lost or misordered user comment:\n%s", generated)
	}
}

func TestCSharpBodyMarkerCommentCompiles(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, cSharpBodyMarkerCollisionFixture)
	m := Start(plan, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(42) {
		t.Fatalf("Run = %#v, %v", got, err)
	}
}

func TestCSharpRefusesPackageDeclarations(t *testing.T) {
	for _, directive := range []string{`#:package Newtonsoft.Json@13.0.3`, `#r "nuget: Newtonsoft.Json, 13.0.3"`} {
		source := aggregateSource("csharp", []Block{{Language: "csharp", Filename: "flow.bsh", Line: 3, Source: "using System;\n" + directive + "\npublic static int One() => 1;\n"}})
		_, _, err := csharpGenerate(source)
		if err == nil || !strings.Contains(err.Error(), "flow.bsh:4:") || !strings.Contains(err.Error(), "NuGet") || !strings.Contains(err.Error(), CSharpNuGetFollowUp) {
			t.Fatalf("%s: error = %v", directive, err)
		}
	}
}

func cSharpRuntime(t *testing.T) CSharp {
	t.Helper()
	return CSharp{Command: requirePowerShell(t), CacheDir: t.TempDir()}
}

func cSharpPlan(t *testing.T, runtime CSharp, source string) Plan {
	t.Helper()
	plans, err := Prepare(context.Background(), []Block{{Language: "csharp", Filename: "flow.bsh", Line: 1, Source: source}}, map[string]Analyzer{"csharp": runtime})
	if err != nil {
		t.Fatal(err)
	}
	return plans[0]
}

const cSharpFixture = `using System;
using System.Collections.Generic;
using System.Linq;

public static long Square(long x) => x * x;
public static string Greet(string name) { Console.WriteLine("greeting " + name); return "hello " + name; }
public static double Half(double x) => x / 2;
public static bool Even(int n) => n % 2 == 0;
public static byte[] Bang(byte[] data) => data.Concat(new byte[] { 33 }).ToArray();
public static long[] Range(int n) => Enumerable.Range(1, n).Select(i => (long)i).ToArray();
public static Point Make(int x, string label) => new Point { X = x, Label = label };
public static int Fail(string why) => throw new InvalidOperationException(why);
public static int Count() => ++calls;
public static void Nothing() { }
static int calls;
static int Hidden() => 0;
public class Point { public int X { get; set; } public string Label = ""; }
`

func TestCSharpAnalyzeExportsByReflection(t *testing.T) {
	plan := cSharpPlan(t, cSharpRuntime(t), cSharpFixture)
	var names []string
	byName := map[string]Signature{}
	for _, e := range plan.Exports {
		names = append(names, e.Name)
		byName[e.Name] = e.Signature
	}
	if want := []string{"Square", "Greet", "Half", "Even", "Bang", "Range", "Make", "Fail", "Count", "Nothing"}; !slices.Equal(names, want) {
		t.Fatalf("exports = %v, want %v", names, want)
	}
	for name, want := range map[string]string{
		"Square": "int->int", "Greet": "string->string", "Half": "float64->float64", "Even": "int->bool",
		"Bang": "bytes->bytes", "Range": "int->object", "Make": "int,string->object", "Nothing": "->",
	} {
		sig := byName[name]
		if got := strings.Join(sig.Params, ",") + "->" + strings.Join(sig.Results, ","); got != want || sig.Dynamic {
			t.Fatalf("%s signature = %s (dynamic %v), want %s", name, got, sig.Dynamic, want)
		}
	}
}

func TestCSharpTypedCallsErrorsAndState(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, cSharpFixture)
	m := Start(plan, runtime)
	defer m.Close()
	ctx := context.Background()

	if got, err := m.Call(ctx, "Square", int64(6)); err != nil || got.Value != int64(36) {
		t.Fatalf("Square = %#v, %v", got, err)
	}
	greet, err := m.Call(ctx, "Greet", "bashy")
	if err != nil || greet.Value != "hello bashy" || greet.Stdout != "greeting bashy\n" {
		t.Fatalf("Greet = %#v, %v", greet, err)
	}
	if got, err := m.Call(ctx, "Half", 5.0); err != nil || got.Value != 2.5 {
		t.Fatalf("Half = %#v, %v", got, err)
	}
	if got, err := m.Call(ctx, "Even", int64(4)); err != nil || got.Value != true {
		t.Fatalf("Even = %#v, %v", got, err)
	}
	if got, err := m.Call(ctx, "Bang", []byte("hi")); err != nil || string(got.Value.([]byte)) != "hi!" {
		t.Fatalf("Bang = %#v, %v", got, err)
	}
	if got, err := m.Call(ctx, "Range", int64(3)); err != nil || !slices.Equal(got.Value.([]any), []any{int64(1), int64(2), int64(3)}) {
		t.Fatalf("Range = %#v, %v", got, err)
	}
	point, err := m.Call(ctx, "Make", int64(7), "seven")
	if err != nil {
		t.Fatal(err)
	}
	if obj, ok := point.Value.(map[string]any); !ok || obj["X"] != int64(7) || obj["Label"] != "seven" {
		t.Fatalf("Make = %#v", point.Value)
	}
	if got, err := m.Call(ctx, "Nothing"); err != nil || got.Value != nil {
		t.Fatalf("Nothing = %#v, %v", got, err)
	}
	_, err = m.Call(ctx, "Fail", "nope")
	detail, ok := ForeignErrorDetail(err)
	if !ok || detail.Code != "InvalidOperationException" || detail.Message != "nope" {
		t.Fatalf("Fail error = %v (%#v)", err, detail)
	}
	// Static state persists in the one worker across calls, including after a
	// failed call.
	for want := int64(1); want <= 2; want++ {
		if got, err := m.Call(ctx, "Count"); err != nil || got.Value != want {
			t.Fatalf("Count = %#v, %v; want %d", got, err, want)
		}
	}
	if _, err := m.Call(ctx, "Square", "x"); err == nil {
		t.Fatal("Square(\"x\") succeeded")
	}
	if _, err := m.Call(ctx, "Hidden"); err == nil || !strings.Contains(err.Error(), "no C# method Hidden") {
		t.Fatalf("Hidden error = %v", err)
	}
}

// Every fence uses one deterministic compiler path. Enabling unsafe code is
// permissive for ordinary C# and avoids a source-text heuristic disagreeing
// between analysis and worker-side recompilation.
func TestCSharpUnsafeCode(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, `public static unsafe int Read() {
    int value = 42;
    int* pointer = &value;
    return *pointer;
}
`)
	m := Start(plan, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Read"); err != nil || got.Value != int64(42) {
		t.Fatalf("Read = %#v, %v", got, err)
	}
}

// Compilation-unit declarations stay outside the generated Fence while the
// free methods remain its only exports. Exact source slices carry directives,
// comments and #line trivia through the Roslyn-backed partition.
func TestCSharpCompilationUnitNamespacesExtensionAndTrivia(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, `#define USE_FIRST
using Helpers;

#if USE_FIRST
namespace First {
    // Same short name is intentional.
    public class Person { public int Value => 10; }
}
#endif
namespace Second { public class Person { public int Value => 20; } }
namespace Helpers {
    public static class Ext { public static int Twice(this int value) => value * 2; }
}
public static unsafe int Run() {
    int value = new First.Person().Value + new Second.Person().Value;
    int* pointer = &value;
    return (*pointer).Twice();
}
`)
	var names []string
	for _, export := range plan.Exports {
		names = append(names, export.Name)
	}
	if got := names; !slices.Equal(got, []string{"Run"}) {
		t.Fatalf("exports = %v", got)
	}
	m := Start(plan, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(60) {
		t.Fatalf("Run = %#v, %v", got, err)
	}
}

const cSharpCompilationUnitUnsafeFixture = `using Helpers;

namespace Models {
    public class Value { public int Number => 21; }
}
namespace Helpers {
    public static class Ext {
        public static unsafe int Twice(this int value) {
            int* pointer = &value;
            return *pointer * 2;
        }
    }
}
public static int Run() => new Models.Value().Number.Twice();
`

func TestCSharpFileScopedNamespace(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, `namespace Example;
using System.Collections.Generic;
public class Person { public int Value => 42; }
// This free fence member must stay on the generated export type, outside
// the equivalent block-scoped user namespace, while retaining the file
// namespace's unqualified type and using-directive lookup.
public static int Run() => new Person().Value + new List<int> { 1 }.Count;
`)
	if len(plan.Exports) != 1 || plan.Exports[0].Name != "Run" {
		t.Fatalf("exports = %#v", plan.Exports)
	}
	m := Start(plan, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(43) {
		t.Fatalf("Run = %#v, %v", got, err)
	}
}

func TestCSharpTopLevelExtensionClass(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, `static class Ext {
    public static int Twice(this int value) => value * 2;
}
public static int Run() => 3.Twice();
`)
	if len(plan.Exports) != 1 || plan.Exports[0].Name != "Run" {
		t.Fatalf("exports = %#v", plan.Exports)
	}
	m := Start(plan, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(6) {
		t.Fatalf("Run = %#v, %v", got, err)
	}
}

// Compiler errors name the script file and the fence's own lines — never the
// generated wrapper's — and leave no cache entry behind.
func TestCSharpCompileErrorsUseFenceLines(t *testing.T) {
	runtime := cSharpRuntime(t)
	_, err := Prepare(context.Background(), []Block{{Language: "csharp", Filename: "flow.bsh", Line: 10, Source: "using System;\n\npublic static int Ok() => 1;\npublic static int Bad() => missing + 1;\n"}}, map[string]Analyzer{"csharp": runtime})
	if err == nil || !strings.Contains(err.Error(), "flow.bsh:13:") || !strings.Contains(err.Error(), "CS0103") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("compile error = %v", err)
	}
	_, err = Prepare(context.Background(), []Block{{Language: "csharp", Filename: "flow.bsh", Line: 10, Source: "using Nope.Missing;\npublic static int Ok() => 1;\n"}}, map[string]Analyzer{"csharp": runtime})
	if err == nil || !strings.Contains(err.Error(), "flow.bsh:10:") {
		t.Fatalf("using error = %v", err)
	}
	_, err = Prepare(context.Background(), []Block{{Language: "csharp", Filename: "flow.bsh", Line: 10, Source: "namespace Broken {\npublic class Value { public int Number => missing; }\n}\npublic static int Ok() => 1;\n"}}, map[string]Analyzer{"csharp": runtime})
	if err == nil || !strings.Contains(err.Error(), "flow.bsh:11:") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("namespace error = %v", err)
	}
	_, err = Prepare(context.Background(), []Block{{Language: "csharp", Filename: "flow.bsh", Line: 10, Source: "namespace Broken;\npublic class Value { public int Number => 1; }\n    public static int Bad() => missing + 1;\n"}}, map[string]Analyzer{"csharp": runtime})
	if err == nil {
		t.Fatal("file-scoped namespace compile succeeded")
	}
	location := regexp.MustCompile(`flow\.bsh:[0-9]+:[0-9]+:`).FindString(err.Error())
	if location != "flow.bsh:12:32:" || !strings.Contains(err.Error(), "CS0103") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("file-scoped namespace error = %v (location %q)", err, location)
	}
	entries, _ := os.ReadDir(runtime.CacheDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".dll") || strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("failed compile left %s in the cache", entry.Name())
		}
	}
	// An overload compiles but is refused at prepare: an export is one name
	// with one signature.
	_, err = Prepare(context.Background(), []Block{{Language: "csharp", Filename: "flow.bsh", Line: 10, Source: "public static int A(int x) => x;\npublic static int A(string x) => 1;\n"}}, map[string]Analyzer{"csharp": runtime})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("overload error = %v", err)
	}
}

// An unchanged fence on an unchanged toolchain is analyzed and loaded from the
// cache: no second compile, and the cached assembly is the one that runs.
func TestCSharpSecondRunDoesNotRecompile(t *testing.T) {
	runtime := cSharpRuntime(t)
	source := cSharpCompilationUnitUnsafeFixture
	before := csharpCompiles.Load()
	plan := cSharpPlan(t, runtime, source)
	if csharpCompiles.Load() != before+1 {
		t.Fatalf("first run compiled %d times", csharpCompiles.Load()-before)
	}
	unit, err := runtime.unit(plan.Source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(unit.assembly)
	if err != nil {
		t.Fatalf("no cached assembly: %v", err)
	}
	m := Start(plan, runtime)
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(42) {
		t.Fatalf("first Run = %#v, %v", got, err)
	}
	m.Close()

	second := cSharpPlan(t, runtime, source)
	if csharpCompiles.Load() != before+1 {
		t.Fatal("second run recompiled")
	}
	m = Start(second, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(42) {
		t.Fatalf("second Run = %#v, %v", got, err)
	}
	after, err := os.Stat(unit.assembly)
	if err != nil || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("cached assembly rewritten: %v", err)
	}
	// A changed body is a different key and compiles again.
	cSharpPlan(t, runtime, "public static long Square(long x) => x * x * 1;\n")
	if csharpCompiles.Load() != before+2 {
		t.Fatal("changed fence did not compile")
	}
}

// A cache entry that disappears between prepare and the first call is
// recompiled by the worker rather than failing the call.
func TestCSharpWorkerRecompilesMissingAssembly(t *testing.T) {
	runtime := cSharpRuntime(t)
	plan := cSharpPlan(t, runtime, cSharpCompilationUnitUnsafeFixture)
	unit, err := runtime.unit(plan.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(unit.assembly); err != nil {
		t.Fatal(err)
	}
	m := Start(plan, runtime)
	defer m.Close()
	if got, err := m.Call(context.Background(), "Run"); err != nil || got.Value != int64(42) {
		t.Fatalf("Run = %#v, %v", got, err)
	}
	if _, err := os.Stat(unit.assembly); err != nil {
		t.Fatalf("worker did not restore the cache entry: %v", err)
	}
}

// The v2 salt covers both the normalized compilation-unit shape and the
// deterministic /unsafe compiler option. A complete-looking v1 entry must not
// suppress compilation or supply exports after either behavior changes.
func TestCSharpStaleV1CacheIsNotReused(t *testing.T) {
	runtimePlan := cSharpRuntime(t)
	aggregated := aggregateSource("csharp", []Block{{
		Language: "csharp", Filename: "flow.bsh", Line: 1, Source: cSharpCompilationUnitUnsafeFixture,
	}})
	generated, _, err := csharpGenerate(aggregated)
	if err != nil {
		t.Fatal(err)
	}
	toolchain, err := csharpToolchainHash(runtimePlan.executable(), leadingArgs(runtimePlan.Environment))
	if err != nil {
		t.Fatal(err)
	}
	v1sum := sha256.Sum256([]byte("bashpp-csharp-v1\x00" + runtime.GOOS + "/" + runtime.GOARCH + "\x00" + toolchain + "\x00" + generated))
	v1base := filepath.Join(runtimePlan.CacheDir, hex.EncodeToString(v1sum[:]))
	if err := os.WriteFile(v1base+".dll", []byte("stale-v1-assembly"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v1base+".json", []byte(`[{"name":"Stale","signature":{"params":[],"results":["int"],"dynamic":false}}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	before := csharpCompiles.Load()
	exports, err := runtimePlan.Analyze(context.Background(), aggregated)
	if err != nil {
		t.Fatal(err)
	}
	if csharpCompiles.Load() != before+1 {
		t.Fatal("stale v1 entry suppressed the v2 compilation")
	}
	if len(exports) != 1 || exports[0].Name != "Run" {
		t.Fatalf("exports = %#v, want Run", exports)
	}
	v2, err := runtimePlan.unit(aggregated)
	if err != nil {
		t.Fatal(err)
	}
	if v2.assembly == v1base+".dll" {
		t.Fatal("v1 and v2 cache paths are identical")
	}
	if _, err := os.Stat(v2.assembly); err != nil {
		t.Fatalf("v2 assembly missing: %v", err)
	}
}
