//go:build full

package interp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashPPImportScratchOverlay(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	scratch := filepath.Join(root, "private")
	for _, dir := range []string{filepath.Join(module, "internal", "dep"), scratch} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"go.mod": "module example.test/overlay\n\ngo 1.26.5\n", "internal/dep/dep.go": "package dep\nconst Value = \"private dependency\"\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(module, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A read-only original module must remain usable; only scratch is writable.
	if err := os.Chmod(module, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(module, 0700) })
	alias := filepath.Join(root, "module-alias")
	if err := os.Symlink(module, alias); err != nil {
		t.Fatal(err)
	}
	env := setEnvString(os.Environ(), "TMPDIR", scratch)
	env = setEnvString(env, "GOWORK", "off")
	f, err := bashPPImportTempSource(alias, "helper-*.go", env, bashPPScratchIsolated)
	if err != nil {
		t.Fatal(err)
	}
	defer f.cleanup()
	if _, err = f.WriteString("package main\nimport(\"fmt\";\"example.test/overlay/internal/dep\")\nfunc main(){fmt.Print(dep.Value)}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = os.Stat(filepath.Dir(f.buildPath)); !os.IsNotExist(err) {
		t.Fatalf("virtual directory exists: %v", err)
	}
	info, err := os.Stat(f.Name())
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("private input mode: %v %v", info, err)
	}
	binary := f.Name() + ".bin"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p=2", "-overlay="+f.overlay, "-o", binary, f.buildPath)
	cmd.Dir = alias
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real overlay build: %v\n%s", err, out)
	}
	if out, err := exec.Command(binary).CombinedOutput(); err != nil || string(out) != "private dependency" {
		t.Fatalf("real artifact: %v %q", err, out)
	}
	for name, body := range files {
		if got, err := os.ReadFile(filepath.Join(module, name)); err != nil || string(got) != body {
			t.Fatalf("original mutated: %s", name)
		}
	}
	entries, err := os.ReadDir(module)
	if err != nil || len(entries) != 2 {
		t.Fatalf("source tree entries: %v %v", entries, err)
	}
	f.cleanup()
	entries, err = os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch not cleaned: %v %v", entries, err)
	}
}

// On Windows the default OS temp directory lives under the user profile
// (%LOCALAPPDATA%\Temp), so a shell run from the home directory — or any
// ancestor of that temp directory — sees the default temp nested inside its
// own working ("source") directory. That is not an explicit TMPDIR pointed
// into a read-only module, so an imported-package helper must still build
// instead of being refused. Without this an `import "net/url"` worker failed
// on Windows and a readonly mutation surfaced the wrong diagnostic. Reproduced
// portably by pointing the process temp at a subdirectory of the source root
// while leaving the request env's TMPDIR unset — exactly the default-temp shape.
func TestBashPPImportScratchToleratesDefaultTempUnderSource(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(root, "nested-temp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp) // unix: os.TempDir reads $TMPDIR
	t.Setenv("TMP", tmp)    // windows: GetTempPath reads %TMP%/%TEMP%
	t.Setenv("TEMP", tmp)
	resolved, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil || !pathWithin(root, resolved) {
		t.Skipf("default temp %q not under source %q: %v", os.TempDir(), root, err)
	}
	// The request env carries no TMPDIR, so the default OS temp is in force; a
	// default temp nested under the source dir must not arm the isolation guard.
	env := bashPPEnvWithout(os.Environ(), "TMPDIR", "TMP", "TEMP")
	f, err := bashPPImportTempSource(root, "helper-*.go", env, bashPPScratchIsolated)
	if err != nil {
		t.Fatalf("default temp under source rejected: %v", err)
	}
	f.Close()
	f.cleanup()
}

func TestBashPPImportScratchRejectsSourceTMPDIR(t *testing.T) {
	root := t.TempDir()
	for _, tmp := range []string{root, filepath.Join(root, "missing")} {
		f, err := bashPPImportTempSource(root, "helper-*.go", setEnvString(os.Environ(), "TMPDIR", tmp), bashPPScratchIsolated)
		if err == nil {
			f.Close()
			f.cleanup()
			t.Fatal("source TMPDIR accepted")
		}
		if tmp == root && !strings.Contains(err.Error(), "inside the source") {
			t.Fatal(err)
		}
	}
	// Tolerating a source-tree scratch root must not also start tolerating a
	// TMPDIR that does not resolve: Classic still needs a real directory.
	f, err := bashPPImportTempSource(root, "helper-*.go", setEnvString(os.Environ(), "TMPDIR", filepath.Join(root, "missing")), bashPPScratchSourceTree)
	if err == nil {
		f.Close()
		f.cleanup()
		t.Fatal("missing TMPDIR accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed request altered source: %v %v", entries, err)
	}
}

// The Classic bash++ import profile deliberately points TMPDIR at the exec
// source root: its helpers have always been evaluated from a dot directory
// under the importer. Isolation is a GoSource requirement, so a source-tree
// TMPDIR must keep working here instead of failing the whole call. The cases
// mirror the import shapes of the legacy profile's stdlib-import fixtures.
func TestBashPPImportClassicAllowsSourceTMPDIR(t *testing.T) {
	cases := []struct {
		name     string
		imports  map[string]string
		selector []string
		args     []string
		want     string
		values   bool
	}{
		{name: "named", imports: map[string]string{"fmt": "fmt"}, selector: []string{"fmt", "Print"}, args: []string{`"incremental"`}, want: "incremental"},
		{name: "dot", imports: map[string]string{".:fmt": "fmt"}, selector: []string{"Print"}, args: []string{`"dot"`}, want: "dot"},
		{name: "alias-blank", imports: map[string]string{"fmt": "fmt", "_:strings": "strings"}, selector: []string{"fmt", "Print"}, args: []string{`"alias"`}, want: "alias"},
		{name: "values", imports: map[string]string{"strings": "strings"}, selector: []string{"strings", "Repeat"}, args: []string{`"package"`, `1`}, want: "package", values: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/classic\n\ngo 1.25\n"), 0600); err != nil {
				t.Fatal(err)
			}
			goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
			if runtime.GOOS == "windows" {
				goBin += ".exe"
			}
			env := setEnvString(os.Environ(), "GOTOOLCHAIN", "local")
			env = setEnvString(env, "GOWORK", "off")
			// Exactly the legacy profile shape: TMPDIR inside the source root.
			env = setEnvString(env, "TMPDIR", root)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			req := bashPPEvalRequest{Go: goBin, Dir: root, Env: env, Stdout: &stdout, Stderr: &stderr,
				Imports: tc.imports, Selector: tc.selector, Args: tc.args}
			if tc.values {
				req.Results = 1
				values, err := (nativeBashPPEvaluator{}).Values(ctx, req)
				if err != nil {
					t.Fatalf("classic values with source TMPDIR: %v\n%s", err, stderr.String())
				}
				if len(values) != 1 || values[0] != tc.want {
					t.Fatalf("values = %v, want %q", values, tc.want)
				}
			} else {
				if err := (nativeBashPPEvaluator{}).Call(ctx, req); err != nil {
					t.Fatalf("classic call with source TMPDIR: %v\n%s", err, stderr.String())
				}
				if stdout.String() != tc.want {
					t.Fatalf("stdout = %q, want %q", stdout.String(), tc.want)
				}
			}
			// The helper must still leave the source tree exactly as it found it.
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "go.mod" {
				t.Fatalf("classic helper leaked into source tree: %v %v", entries, err)
			}
		})
	}
}
