package polyglot

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPythonDoesNotImportFromImplicitWorkingDirectory(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	pythonPath := filepath.Join(t.TempDir(), "recorded")
	if err := os.MkdirAll(pythonPath, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ast.py", "contextlib.py"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("raise RuntimeError('cwd shadow imported')\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pythonPath, "sitecustomize.py"), []byte("raise RuntimeError('sitecustomize ran before bootstrap')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "PYTHONNOUSERSITE=1", "PYTHONPATH=" + pythonPath}
	runtime := Python{Command: python, Environment: &EnvironmentPlan{Dir: dir, Env: env, PythonPath: []string{pythonPath}}}
	plans, err := Prepare(context.Background(), []Block{{Language: "python", Source: "def ok(): return 1\n"}}, map[string]Analyzer{"python": runtime})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], runtime)
	defer module.Close()
	got, err := module.Call(context.Background(), "ok")
	if err != nil || got.Value != int64(1) {
		t.Fatalf("call = %#v, %v", got, err)
	}
}

func TestPythonPreservesRecordedWorkingDirectoryPythonPath(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "recorded.py"), []byte("value = 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "PYTHONNOUSERSITE=1", "PYTHONPATH=" + dir}
	runtime := Python{Command: python, Environment: &EnvironmentPlan{Dir: dir, Env: env, PythonPath: []string{dir}}}
	plan := pythonPlanWithRuntime(t, "def load():\n    import recorded\n    return recorded.value\n", runtime)
	module := Start(plan, runtime)
	defer module.Close()
	got, err := module.Call(context.Background(), "load")
	if err != nil || got.Value != int64(7) {
		t.Fatalf("call = %#v, %v", got, err)
	}
}

func pythonPlanWithRuntime(t *testing.T, source string, runtime Python) Plan {
	t.Helper()
	plans, err := Prepare(context.Background(), []Block{{Language: "python", Source: source}}, map[string]Analyzer{"python": runtime})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %d", len(plans))
	}
	return plans[0]
}

func TestPythonUnavailable(t *testing.T) {
	python := Python{Command: t.TempDir() + "/missing-python"}
	_, err := python.Analyze(context.Background(), "def f(): pass\n")
	if err == nil || !strings.Contains(err.Error(), "Python runtime unavailable") {
		t.Fatalf("analyze error = %v", err)
	}
	m := Start(Plan{Language: "python", Source: "def f(): pass\n"}, python)
	_, err = m.Call(context.Background(), "f")
	if err == nil || !strings.Contains(err.Error(), "Python runtime unavailable") {
		t.Fatalf("call error = %v", err)
	}
	if closeErr := m.Close(); closeErr != nil && !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("close = %v", closeErr)
	}
}

func pythonPlan(t *testing.T, source string) Plan {
	return pythonPlanWithRuntime(t, source, Python{})
}

func TestPythonAnalyzeDeclarationOnlyAndSignatures(t *testing.T) {
	plan := pythonPlan(t, `"""module docs"""
def add(a: int, b: int) -> int:
    return a+b
def loose(value):
    return value
def _private():
    return 1
`)
	if len(plan.Exports) != 2 {
		t.Fatalf("exports = %#v", plan.Exports)
	}
	if got := plan.Exports[0]; got.Name != "add" || got.Signature.Dynamic || strings.Join(got.Signature.Params, ",") != "int,int" || strings.Join(got.Signature.Results, ",") != "int" {
		t.Fatalf("typed export = %#v", got)
	}
	if !plan.Exports[1].Signature.Dynamic {
		t.Fatalf("dynamic export = %#v", plan.Exports[1])
	}
	for _, source := range []string{
		"class X: pass\n", "async def f(): pass\n", "@staticmethod\ndef f(): pass\n",
		"print('side effect')\n", "import os\nx = os.sep\n", "x = [i for i in range(3)]\n",
		"x: int\n", "a, b = 1, 2\n", "x = y = len\n", "for i in range(2): pass\n",
	} {
		if _, err := Prepare(context.Background(), []Block{{Language: "python", Source: source}}, map[string]Analyzer{"python": Python{}}); err == nil {
			t.Fatalf("accepted %q", source)
		}
	}
}

func TestPythonModuleLevelImportsAndLiteralConstants(t *testing.T) {
	source := `"""module docs"""
from __future__ import annotations
import os.path as osp
import json, math
from collections import OrderedDict as OD
from typing import Any

SCALE = 3
NAME: str = "fence"
LIMITS = {"lo": -1, "hi": [1, 2.5, None, True]}
A = B = 7

def scaled(n: int) -> int:
    return n * SCALE + A + B

def describe(value: Any) -> str:
    return json.dumps({"name": NAME, "limits": LIMITS, "sqrt": math.sqrt(16), "sep": osp.basename("a/b.txt"), "od": list(OD(x=1))}, sort_keys=True)
`
	plan := pythonPlan(t, source)
	if len(plan.Exports) != 2 || plan.Exports[0].Name != "scaled" || plan.Exports[1].Name != "describe" {
		t.Fatalf("exports = %#v", plan.Exports)
	}
	module := Start(plan, Python{})
	defer module.Close()
	result, err := module.Call(context.Background(), "scaled", int64(2))
	if err != nil || result.Value != int64(20) {
		t.Fatalf("scaled = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "describe", nil)
	want := `{"limits": {"hi": [1, 2.5, null, true], "lo": -1}, "name": "fence", "od": ["x"], "sep": "b.txt", "sqrt": 4.0}`
	if err != nil || result.Value != want {
		t.Fatalf("describe = %#v, %v", result, err)
	}
}

func TestPythonModuleImportFailureNamesPackageAndNeverInstalls(t *testing.T) {
	plan := pythonPlan(t, "import bashpp_definitely_missing_package\n\ndef f() -> int:\n    return 1\n")
	module := Start(plan, Python{})
	defer module.Close()
	_, err := module.Call(context.Background(), "f")
	if err == nil || !strings.Contains(err.Error(), "bashpp_definitely_missing_package") {
		t.Fatalf("import failure = %v", err)
	}
	detail, ok := ForeignErrorDetail(err)
	if !ok || detail.Code != "ModuleNotFoundError" {
		t.Fatalf("detail = %#v, %v", detail, ok)
	}
}

func TestPythonPlanIDChangesWithImportLine(t *testing.T) {
	a := pythonPlan(t, "import json\n\ndef f() -> int:\n    return 1\n")
	b := pythonPlan(t, "import math\n\ndef f() -> int:\n    return 1\n")
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("fingerprints %q %q", a.ID, b.ID)
	}
}

func TestPythonPersistentCallsValuesOutputAndRestart(t *testing.T) {
	plan := pythonPlan(t, `
def add(a: int, b: int) -> int:
    print("called")
    return a+b
def blob(value: bytes) -> bytes:
    return value+b"!"
def fail():
    raise ValueError("boom")
def lies() -> int:
    return "not an int"
def hang():
    while True: pass
`)
	m := Start(plan, Python{})
	defer m.Close()
	got, err := m.Call(context.Background(), "add", int64(2), int64(3))
	if err != nil || got.Value != int64(5) || got.Stdout != "called\n" {
		t.Fatalf("call = %#v, %v", got, err)
	}
	blob, err := m.Call(context.Background(), "blob", []byte("x"))
	if err != nil || string(blob.Value.([]byte)) != "x!" {
		t.Fatalf("blob = %#v, %v", blob, err)
	}
	if _, err := m.Call(context.Background(), "fail"); err == nil || !strings.Contains(err.Error(), "ValueError: boom") {
		t.Fatalf("error = %v", err)
	} else {
		detail, ok := ForeignErrorDetail(err)
		if !ok || detail.Code != "ValueError" || detail.Message != "boom" || !strings.Contains(detail.Help, "Traceback") {
			t.Fatalf("structured Python error = %#v, ok=%t", detail, ok)
		}
	}
	if _, err := m.Call(context.Background(), "lies"); err == nil || !strings.Contains(err.Error(), "violated its int result annotation") {
		t.Fatalf("annotation error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.Call(ctx, "hang"); err == nil {
		t.Fatal("hang was not canceled")
	}
	got, err = m.Call(context.Background(), "add", int64(4), int64(5))
	if err != nil || got.Value != int64(9) {
		t.Fatalf("restart = %#v, %v", got, err)
	}
}

func TestWorkerEnvelopeErrorCompatibility(t *testing.T) {
	// Source-derived contract fixture for Sprint 221 B4.
	//
	// CPython exception source: python/cpython
	// 23116f998f6789d8c2fbe5ed5b8146854c8c2a4f, Lib/test/test_exceptions.py
	// `testChainingAttrs` and Doc/library/exceptions.rst exception context,
	// PSF-2.0.
	//
	// Rust source: serde-rs/json v1.0.151
	// 8d25f3af9f94471a75a18f04da4ca4cdb3cb5f64, tests/test.rs
	// `test_missing_nonoption_field` and Rust std Result documentation as
	// shipped with rustc 1.93.1; MIT OR Apache-2.0 for serde_json, MIT OR
	// Apache-2.0 for rust-lang/rust library docs.
	tests := []struct {
		name      string
		frame     string
		wantCode  string
		wantText  string
		wantHelp  string
		wantCause string
	}{
		{
			name:     "structured error",
			frame:    `{"id":1,"ok":false,"error":{"code":"ValueError","message":"boom","help":"raise ValueError('boom')"},"stdout":"out\n","stderr":"err\n"}`,
			wantCode: "ValueError", wantText: "ValueError: boom", wantHelp: "raise ValueError",
		},
		{
			name:     "legacy string",
			frame:    `{"id":1,"ok":false,"error":"plain boom"}`,
			wantText: "plain boom",
		},
		{
			name:     "missing fields",
			frame:    `{"id":1,"ok":false,"error":{"code":"KeyError"}}`,
			wantCode: "KeyError", wantText: "KeyError",
		},
		{
			name:      "nested cause",
			frame:     `{"id":1,"ok":false,"error":{"code":"RuntimeError","message":"outer","cause":{"code":"ValueError","message":"inner"}}}`,
			wantCode:  "RuntimeError",
			wantText:  "RuntimeError: outer: ValueError: inner",
			wantCause: "ValueError",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := bufio.NewReader(strings.NewReader(tt.frame + "\n"))
			var written bytes.Buffer
			var response workerResponse
			if err := exchange(&written, input, map[string]any{"id": 1}, &response, false, nil); err != nil {
				t.Fatal(err)
			}
			err := response.Error.err()
			if err == nil || err.Error() != tt.wantText {
				t.Fatalf("error = %q, want %q", err, tt.wantText)
			}
			detail, ok := ForeignErrorDetail(err)
			if !ok {
				t.Fatalf("error does not expose structured detail: %T", err)
			}
			if detail.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", detail.Code, tt.wantCode)
			}
			if tt.wantHelp != "" && !strings.Contains(detail.Help, tt.wantHelp) {
				t.Fatalf("help = %q, want contains %q", detail.Help, tt.wantHelp)
			}
			if tt.wantCause != "" {
				cause := errors.Unwrap(err)
				causeDetail, ok := ForeignErrorDetail(cause)
				if !ok || causeDetail.Code != tt.wantCause {
					t.Fatalf("cause = %#v, ok=%t, want code %q", causeDetail, ok, tt.wantCause)
				}
			}
		})
	}
}

func TestWorkerEnvelopeTransportFailure(t *testing.T) {
	var response workerResponse
	err := exchange(&bytes.Buffer{}, bufio.NewReader(strings.NewReader("{not-json\n")), map[string]any{"id": 1}, &response, false, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid worker response") {
		t.Fatalf("transport error = %v", err)
	}
	if detail, ok := ForeignErrorDetail(err); ok {
		t.Fatalf("transport failure became envelope error: %#v", detail)
	}
}

func TestPythonNestedCauseStructuredError(t *testing.T) {
	plan := pythonPlan(t, `
def fail():
    try:
        raise ValueError("inner")
    except ValueError as exc:
        raise RuntimeError("outer") from exc
`)
	module := Start(plan, Python{})
	defer module.Close()
	_, err := module.Call(context.Background(), "fail")
	detail, ok := ForeignErrorDetail(err)
	if !ok || detail.Code != "RuntimeError" || detail.Cause == nil || detail.Cause.Code != "ValueError" {
		t.Fatalf("nested Python error = %#v, ok=%t (%v)", detail, ok, err)
	}
}

func TestPythonStructuredErrorsConcurrent(t *testing.T) {
	plan := pythonPlan(t, `
def fail():
    raise ValueError("boom")
`)
	module := Start(plan, Python{})
	defer module.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := module.Call(context.Background(), "fail")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		detail, ok := ForeignErrorDetail(err)
		if !ok || detail.Code != "ValueError" || detail.Message != "boom" {
			t.Fatalf("concurrent structured error = %#v, ok=%t (%v)", detail, ok, err)
		}
	}
}

func TestPythonCauseSerializationIsBounded(t *testing.T) {
	plan := pythonPlan(t, `
def self_cycle():
    err = ValueError("cycle")
    err.__cause__ = err
    raise err
def deep_chain():
    err = ValueError("root")
    for i in range(40):
        outer = RuntimeError("level %d" % i)
        outer.__cause__ = err
        err = outer
    raise err
`)
	module := Start(plan, Python{})
	defer module.Close()
	for _, name := range []string{"self_cycle", "deep_chain"} {
		_, err := module.Call(context.Background(), name)
		detail, ok := ForeignErrorDetail(err)
		if !ok {
			t.Fatalf("%s detail = %#v, ok=%t, err=%v", name, detail, ok, err)
		}
		depth := 0
		for d := detail; d != nil; d = d.Cause {
			depth++
			if depth > 18 {
				t.Fatalf("%s cause chain was not bounded", name)
			}
		}
	}
}

func TestPythonStructuredAnnotationsAndObjectCodec(t *testing.T) {
	// Source-derived boundary matrix: CPython v3.14.4
	// (23116f998f6789d8c2fbe5ed5b8146854c8c2a4f),
	// Doc/library/json.rst and Lib/test/test_json, PSF-2.0. The cases retain
	// CPython's nested list/dict and empty-container shapes while making the
	// Bash# boundary stricter: bytes are explicit non-JSON values.
	plan := pythonPlan(t, `
def records() -> list[dict[str, int]]:
    return [{"n": 1}, {"n": 2}]
def bounce(value: dict[str, list[int]]) -> dict[str, list[int]]:
    return value
def empty() -> list[dict[str, int]]:
    return []
def invalid() -> list[bytes]:
    return [b"not-json"]
`)
	for _, export := range plan.Exports {
		if export.Signature.Dynamic {
			t.Fatalf("structured export %s is dynamic: %#v", export.Name, export.Signature)
		}
	}
	m := Start(plan, Python{})
	defer m.Close()

	wantRecords := []any{map[string]any{"n": int64(1)}, map[string]any{"n": int64(2)}}
	got, err := m.Call(context.Background(), "records")
	if err != nil || !reflect.DeepEqual(got.Value, wantRecords) {
		t.Fatalf("records = %#v, %v", got.Value, err)
	}
	wantMap := map[string]any{"items": []any{int64(3), int64(4)}}
	got, err = m.Call(context.Background(), "bounce", wantMap)
	if err != nil || !reflect.DeepEqual(got.Value, wantMap) {
		t.Fatalf("bounce = %#v, %v", got.Value, err)
	}
	got, err = m.Call(context.Background(), "empty")
	if err != nil || !reflect.DeepEqual(got.Value, []any{}) {
		t.Fatalf("empty = %#v, %v", got.Value, err)
	}
	if _, err := m.Call(context.Background(), "invalid"); err == nil || !strings.Contains(err.Error(), "non-JSON") {
		t.Fatalf("invalid structured result error = %v", err)
	}
}

func TestPrepareAggregationAliasesAndIdentity(t *testing.T) {
	blocks := []Block{{Language: "python", Alias: "py", Source: "def a(): return 1\n"}, {Language: "PYTHON", Alias: "py", Source: "def b(): return 2\n"}}
	a, err := Prepare(context.Background(), blocks, map[string]Analyzer{"python": Python{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Prepare(context.Background(), blocks, map[string]Analyzer{"python": Python{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(a[0].Exports) != 2 || a[0].ID != b[0].ID || a[0].Alias != "py" {
		t.Fatalf("plans = %#v %#v", a, b)
	}
	blocks[1].Alias = "other"
	if _, err := Prepare(context.Background(), blocks, map[string]Analyzer{"python": Python{}}); err == nil {
		t.Fatal("inconsistent aliases accepted")
	}
}

func TestCanonicalLanguageAliases(t *testing.T) {
	for in, want := range map[string]string{"python": "python", "Python": "python", "py": "python", "PY": "python", "ts": "typescript", "typescript": "typescript", " py ": "python", "rust": "rust", "rs": "rust", "RS": "rust", "c": "c", "cpp": "cpp", "cxx": "cpp", "CXX": "cpp"} {
		if got := CanonicalLanguage(in); got != want {
			t.Fatalf("CanonicalLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
