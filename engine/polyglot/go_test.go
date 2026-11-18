package polyglot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoCommandInvocationBoundary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	const source = `import (
	"fmt"
	"io"
	"os"
)
func Inspect() {
	input, _ := io.ReadAll(os.Stdin)
	wd, _ := os.Getwd()
	fmt.Printf("argv=%q input=%q cwd=%q env=%q\n", os.Args[1:], input, wd, os.Getenv("GO_COMMAND_TEST"))
	fmt.Fprintln(os.Stderr, "guest stderr")
}
func Zero() { os.Exit(0) }
func Three() { os.Exit(3) }
func Panic() { panic("native panic") }
func Typed() string { fmt.Print("\x1eBASHPP{\"ok\":false}\n"); return "typed value" }
func WithParam(value string) {}
`
	root := t.TempDir()
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := Prepare(context.Background(), []Block{{Language: "go", Source: source}}, map[string]Analyzer{"go": Go{}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := Go{Cwd: func() string { return root }, Env: func() []string { return append(os.Environ(), "GO_COMMAND_TEST=guest") }}
	module := Start(plans[0], runtime)
	defer module.Close()
	got, err := module.Command(context.Background(), "Inspect", []string{"-flag", "two words", ""}, strings.NewReader("line\n"))
	if err != nil || got.Status != 0 || got.Stderr != "guest stderr\n" || !strings.Contains(got.Stdout, `argv=["-flag" "two words" ""]`) || !strings.Contains(got.Stdout, `input="line\n"`) || !strings.Contains(got.Stdout, `cwd="`+physicalRoot+`"`) || !strings.Contains(got.Stdout, `env="guest"`) {
		t.Fatalf("Inspect = %#v, %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		status int
		stderr string
	}{
		{"Zero", 0, ""}, {"Three", 3, ""}, {"Panic", 2, "native panic"},
	} {
		got, err := module.Command(context.Background(), tc.name, nil, nil)
		if err != nil || got.Status != tc.status || !strings.Contains(got.Stderr, tc.stderr) {
			t.Errorf("%s = %#v, %v", tc.name, got, err)
		}
	}
	if _, err := module.Command(context.Background(), "WithParam", []string{"value"}, nil); !errors.Is(err, ErrCommandNotCallable) {
		t.Fatalf("WithParam command error = %v", err)
	}
	if _, err := module.Command(context.Background(), "Missing", nil, nil); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("Missing command error = %v", err)
	}
	typed, err := module.Call(context.Background(), "Typed")
	if err != nil || typed.Value != "typed value" || !strings.Contains(typed.Stdout, "\x1eBASHPP") {
		t.Fatalf("typed result = %#v, %v", typed, err)
	}
}

func TestGoCommandStreamsBeforeExitAndCancels(t *testing.T) {
	const source = `import ("fmt"; "time")
func Ready() { fmt.Println("ready"); time.Sleep(30*time.Second) }
`
	plans, err := Prepare(context.Background(), []Block{{Language: "go", Source: source}}, map[string]Analyzer{"go": Go{}})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], Go{})
	defer module.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	writer := &goReadyWriter{ready: ready}
	done := make(chan error, 1)
	go func() {
		_, err := module.CommandStreams(ctx, "Ready", nil, nil, writer, io.Discard)
		done <- err
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Go command exited before readiness: %v; output %q", err, writer.String())
	case <-time.After(5 * time.Second):
		t.Fatalf("Go command stdout was not streamed before exit: output %q, ready len %d", writer.String(), len(ready))
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Go command did not exit")
	}
}

func TestGoCommandCallerContextsAreIsolated(t *testing.T) {
	t.Setenv("BPP_GO_CALL_CONTEXT_TYPED_PROBE", "")
	const source = `import ("fmt"; "os"; "path/filepath")
func Inspect() { wd, _ := os.Getwd(); fmt.Printf("%s:%s\n", filepath.Base(wd), os.Getenv("BPP_GO_CALL_CONTEXT")) }
func TypedEnv() string { return os.Getenv("BPP_GO_CALL_CONTEXT_TYPED_PROBE") }
`
	plans, err := Prepare(context.Background(), []Block{{Language: "go", Source: source}}, map[string]Analyzer{"go": Go{}})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], Go{})
	defer module.Close()
	type outcome struct {
		got CommandResult
		err error
	}
	result := make(chan outcome, 2)
	for _, label := range []string{"alpha", "beta"} {
		root := filepath.Join(t.TempDir(), label)
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		go func() {
			ctx := WithGoCommandCaller(context.Background(), root, append(os.Environ(), "BPP_GO_CALL_CONTEXT="+label))
			got, err := module.Command(ctx, "Inspect", nil, nil)
			result <- outcome{got, err}
		}()
	}
	seen := map[string]bool{}
	for range 2 {
		item := <-result
		if item.err != nil || item.got.Status != 0 {
			t.Fatalf("command = %#v, %v", item.got, item.err)
		}
		seen[item.got.Stdout] = true
	}
	if !seen["alpha:alpha\n"] || !seen["beta:beta\n"] || len(seen) != 2 {
		t.Fatalf("caller contexts mixed: %v", seen)
	}
	typedCtx := WithGoCommandCaller(context.Background(), "", []string{"BPP_GO_CALL_CONTEXT_TYPED_PROBE=command-only"})
	typed, err := module.Call(typedCtx, "TypedEnv")
	if err != nil || typed.Value != "" {
		t.Fatalf("typed call inherited command context: %#v, %v", typed, err)
	}
}

type goReadyWriter struct {
	buffer bytes.Buffer
	ready  chan struct{}
}

func (w *goReadyWriter) String() string { return w.buffer.String() }

func (w *goReadyWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	if n > 0 {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestGoArtifactUsesModuleOverlayAndBridgesValues(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.local/project\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project.go"), []byte("package project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "version", "version.go"), []byte("package version\nconst Value = \"module\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := directoryNames(t, root)
	environment, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "dag.md"), Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := Go{Environment: &environment}
	plans, err := Prepare(context.Background(), []Block{{Language: "go", Source: `
import "example.local/project/version"
import "fmt"
func Add(a int64, b int64) int64 { fmt.Println(version.Value); return a+b }
func Blob(value []byte) []byte { return append(value, 0xff) }
func Checked(value int64) (int64, error) { if value < 0 { return 0, fmt.Errorf("negative") }; return value, nil }
`}}, map[string]Analyzer{"go": runtime})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Exports) != 3 || plans[0].Artifact == "" {
		t.Fatalf("plan = %#v", plans)
	}
	if after := directoryNames(t, root); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("overlay build changed checkout: before=%v after=%v", before, after)
	}
	module := Start(plans[0], runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "Add", int64(20), int64(22))
	if err != nil || result.Value != int64(42) || result.Stdout != "module\n" {
		t.Fatalf("Add = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "Blob", []byte{0, 1})
	if err != nil || string(result.Value.([]byte)) != string([]byte{0, 1, 0xff}) {
		t.Fatalf("Blob = %#v, %v", result, err)
	}
	if _, err := module.Call(context.Background(), "Checked", int64(-1)); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("Checked error = %v", err)
	}
}

func TestGoRejectsUnsupportedSurface(t *testing.T) {
	for _, source := range []string{
		"func value() int { return 1 }\n",
		"func main() {}\nfunc Value() int { return 1 }\n",
	} {
		exports, err := analyzeGoExports(source)
		if err == nil && len(exports) != 0 {
			t.Fatalf("accepted %q as %#v", source, exports)
		}
	}
}

func TestGoTopLevelDeclarations(t *testing.T) {
	const source = `import "fmt"
type Person struct { Name string }
func (p Person) String() string { return p.Name }
func (p *Person) Main() { p.Name = "ready" }
type Stringer interface { String() string }
type Box[T any] struct { Value T }
type Alias = Person
const ( First = iota; Second )
var State = First
func init() { State = Second }
func init() { State++ }
func Main() { p := Person{}; p.Main(); var s Stringer = p; fmt.Println(Box[string]{s.String()}, State) }
`
	module, exports, err := analyzeGoModule(source)
	if err != nil {
		t.Fatal(err)
	}
	if module != "package main\n"+source {
		t.Fatal("module declarations were not preserved")
	}
	if len(exports) != 1 || exports[0].Name != "Main" || exports[0].Unavailable != "" {
		t.Fatalf("exports = %#v, want only Main", exports)
	}
}

func directoryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func TestGoAnalyzeVoidExports(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		resultError  bool
	}{
		{"implicit", "func Main() {}", false},
		{"explicit", "func Main() () {}", false},
		{"error only", "func Main() error { return nil }", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exports, err := analyzeGoExports(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if len(exports) != 1 {
				t.Fatalf("exports = %#v", exports)
			}
			got := exports[0]
			if got.Name != "Main" || len(got.Signature.Results) != 0 || got.result != "" || got.resultError != tc.resultError {
				t.Fatalf("export = %#v", got)
			}
		})
	}
}

func TestGoModuleInternalDeclarations(t *testing.T) {
	source := `func SlicesIndex[S ~[]E, E comparable](s S, v E) int { return -1 }
func Variadic(v ...int) {}
func Unsupported(v []int) {}
func Result() []int { return nil }
func Many() (int, int, error) { return 0, 0, nil }
func Main() { SlicesIndex([]int{1}, 1); Variadic(1); Unsupported(nil); Result(); Many() }
`
	plans, err := Prepare(context.Background(), []Block{{Language: "go", Source: source}}, map[string]Analyzer{"go": Go{}})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	if len(plan.Exports) != 1 || plan.Exports[0].Name != "Main" || len(plan.UnavailableExports) != 5 {
		t.Fatalf("exports=%+v unavailable=%+v", plan.Exports, plan.UnavailableExports)
	}
	module := Start(plan, Go{})
	defer module.Close()
	if _, err := module.Call(context.Background(), "Main"); err != nil {
		t.Fatal(err)
	}
	for _, export := range plan.UnavailableExports {
		if _, err := module.Call(context.Background(), export.Name); err == nil || !strings.Contains(err.Error(), export.UnavailableDiagnostic()) {
			t.Errorf("%s: %v", export.Name, err)
		}
	}
	for _, source := range []string{
		"func private() {}",
		"func Variadic(v ...int) {}",
	} {
		if _, _, err := (Go{}).AnalyzeArtifact(context.Background(), source); err != nil {
			t.Errorf("zero callable exports: %v", err)
		}
	}
	for _, source := range []string{
		"func Broken(",
		"func Duplicate[T any](x T) {}\nfunc Duplicate(v ...int) {}",
		"func Broken(v ...int) { undefined() }",
	} {
		if _, _, err := (Go{}).AnalyzeArtifact(context.Background(), source); err == nil {
			t.Errorf("accepted invalid module %q", source)
		}
	}
}
