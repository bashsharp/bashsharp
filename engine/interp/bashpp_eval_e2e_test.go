// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// End-to-end evidence for the P2 capability policy, through a real runner and
// the reviewed Go toolchain. The classification tests elsewhere in this
// package are pure functions over go-list facts; these run the whole path.

func writeBashPPFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

// runBashPPInDir runs src through a default (policy) runner rooted at dir.
func runBashPPInDir(t *testing.T, dir, src string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r, err := New(StdIO(nil, &stdout, &stderr), Dir(dir), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "t.bpp")
	if err != nil {
		t.Fatal(err)
	}
	runErr := r.Run(context.Background(), f)
	return stdout.String(), stderr.String(), runErr
}

func bashPPLocalModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeBashPPFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.27.0\n")
	writeBashPPFile(t, filepath.Join(dir, "greet", "greet.go"),
		"package greet\n\nimport \"fmt\"\n\nfunc Hello(who string) { fmt.Println(\"hello,\", who) }\n")
	return dir
}

// TestBashPPLocalModuleRunsThroughPolicy proves the capExternalPureGo path end to
// end: a local-module package is classified at import time, passes policy, and
// is built and run by the reviewed toolchain.
func TestBashPPLocalModuleRunsThroughPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go program with the reviewed toolchain")
	}
	dir := bashPPLocalModule(t)
	stdout, stderr, err := runBashPPInDir(t, dir, "import \"example.com/m/greet\"\ngreet.Hello(\"p2\")\n")
	if err != nil {
		t.Fatalf("run: %v (stderr %q)", err, stderr)
	}
	if stdout != "hello, p2\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "hello, p2\n")
	}
	// The reviewed toolchain is the production engine, not a fallback, so a
	// successful evaluation adds no diagnostic of its own.
	if stderr != "" {
		t.Fatalf("unexpected diagnostic: %q", stderr)
	}
}

func TestBashPPStdoutStderrAndNativeDifferentialAreExact(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go programs with the reviewed toolchain")
	}
	dir := t.TempDir()
	writeBashPPFile(t, filepath.Join(dir, "go.mod"), "module example.com/diff\n\ngo 1.27.0\n")
	writeBashPPFile(t, filepath.Join(dir, "probe", "probe.go"), `package probe
import ("fmt"; "os")
func Run() { fmt.Fprintln(os.Stdout, "out"); fmt.Fprintln(os.Stderr, "err"); os.Exit(7) }
`)
	script := "import \"example.com/diff/probe\"\nprobe.Run()\n"
	stdout, stderr, shellErr := runBashPPInDir(t, dir, script)

	direct := `package main
import "example.com/diff/probe"
func main() { probe.Run() }
`
	directOut, directErrOut, directErr := runDirectGoProgram(t, dir, direct)
	if stdout != directOut || stderr != directErrOut || exitCode(shellErr) != exitCode(directErr) {
		t.Fatalf("shell=(%q,%q,%d) direct=(%q,%q,%d)", stdout, stderr, exitCode(shellErr), directOut, directErrOut, exitCode(directErr))
	}
}

func runDirectGoProgram(t *testing.T, dir, src string) (string, string, error) {
	t.Helper()
	writeBashPPFile(t, filepath.Join(dir, "direct.go"), src)
	bin := filepath.Join(dir, "direct-bin")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.Command(goBin, "build", "-o", bin, "direct.go")
	build.Dir = dir
	var buildErr bytes.Buffer
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		t.Fatalf("direct build: %v: %s", err, buildErr.String())
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func TestBashPPPackageInitAndNamespacePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go programs with the reviewed toolchain")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "init.log")
	writeBashPPFile(t, filepath.Join(dir, "go.mod"), "module example.com/lifecycle\n\ngo 1.27.0\n")
	writeBashPPFile(t, filepath.Join(dir, "side", "side.go"), `package side
import "os"
func init() { f, _ := os.OpenFile(os.Getenv("BASHPP_INIT_MARKER"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); if f != nil { f.WriteString("init\n"); f.Close() } }
`)
	var stdout, stderr bytes.Buffer
	r, err := New(StdIO(nil, &stdout, &stderr), Dir(dir),
		Env(expand.ListEnviron(append(os.Environ(), "BASHPP_INIT_MARKER="+marker)...)), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{`import _ "example.com/lifecycle/side"`, `import _ "example.com/lifecycle/side"`, `import "fmt"`, `fmt.Println("persistent")`} {
		f := parseBashPPInternal(t, src+"\n")
		if err := r.Run(context.Background(), f); err != nil {
			t.Fatalf("%s: %v: %s", src, err, stderr.String())
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "persistent\n" || string(data) != "init\n" || len(r.bashPPImports) != 2 {
		t.Fatalf("stdout=%q init=%q imports=%#v", stdout.String(), data, r.bashPPImports)
	}
}

func TestBashPPCancellationStopsNativeEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go programs with the reviewed toolchain")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "late")
	writeBashPPFile(t, filepath.Join(dir, "go.mod"), "module example.com/cancel\n\ngo 1.27.0\n")
	writeBashPPFile(t, filepath.Join(dir, "slow", "slow.go"), `package slow
import ("os"; "time")
func Wait(path string) { time.Sleep(2*time.Second); os.WriteFile(path, []byte("late"), 0600) }
`)
	r, err := New(StdIO(nil, io.Discard, io.Discard), Dir(dir), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), parseBashPPInternal(t, `import "example.com/cancel/slow"`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = r.Run(ctx, parseBashPPInternal(t, `slow.Wait(`+strconv.Quote(marker)+`)`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled evaluation left a side effect: %v", err)
	}
}

// TestBashPPRefusedClassesFailAtImport is the diagnostic-timing evidence. Each
// refused class must fail on the line that NAMED the package, not later at an
// unrelated call site, and must leave the session registry untouched.
func TestBashPPRefusedClassesFailAtImport(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the reviewed Go toolchain")
	}
	dir := bashPPLocalModule(t)
	// package main: Go refuses to import it, and so must the policy.
	writeBashPPFile(t, filepath.Join(dir, "cmdmain", "main.go"), "package main\n\nfunc main() {}\n")
	// A platform mismatch: source exists, but nothing builds on this host.
	writeBashPPFile(t, filepath.Join(dir, "plan9only", "p.go"),
		"//go:build plan9 && ignoreme\n\npackage plan9only\n\nfunc F() {}\n")
	writeBashPPFile(t, filepath.Join(dir, "cgopkg", "p.go"),
		"package cgopkg\n\n/* int bashpp_probe; */\nimport \"C\"\n")

	for _, test := range []struct {
		name, path, want string
	}{
		{"package main", "example.com/m/cmdmain", "not importable"},
		{"platform mismatch", "example.com/m/plan9only", "not importable"},
		{"cgo", "example.com/m/cgopkg", "requires cgo"},
		{"missing", "example.com/m/absent", "could not be resolved"},
		{"unreviewed stdlib", "internal/abi", "reviewed Go standard library"},
	} {
		t.Run(test.name, func(t *testing.T) {
			src := "import " + strconv.Quote(test.path) + "\necho REACHED-NEXT-LINE\n"
			stdout, _, err := runBashPPInDir(t, dir, src)
			if err == nil {
				t.Fatalf("import %q was accepted", test.path)
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want it to mention %q", err, test.want)
			}
			// The failure must stop the script at the import.
			if strings.Contains(stdout, "REACHED-NEXT-LINE") {
				t.Fatalf("execution continued past a refused import: %q", stdout)
			}
		})
	}
}

// TestBashPPRefusedImportLeavesRegistryUntouched pins atomicity: a refused
// import must not half-populate the session namespace.
func TestBashPPRefusedImportLeavesRegistryUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the reviewed Go toolchain")
	}
	dir := bashPPLocalModule(t)
	writeBashPPFile(t, filepath.Join(dir, "cmdmain", "main.go"), "package main\n\nfunc main() {}\n")
	r, err := New(StdIO(nil, &bytes.Buffer{}, &bytes.Buffer{}), Dir(dir), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	src := "import (\n\t\"example.com/m/greet\"\n\t\"example.com/m/cmdmain\"\n)\n"
	f, perr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "t.bpp")
	if perr != nil {
		t.Fatal(perr)
	}
	if err := r.Run(context.Background(), f); err == nil {
		t.Fatal("grouped import containing a refused package was accepted")
	}
	if len(r.bashPPImports) != 0 {
		t.Fatalf("refused group left the registry populated: %#v", r.bashPPImports)
	}
}
