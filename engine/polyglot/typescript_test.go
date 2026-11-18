package polyglot

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testTypeScript(t *testing.T) TypeScript {
	t.Helper()
	module := os.Getenv("BASHPP_TYPESCRIPT_MODULE")
	if module == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to an official TypeScript compiler module")
	}
	return TypeScript{CompilerModule: module}
}

func TestTypeScriptAnalyzeAndCall(t *testing.T) {
	ts := testTypeScript(t)
	source := `
interface Pair { left: number; right: number }
type Label = string
const initialized = 40
export function add(a: number, b: number): number { console.log("typescript"); return a + b }
function label(value: Label): string { return value.toUpperCase() }
function dynamic(pair: Pair): Pair { return {left: pair.left + 1, right: pair.right + 1} }
export async function delayed(): Promise<number> { return initialized + 2 }
`
	plans, err := Prepare(context.Background(), []Block{{Language: "ts", Source: source}}, map[string]Analyzer{"typescript": ts})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Language != "typescript" || plans[0].Artifact == "" || len(plans[0].Exports) != 4 {
		t.Fatalf("unexpected plan: %+v", plans)
	}
	module := Start(plans[0], ts)
	defer module.Close()
	result, err := module.Call(context.Background(), "add", float64(20), float64(22))
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != float64(42) || result.Stdout != "typescript\n" {
		t.Fatalf("result = %#v", result)
	}
	result, err = module.Call(context.Background(), "label", "hello")
	if err != nil || result.Value != "HELLO" {
		t.Fatalf("label = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "dynamic", map[string]any{"left": float64(1), "right": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Value.(map[string]any)["left"]; got != int64(2) {
		t.Fatalf("dynamic result = %#v", result.Value)
	}
	result, err = module.Call(context.Background(), "delayed")
	if err != nil || result.Value != float64(42) {
		t.Fatalf("delayed = %#v, %v", result, err)
	}
}

func TestTypeScriptAnalyzeProjectImport(t *testing.T) {
	configured := testTypeScript(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), `{}`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "package.json"), `{"name":"fixture","main":"index.js","types":"index.d.ts"}`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), `exports.base = 40`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "index.d.ts"), `export const base: number`)
	runtime := TypeScript{Environment: &EnvironmentPlan{
		Language: "typescript", Runtime: "node", Executable: node, CompilerModule: configured.CompilerModule,
		Dir: root, Env: os.Environ(),
	}}
	exports, artifact, err := runtime.AnalyzeArtifact(context.Background(), `
import { base } from "fixture"
const increment = 2
export async function answer(): Promise<number> { return base + increment }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Name != "answer" || len(exports[0].Signature.Results) != 1 || exports[0].Signature.Results[0] != "float64" {
		t.Fatalf("exports = %#v", exports)
	}
	module := Start(Plan{Language: "typescript", Artifact: artifact, Exports: exports}, runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "answer")
	if err != nil || result.Value != float64(42) {
		t.Fatalf("answer = %#v, %v", result, err)
	}
}

// A relative source import may name its .ts file explicitly: that is the
// spelling Node's native type stripping requires, and one project tsconfigs
// enable with allowImportingTsExtensions. The fence's checking program must not
// refuse it (TS5097) just because it does not read the project's tsconfig.
func TestTypeScriptAnalyzeRelativeTsExtensionImport(t *testing.T) {
	configured := testTypeScript(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), `{"type":"module"}`)
	writeEnvironmentFile(t, filepath.Join(root, "src", "format.ts"), `export function compact(value: number): string { return value >= 1000 ? (value / 1000) + "k" : String(value) }`)
	runtime := TypeScript{Environment: &EnvironmentPlan{
		Language: "typescript", Runtime: "node", Executable: node, CompilerModule: configured.CompilerModule,
		Dir: root, Env: os.Environ(),
	}}
	exports, artifact, err := runtime.AnalyzeArtifact(context.Background(), `
import { compact } from "./src/format.ts"
export function launch(n: number): string { return compact(n) }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Name != "launch" {
		t.Fatalf("exports = %#v", exports)
	}
	if !strings.Contains(artifact, `"./src/format.ts"`) {
		t.Fatalf("artifact lost the explicit .ts specifier:\n%s", artifact)
	}
	module := Start(Plan{Language: "typescript", Artifact: artifact, Exports: exports}, runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "launch", float64(1500))
	if err != nil || result.Value != "1.5k" {
		t.Fatalf("launch = %#v, %v", result, err)
	}
}

func TestTypeScriptOpenCodeBunImport(t *testing.T) {
	root := os.Getenv("BASHPP_OPENCODE_ROOT")
	if root == "" {
		t.Skip("set BASHPP_OPENCODE_ROOT to exercise the OpenCode workspace")
	}
	compiler := os.Getenv("BASHPP_TYPESCRIPT_MODULE")
	if compiler == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to OpenCode's official TypeScript compiler")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		home, _ := os.UserHomeDir()
		bun = filepath.Join(home, ".bun", "bin", "bun")
		if _, statErr := os.Stat(bun); statErr != nil {
			t.Skip("bun unavailable")
		}
	}
	runtime := TypeScript{Environment: &EnvironmentPlan{
		Language: "typescript", Runtime: "bun", Executable: bun, CompilerModule: compiler,
		Dir: root, SourceDir: filepath.Join(root, "packages", "opencode"), Env: os.Environ(),
	}}
	source := `import { fileInDirectory } from "opencode/config/paths"
export async function extract(): Promise<string> { return fileInDirectory("/tmp", "opencode")[0] }
`
	exports, artifact, err := runtime.AnalyzeArtifact(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	module := Start(Plan{Language: "typescript", Artifact: artifact, Exports: exports}, runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "extract")
	value, _ := result.Value.(string)
	if err != nil || value != "/tmp/opencode.json" {
		t.Fatalf("extract = %#v, %v", result, err)
	}
}

func TestTypeScriptDiagnosticsAndRuntime(t *testing.T) {
	ts := testTypeScript(t)
	for _, source := range []string{
		`function broken(: number): number { return 1 }`,
	} {
		if _, _, err := ts.AnalyzeArtifact(context.Background(), source); err == nil || !strings.Contains(err.Error(), "<bash++ typescript>") {
			t.Fatalf("AnalyzeArtifact(%q) error = %v", source, err)
		}
	}
	exports, artifact, err := ts.AnalyzeArtifact(context.Background(), `function fail(): void { console.error("before"); throw new Error("boom") }`)
	if err != nil {
		t.Fatal(err)
	}
	module := Start(Plan{Language: "typescript", Artifact: artifact, Exports: exports}, ts)
	defer module.Close()
	result, err := module.Call(context.Background(), "fail")
	if err == nil || !strings.Contains(err.Error(), "boom") || result.Stderr != "before\n" {
		t.Fatalf("fail = %#v, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	exports, artifact, err = ts.AnalyzeArtifact(context.Background(), `function spin(): void { while (true) {} } function ok(): string { return "restarted" }`)
	if err != nil {
		t.Fatal(err)
	}
	module = Start(Plan{Language: "typescript", Artifact: artifact, Exports: exports}, ts)
	defer module.Close()
	if _, err := module.Call(ctx, "spin"); err == nil {
		t.Fatal("spin was not cancelled")
	}
	result, err = module.Call(context.Background(), "ok")
	if err != nil || result.Value != "restarted" {
		t.Fatalf("restart = %#v, %v", result, err)
	}
}

func TestTypeScriptWorkerProjectImportAsyncAndFraming(t *testing.T) {
	root := t.TempDir()
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), `{}`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "package.json"), `{"name":"fixture","main":"index.js"}`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), `
console.log("module initialized");
process.stdout.write("unframed module output\n");
exports.answer = async value => { console.log("called"); process.stdout.write("unframed call output\n"); return value + 1; };
`)
	artifact := `
import fixture from "fixture";
export const answer = async value => await fixture.answer(value);
export const identity = () => new (class Example {})();
`
	for _, name := range []string{"node", "bun"} {
		t.Run(name, func(t *testing.T) {
			executable, err := exec.LookPath(name)
			if err != nil && name == "bun" {
				home, _ := os.UserHomeDir()
				executable = filepath.Join(home, ".bun", "bin", "bun")
				if _, statErr := os.Stat(executable); statErr != nil {
					t.Skip("bun unavailable")
				}
			} else if err != nil {
				t.Skip(name + " unavailable")
			}
			runtime := TypeScript{Environment: &EnvironmentPlan{
				Language: "typescript", Runtime: name, Executable: executable, Dir: root, Env: os.Environ(),
			}}
			module := Start(Plan{Language: "typescript", Artifact: artifact}, runtime)
			defer module.Close()
			result, err := module.Call(context.Background(), "answer", float64(41))
			if err != nil {
				t.Fatal(err)
			}
			if result.Value != int64(42) || result.Stdout != "module initialized\ncalled\n" {
				t.Fatalf("result = %#v", result)
			}
			if _, err := module.Call(context.Background(), "identity"); err == nil || !strings.Contains(err.Error(), "unsupported foreign result type") {
				t.Fatalf("identity error = %v", err)
			}
			materialized := module.tempDir
			if err := module.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(materialized); !os.IsNotExist(err) {
				t.Fatalf("materialized module directory was not removed: %v", err)
			}
		})
	}
}

func TestTypeScriptStdoutTransportRequiresMarker(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("{\"id\":99,\"ok\":true}\n\x1eBASHPP{\"id\":1,\"ok\":true}\n"))
	var written bytes.Buffer
	var response workerResponse
	if err := exchange(&written, input, map[string]any{"id": 1}, &response, true, nil); err != nil {
		t.Fatal(err)
	}
	if response.ID != 1 {
		t.Fatalf("response ID = %d, want framed ID 1", response.ID)
	}
}

func TestTypeScriptMissingCompilerModule(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	missing := TypeScript{CompilerModule: filepath.Join(t.TempDir(), "missing-typescript")}
	if _, err := missing.Analyze(context.Background(), `function f(): number { return 1 }`); err == nil || !strings.Contains(err.Error(), "compiler module unavailable") {
		t.Fatalf("missing compiler error = %v", err)
	}
}

func TestTypeScriptToolchainErrorsAndCanonicalName(t *testing.T) {
	missing := TypeScript{NodeCommand: t.TempDir() + "/missing-node"}
	if _, err := missing.Analyze(context.Background(), `function f(): number { return 1 }`); err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("missing Node error = %v", err)
	}
	available := testTypeScript(t)
	missingCompiler := available
	missingCompiler.CompilerModule = t.TempDir() + "/missing-typescript"
	if _, err := missingCompiler.Analyze(context.Background(), `function f(): number { return 1 }`); err == nil || !strings.Contains(err.Error(), "compiler module unavailable") {
		t.Fatalf("missing compiler error = %v", err)
	}
	plans, err := Prepare(context.Background(), []Block{
		{Language: "ts", Alias: "typed", Source: `function one(): number { return 1 }`},
		{Language: "TypeScript", Alias: "typed", Source: `function two(): number { return 2 }`},
	}, map[string]Analyzer{"typescript": available})
	if err != nil || len(plans) != 1 || len(plans[0].Exports) != 2 {
		t.Fatalf("canonical plan = %+v, %v", plans, err)
	}
}

// A first-contact fence imports Node built-ins with no project and no
// @types/node on disk; the check must not refuse them.
func TestTypeScriptNodeBuiltinImportsWithoutNodeTypes(t *testing.T) {
	ts := testTypeScript(t)
	root := t.TempDir()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	ts.Environment = &EnvironmentPlan{Language: "typescript", Runtime: "node", Executable: node, CompilerModule: ts.CompilerModule, Dir: root, Env: os.Environ()}
	source := `
import * as path from "node:path"
import { basename } from "path"
import fs from "fs"
import { readFileSync } from "node:fs"
const sep = process.platform === "win32" ? "\\" : "/"
export function join(a: string, b: string): string { return path.join(a, b) }
export function base(a: string): string { return basename(a) }
export function exists(a: string): boolean { return fs.existsSync(a) }
export function read(a: string): string { return String(readFileSync(a)) }
export function sepOf(): string { return sep }
`
	exports, artifact, err := ts.AnalyzeArtifact(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 5 {
		t.Fatalf("exports = %#v", exports)
	}
	writeEnvironmentFile(t, filepath.Join(root, "data.txt"), "payload")
	module := Start(Plan{Language: "typescript", Artifact: artifact, Exports: exports}, ts)
	defer module.Close()
	for call, want := range map[string]any{"join": path.Join("a", "b"), "base": "b.txt", "exists": true, "read": "payload"} {
		var args []any
		switch call {
		case "join":
			args = []any{"a", "b"}
		case "base":
			args = []any{"a/b.txt"}
		case "exists", "read":
			args = []any{filepath.Join(root, "data.txt")}
		}
		result, err := module.Call(context.Background(), call, args...)
		if err != nil || result.Value != want {
			t.Fatalf("%s = %#v, %v (want %v)", call, result, err, want)
		}
	}
}

// Real @types/node keeps working: the loose fallback declarations must not
// collide with it.
func TestTypeScriptNodeBuiltinImportsWithNodeTypes(t *testing.T) {
	ts := testTypeScript(t)
	types := os.Getenv("BASHPP_NODE_TYPES")
	if types == "" {
		t.Skip("set BASHPP_NODE_TYPES to a @types/node package directory")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), `{}`)
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "@types"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(types, filepath.Join(root, "node_modules", "@types", "node")); err != nil {
		t.Skip("symlink unavailable: " + err.Error())
	}
	ts.Environment = &EnvironmentPlan{Language: "typescript", Runtime: "node", Executable: node, CompilerModule: ts.CompilerModule, Dir: root, Env: os.Environ()}
	if _, _, err := ts.AnalyzeArtifact(context.Background(), `import { basename } from "node:path"
export function base(a: string): string { return basename(a) }`); err != nil {
		t.Fatal(err)
	}
	// With the real types present, a genuine type error is still reported.
	if _, _, err := ts.AnalyzeArtifact(context.Background(), `import { basename } from "node:path"
export function base(a: string): number { return basename(a) }`); err == nil || !strings.Contains(err.Error(), "not assignable") {
		t.Fatalf("type error not reported: %v", err)
	}
}
