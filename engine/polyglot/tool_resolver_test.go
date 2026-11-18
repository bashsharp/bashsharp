package polyglot

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A fence never resolves its tool from PATH once the embedder sets
// ToolResolver: a decoy on PATH is invisible, the resolver's argv prefix
// rides into every plan, and nil restores the PATH lookup.
func TestToolResolverReplacesPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	decoyDir := filepath.Join(root, "decoy")
	provDir := filepath.Join(root, "provisioned")
	for _, name := range []string{"python3", "go", "clang", "cc", "rustc", "node"} {
		writeEnvironmentFile(t, filepath.Join(decoyDir, name), "#!/bin/sh\nexit 1\n")
	}
	zig := filepath.Join(provDir, "zig")
	writeEnvironmentFile(t, zig, "#!/bin/sh\nexit 0\n")
	for _, name := range []string{"python3", "go", "rustc", "node"} {
		writeEnvironmentFile(t, filepath.Join(provDir, name), "#!/bin/sh\nexit 0\n")
	}
	if err = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environ := []string{"PATH=" + decoyDir}

	var asked []string
	ToolResolver = func(name string) ([]string, string, error) {
		asked = append(asked, name)
		switch name {
		case "clang", "cc", "clang++", "c++":
			return []string{zig, "cc"}, "selected provisioned zig cc", nil
		case "python3", "go", "rustc", "node":
			return []string{filepath.Join(provDir, name)}, "selected provisioned " + name, nil
		case "typescript":
			return []string{filepath.Join(provDir, "typescript")}, "selected provisioned typescript", nil
		}
		return nil, "", errors.New("no record for " + name)
	}
	t.Cleanup(func() { ToolResolver = nil })

	for _, language := range []string{"python", "go", "c", "cpp", "rust", "typescript"} {
		plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: language, Environ: environ})
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		if strings.HasPrefix(plan.Executable, decoyDir) {
			t.Fatalf("%s: selected the PATH decoy %s", language, plan.Executable)
		}
		if !strings.HasPrefix(plan.Executable, provDir) {
			t.Fatalf("%s: executable %s is not the provisioned one", language, plan.Executable)
		}
		if !strings.Contains(strings.Join(plan.Explanation, "\n"), "selected provisioned") {
			t.Fatalf("%s: explanation %q does not name the resolver", language, plan.Explanation)
		}
		if language == "c" || language == "cpp" {
			if len(plan.ExecutableArgs) != 1 || plan.ExecutableArgs[0] != "cc" {
				t.Fatalf("%s: leading args %q, want [cc]", language, plan.ExecutableArgs)
			}
		} else if len(plan.ExecutableArgs) != 0 {
			t.Fatalf("%s: unexpected leading args %q", language, plan.ExecutableArgs)
		}
		if language == "typescript" && plan.CompilerModule != filepath.Join(provDir, "typescript") {
			t.Fatalf("typescript: compiler module %q did not come from the resolver", plan.CompilerModule)
		}
	}
	if len(asked) == 0 {
		t.Fatal("resolver was never consulted")
	}

	// The fingerprint carries the prefix: the same executable with different
	// leading arguments is a different plan.
	a, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: "c", Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	ToolResolver = func(name string) ([]string, string, error) { return []string{zig, "c++"}, "", nil }
	b, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: "c", Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint == b.Fingerprint {
		t.Fatal("fingerprint ignores the leading arguments")
	}

	// The explicit override still names the program, resolver or not.
	override := filepath.Join(root, "mine", "python3")
	writeEnvironmentFile(t, override, "#!/bin/sh\nexit 0\n")
	plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: "python", Environ: append(environ, "BASHPP_PYTHON="+override)})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable != override {
		t.Fatalf("BASHPP_PYTHON lost to the resolver: %s", plan.Executable)
	}

	// A resolver that cannot provide the tool fails the island with its reason.
	ToolResolver = func(name string) ([]string, string, error) { return nil, "", errors.New("no digest for this platform") }
	if _, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: "rust", Environ: environ}); err == nil || !strings.Contains(err.Error(), "no digest for this platform") {
		t.Fatalf("resolver error not surfaced: %v", err)
	}

	// nil restores the PATH rung: the decoy is what PATH offers.
	ToolResolver = nil
	plan, err = DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: "python", Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Executable, decoyDir) {
		t.Fatalf("without a resolver PATH should win: %s", plan.Executable)
	}
}

func TestZigDriverOnlyFailure(t *testing.T) {
	if !zigDriverOnlyFailure("zig: warning: argument unused during compilation: '-c'\n/tmp/x/module.c:1:1: error: FileNotFound\n") {
		t.Fatal("zig's post-dump FileNotFound must be tolerated")
	}
	if zigDriverOnlyFailure("/tmp/x/module.c:3:5: error: use of undeclared identifier 'y'\n/tmp/x/module.c:1:1: error: FileNotFound\n") {
		t.Fatal("a clang diagnostic must keep the failure")
	}
	if zigDriverOnlyFailure("") {
		t.Fatal("no error line is not the zig quirk")
	}
	if !zigDriverOnlyFailure("In file included from C:\\zig\\lib\\libcxx\\include/system_error:152:\nC:\\zig\\lib\\libcxx\\include/string:1078:80: warning: pointer is missing a nullability type specifier\nC:\\t\\module.cpp:1:1: error: FileNotFound\n") {
		t.Fatal("an include path containing 'error:' is not a diagnostic")
	}
}

// A BASHPP_* override names a program explicitly: a bare name resolves on
// PATH and never reaches the resolver, absolute or not.
func TestOverridesBypassResolver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pathDir := filepath.Join(root, "path")
	for _, name := range []string{"gcc", "rustc-mine", "node-mine", "go-mine"} {
		writeEnvironmentFile(t, filepath.Join(pathDir, name), "#!/bin/sh\nexit 0\n")
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ToolResolver = func(name string) ([]string, string, error) {
		return nil, "", errors.New("resolver must not be asked for " + name)
	}
	t.Cleanup(func() { ToolResolver = nil })
	for language, environ := range map[string][]string{
		"c":          {"PATH=" + pathDir, "BASHPP_CC=gcc"},
		"rust":       {"PATH=" + pathDir, "BASHPP_RUSTC=rustc-mine"},
		"typescript": {"PATH=" + pathDir, "BASHPP_NODE=node-mine", "BASHPP_TYPESCRIPT_MODULE=" + root},
		"go":         {"PATH=" + pathDir, "BASHPP_GO=go-mine"},
	} {
		plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bsh"), Language: language, Environ: environ})
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		if !strings.HasPrefix(plan.Executable, pathDir) {
			t.Fatalf("%s: override did not resolve on PATH: %s", language, plan.Executable)
		}
		if language == "rust" && len((Rust{Environment: &plan}).linkerArgs()) != 0 {
			t.Fatal("an overridden rustc must keep its own linker")
		}
	}
}
