package interp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint: #270; Story: #756; Story-ID: a0503101d15a
func TestGoSourceS270DependencyBridgeSkipsFuncPCIntrinsics(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go:      goBin,
		Dir:     filepath.Join(runtime.GOROOT(), "src", "cmd", "compile"),
		Env:     os.Environ(),
		Imports: map[string]string{"abi": "internal/abi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"FuncPCABI0", "FuncPCABIInternal"} {
		if strings.Contains(source, "."+name+")") {
			t.Fatalf("dependency bridge takes compiler intrinsic %s as a function value", name)
		}
	}
	buildS270DependencyWorker(t, source, filepath.Join(runtime.GOROOT(), "src", "cmd", "compile"))
}

// A two-argument linkname is part of a bodyless declaration's link identity,
// not decoration which the helper may discard. Build the generated worker so
// this regression checks the linker result, not only the generated text.
func TestGoSourceS270DependencyBridgePreservesFunctionLinkname(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a generated dependency worker")
	}
	program, err := gosource.Parse(strings.NewReader(`package main
import _ "unsafe"

//go:linkname linked runtime.nanotime
func linked() int64
`), "linkname.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	_, funcs, _, _, err := runner.bashPPGoSourceNativeCompanions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(funcs) != 1 || funcs[0].Linkname != "runtime.nanotime" {
		t.Fatalf("native funcs = %+v, want preserved runtime.nanotime linkname", funcs)
	}
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Imports:     map[string]string{"_:unsafe": "unsafe"},
		NativeFuncs: funcs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "//go:linkname linked runtime.nanotime\nfunc linked()") {
		t.Fatal("generated dependency worker dropped function linkname")
	}
	buildS270DependencyWorker(t, source, t.TempDir())
}

// Package maps flatten imported names, but source directives still name the
// original declaration. Keep the target when the worker sees that renamed name.
func TestGoSourceS270DependencyBridgePreservesFlattenedFunctionLinkname(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
import _ "unsafe"

//go:linkname linked runtime.nanotime
func __gosource_pkg_0_linked() int64
`), "linkname.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	_, funcs, _, _, err := runner.bashPPGoSourceNativeCompanions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(funcs) != 1 || funcs[0].Name != "__gosource_pkg_0_linked" || funcs[0].Linkname != "runtime.nanotime" {
		t.Fatalf("native funcs = %+v, want flattened local name and original linker target", funcs)
	}
}

func buildS270DependencyWorker(t *testing.T, source, buildDir string) {
	t.Helper()
	mailboxImports, mailboxSource := bashPPMailboxWorkerSource(false)
	source = strings.Replace(source, "//CALLBACKMAILBOXIMPORTS", mailboxImports, 1)
	source = strings.Replace(source, "//CALLBACKMAILBOX", mailboxSource, 1)
	source = strings.Replace(source, "//CONNECTION", `const bridgeNetwork = "tcp"
const bridgeAddress = "127.0.0.1:1"
const bridgeAuth = "0123456789abcdef0123456789abcdef"
const callbackMailboxPath = ""`, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bashPPBuildWorkerImportcfg(context.Background(), filepath.Join(runtime.GOROOT(), "bin", "go"), buildDir, os.Environ(), "", dir, path, filepath.Join(dir, "worker"), nil, ""); err != nil {
		t.Fatalf("build generated dependency worker: %v", err)
	}
}
