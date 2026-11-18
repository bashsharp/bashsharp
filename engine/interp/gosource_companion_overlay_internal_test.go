package interp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeCompanionOverlayDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
//
// Every Go file of the package the companion build reads must be answered by a
// stub, and each stub must carry that file's package clause and nothing else.
func TestBashPPCompanionRootStubs(t *testing.T) {
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	dir := writeCompanionOverlayDir(t, map[string]string{
		"main.go":                 "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(1) }\n",
		"decl.go":                 "package main\n\nfunc Asm() int\n",
		"helper_" + other + ".go": "package main\n\nfunc unreachable() {}\n",
		"tool.go":                 "//go:build ignore\n\npackage tool\n\nfunc main() {}\n",
		"main_test.go":            "package main\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
		"notes.txt":               "not Go\n",
	})
	stubs, err := bashPPCompanionRootStubs(dir, []string{filepath.Join(dir, "main.go")})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.go", "decl.go"} {
		stub, ok := stubs[filepath.Join(dir, name)]
		if !ok {
			t.Fatalf("no stub for %s; got %v", name, stubKeys(stubs))
		}
		if strings.Contains(stub, "func ") || strings.Contains(stub, "import ") {
			t.Fatalf("stub for %s keeps a body: %q", name, stub)
		}
		if !strings.Contains(stub, "package main\n") {
			t.Fatalf("stub for %s drops its package clause: %q", name, stub)
		}
	}
	// A file the host build context excludes contributes no body to compile,
	// and a stub would carry none of its constraints back.
	for _, name := range []string{"helper_" + other + ".go", "tool.go", "main_test.go", "notes.txt"} {
		if _, ok := stubs[filepath.Join(dir, name)]; ok {
			t.Fatalf("stubbed %s, which this build never compiles", name)
		}
	}
	// The stub set is exactly what the invariant check demands.
	if err := bashPPCompanionOverlayComplete(dir, stubs); err != nil {
		t.Fatalf("bashPPCompanionOverlayComplete: %v", err)
	}
}

// An interpreted input the host build context would have skipped is stubbed
// all the same: the interpreter loaded its body, so the compiler must not.
func TestBashPPCompanionRootStubsCoverInterpretedInputs(t *testing.T) {
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	name := "extra_" + other + ".go"
	dir := writeCompanionOverlayDir(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		name:      "package main\n\nfunc extra() {}\n",
	})
	stubs, err := bashPPCompanionRootStubs(dir, []string{filepath.Join(dir, name)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stubs[filepath.Join(dir, name)]; !ok {
		t.Fatalf("no stub for interpreted input %s; got %v", name, stubKeys(stubs))
	}
}

// The check is what keeps a missed file from being compiled silently.
func TestBashPPCompanionOverlayIncomplete(t *testing.T) {
	dir := writeCompanionOverlayDir(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		"decl.go": "package main\n\nfunc Asm() int\n",
	})
	replace := map[string]string{filepath.Join(dir, "main.go"): "stub"}
	err := bashPPCompanionOverlayComplete(dir, replace)
	if err == nil || !strings.Contains(err.Error(), "would compile original file decl.go") {
		t.Fatalf("bashPPCompanionOverlayComplete err=%v, want a missing-overlay refusal", err)
	}
}

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// A dependency bridge with many mapped assembly companions must not pay one
// cmd/go startup per package before the interpreted tests can begin. The
// exact generated-helper and assembly-selection checks are batched into one
// `go list` invocation.
func TestBashPPMappedCompanionIsolationListsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("focused fake go command uses a POSIX shell script")
	}
	dir := t.TempDir()
	p1 := writeMappedCompanionPackage(t, dir, "p1")
	p2 := writeMappedCompanionPackage(t, dir, "p2")
	counter := filepath.Join(dir, "go-list-count")
	argvLog := filepath.Join(dir, "go-list-argv")
	goTool := filepath.Join(dir, "fake-go")
	script := "#!/bin/sh\n" +
		"printf 'invoked\\n' >> " + shellQuote(counter) + "\n" +
		"printf '%s\\n' \"$@\" > " + shellQuote(argvLog) + "\n" +
		"cat <<'EOF'\n" +
		"example.com/p1\n" +
		"go:a.go\n" +
		"s:asm.s\n" +
		"\n" +
		"--bashpp-mapped-companion--\n" +
		"example.com/p2\n" +
		"go:a.go\n" +
		"s:asm.s\n" +
		"\n" +
		"--bashpp-mapped-companion--\n" +
		"EOF\n"
	if err := os.WriteFile(goTool, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	file := &bashPPImportSource{
		sourceDir: p1,
		overlay:   filepath.Join(dir, "overlay.json"),
		replace: map[string]string{
			filepath.Join(p1, "a.go"): filepath.Join(dir, "stub-p1.go"),
			filepath.Join(p2, "a.go"): filepath.Join(dir, "stub-p2.go"),
		},
	}
	checks := []bashPPMappedCompanionCheck{
		{pkg: bashPPMappedCompanion{Path: "example.com/p1", SourceDir: p1, Files: []string{filepath.Join(p1, "asm.s")}}, generated: "a.go"},
		{pkg: bashPPMappedCompanion{Path: "example.com/p2", SourceDir: p2, Files: []string{filepath.Join(p2, "asm.s")}}, generated: "a.go"},
	}
	req := bashPPEvalRequest{BuildGo: goTool, BuildEnv: os.Environ()}
	if err := bashPPMappedCompanionBuildIsolated(t.Context(), req, file, os.Environ(), checks); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n"); got != 1 {
		t.Fatalf("go list invocations = %d, want 1; log:\n%s", got, data)
	}
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(argv)
	if !strings.Contains(log, "example.com/p1") || !strings.Contains(log, "example.com/p2") {
		t.Fatalf("go list did not receive both packages: %s", log)
	}
}

// The helper source carries its own language version, so the module it is
// physically compiled inside cannot lower it.
func TestBashPPNativeWorkerLanguageVersion(t *testing.T) {
	if !strings.HasPrefix(bashPPNativeWorker, "//go:build go1.") {
		t.Fatalf("generated worker does not open with a language constraint: %q", firstLine(bashPPNativeWorker))
	}
	line, rest, _ := strings.Cut(bashPPNativeWorker, "\n")
	if !strings.HasPrefix(rest, "\n") {
		t.Fatalf("constraint %q is not followed by a blank line", line)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func stubKeys(stubs map[string]string) []string {
	out := make([]string, 0, len(stubs))
	for path := range stubs {
		out = append(out, filepath.Base(path))
	}
	return out
}

func writeMappedCompanionPackage(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package "+name+"\n\nfunc Asm() int\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "asm.s"), []byte("TEXT Asm(SB), NOSPLIT, $0-8\nRET\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
