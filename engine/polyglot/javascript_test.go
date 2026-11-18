package polyglot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testJavaScript(t *testing.T) TypeScript {
	t.Helper()
	ts := testTypeScript(t)
	ts.JavaScript = true
	return ts
}

// typeScriptSevenOrLater reports whether the configured compiler is the native
// TypeScript 7 line, whose API has no JSDoc reader.
func typeScriptSevenOrLater(t *testing.T, ts TypeScript) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ts.CompilerModule, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	major, _, _ := strings.Cut(manifest.Version, ".")
	return major != "" && major != "3" && major != "4" && major != "5" && major != "6"
}

func TestJavaScriptFenceAnalyzeAndCall(t *testing.T) {
	js := testJavaScript(t)
	source := `
import { basename } from "node:path"
const bonus = 2
/**
 * @param {number} a
 * @param {number} b
 * @returns {number}
 */
export function add(a, b) { console.log("javascript"); return a + b + bonus }
function untyped(value) { return value.toUpperCase() }
function inferred() { return 1 }
function defaulted(n = 3) { return n * 2 }
export async function later() { return basename("a/b.txt") }
`
	plans, err := Prepare(context.Background(), []Block{{Language: "js", Source: source}}, map[string]Analyzer{"javascript": js})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Language != "javascript" || plans[0].Artifact == "" || len(plans[0].Exports) != 5 {
		t.Fatalf("unexpected plan: %+v", plans)
	}
	byName := map[string]Export{}
	for _, e := range plans[0].Exports {
		byName[e.Name] = e
	}
	if got := byName["add"].Signature; !typeScriptSevenOrLater(t, js) && (got.Dynamic || strings.Join(got.Params, ",") != "float64,float64" || strings.Join(got.Results, ",") != "float64") {
		t.Fatalf("documented signature = %#v", got)
	}
	for _, name := range []string{"untyped", "defaulted"} {
		if !byName[name].Signature.Dynamic {
			t.Fatalf("%s must be dynamic without JSDoc: %#v", name, byName[name].Signature)
		}
	}
	if got := byName["inferred"].Signature; strings.Join(got.Results, ",") != "any" {
		t.Fatalf("an inferred return must stay any: %#v", got)
	}
	module := Start(plans[0], js)
	defer module.Close()
	result, err := module.Call(context.Background(), "add", float64(20), float64(20))
	if err != nil || fmt.Sprint(result.Value) != "42" || result.Stdout != "javascript\n" {
		t.Fatalf("add = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "untyped", "hi")
	if err != nil || result.Value != "HI" {
		t.Fatalf("untyped = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "later")
	if err != nil || result.Value != "b.txt" {
		t.Fatalf("later = %#v, %v", result, err)
	}
	if _, err := module.Call(context.Background(), "untyped", int64(1)); err == nil {
		t.Fatal("a JavaScript runtime error was swallowed")
	}
}

func TestJavaScriptFenceDiagnosticsAndTypesAreNotChecked(t *testing.T) {
	js := testJavaScript(t)
	_, _, err := js.AnalyzeArtifact(context.Background(), `function broken(: number) { return 1 }`)
	if err == nil || !strings.Contains(err.Error(), "<bash++ javascript>") {
		t.Fatalf("syntax error = %v", err)
	}
	// A JavaScript fence is not type checked: this is valid JavaScript.
	exports, _, err := js.AnalyzeArtifact(context.Background(), `function f() { const x = 1; x.nope.deeper; return missingGlobal }`)
	if err != nil || len(exports) != 1 {
		t.Fatalf("exports = %#v, %v", exports, err)
	}
}

func TestJavaScriptFenceImportsProjectPackage(t *testing.T) {
	js := testJavaScript(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), `{}`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "package.json"), `{"name":"fixture","main":"index.js"}`)
	writeEnvironmentFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), `exports.base = 40`)
	js.Environment = &EnvironmentPlan{Language: "javascript", Runtime: "node", Executable: node, CompilerModule: js.CompilerModule, Dir: root, Env: os.Environ()}
	exports, artifact, err := js.AnalyzeArtifact(context.Background(), `import { base } from "fixture"
export function answer() { return base + 2 }`)
	if err != nil {
		t.Fatal(err)
	}
	module := Start(Plan{Language: "javascript", Artifact: artifact, Exports: exports}, js)
	defer module.Close()
	result, err := module.Call(context.Background(), "answer")
	if err != nil || result.Value != int64(42) {
		t.Fatalf("answer = %#v, %v", result, err)
	}
}

func TestJavaScriptFenceUnavailableRuntimeNamesJavaScript(t *testing.T) {
	missing := TypeScript{JavaScript: true, NodeCommand: t.TempDir() + "/missing-node"}
	if _, err := missing.Analyze(context.Background(), `function f() { return 1 }`); err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("missing Node error = %v", err)
	}
	if missing.name() != "JavaScript" {
		t.Fatalf("name = %q", missing.name())
	}
}
