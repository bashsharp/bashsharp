//go:build full

package interp

// Sprint: #319; Story: #1086; Story-ID: 2405313cf7e2
//
// The whole-package lifetime proof of cmd/compile/internal/syntax.Parse
// certifies (TestS319CallbackProofSyntaxParseDepth) and the end-to-end gate
// passes (TestS281DependencySourceCallbackProof), yet an interpreted
// cmd/compile/internal/types2 still refused TestTypeSetString with
// "asynchronous or retained original function callbacks are unsupported for
// __gosource_import_0_64_0.Parse".
//
// The proof never ran. It resolves the dependency's sources by listing the
// import path in the interpreted program's own directory, and `go test
// cmd/compile/internal/types2` runs that program with its cwd INSIDE a second
// GOROOT tree while GOROOT names the toolchain module's copy. Both trees hold
// cmd/compile/internal/syntax, so the listing answered
//
//	ambiguous import: found package cmd/compile/internal/syntax in multiple
//	directories: <GOROOT>/src/... and <cwd's GOROOT>/src/...
//
// with no directory and no files -- and the loader returned false without
// recording anything, so the transport named the general callback rule for
// what was an unreadable package.
//
// Whether a directory can name an import path is a property of that directory,
// not of the dependency. The worker is built from its own scratch directory
// and is unaffected, so these reductions pin both halves: the retry in the
// neutral directory the worker resolves in, and the GOROOT authentication that
// keeps everything else refused.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// dependencyCallbackProofAmbiguousRoot builds the shape the interpreted types2
// run had underfoot: a directory inside a SECOND goroot tree whose `cmd`
// module also declares the import path under proof. Listing from here is
// ambiguous against the real GOROOT, exactly as it was in the sprint's
// reproducer, without depending on any particular machine's layout.
func dependencyCallbackProofAmbiguousRoot(t *testing.T, path string) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module std\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "cmd", "go.mod"), []byte("module cmd\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(src, filepath.FromSlash(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := path[strings.LastIndexByte(path, '/')+1:]
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dependencyCallbackProofRequest(t *testing.T, dir string) bashPPEvalRequest {
	t.Helper()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		t.Skipf("toolchain unavailable: %v", err)
	}
	env := setEnvString(os.Environ(), "GOROOT", runtime.GOROOT())
	env = setEnvString(env, "PWD", dir)
	return bashPPEvalRequest{Dir: dir, Go: goBinary, BuildGo: goBinary, Env: env, BuildEnv: env}
}

// A dependency the program's own directory cannot name is still readable, from
// the toolchain the worker is compiled against -- the one copy the worker can
// have linked, since a build from the program's directory would have failed on
// the very same ambiguity.
func TestS319CallbackProofSourcesResolveAroundAmbiguousProgramDir(t *testing.T) {
	const path = "cmd/compile/internal/syntax"
	dir := dependencyCallbackProofAmbiguousRoot(t, path)
	req := dependencyCallbackProofRequest(t, dir)

	// The premise: the program's directory genuinely cannot name the path.
	direct, err := bashPPGoListFacts(context.Background(), req, path)
	if err == nil && dependencyCallbackProofSourcesUsable(direct) {
		t.Skipf("program directory named %s after all (dir=%s); this reduction has stopped reducing", path, direct.Dir)
	}

	facts, err := dependencyCallbackProofSources(context.Background(), req, path)
	if err != nil {
		t.Fatalf("sources for %s: %v", path, err)
	}
	if !dependencyCallbackProofSourcesUsable(facts) {
		t.Fatalf("sources for %s are unreadable: %+v", path, facts)
	}
	if !dependencyCallbackProofWithin(facts.Dir, filepath.Join(runtime.GOROOT(), "src")) {
		t.Fatalf("sources for %s came from outside the build toolchain: %s", path, facts.Dir)
	}
	if dependencyCallbackProofWithin(facts.Dir, dir) {
		t.Fatalf("sources for %s came from the program's own tree: %s", path, facts.Dir)
	}
	if !slices.Contains(facts.GoFiles, "parser.go") {
		t.Fatalf("sources for %s do not look like the parser: %v", path, facts.GoFiles)
	}

	// And the whole proof now runs over them, as the end-to-end gate needs.
	if !loadDependencyFunctionCallbackLifetimeProof(context.Background(), req, path, "Parse", "", nil, []int{2}) {
		t.Fatalf("%s.Parse refused from an ambiguous program directory", path)
	}
}

// The retry is not a licence to read whatever the neutral directory finds. A
// path that names nothing there stays refused -- and says why, instead of the
// silence that made story #1086 read as a callback-rule refusal.
func TestS319CallbackProofSourcesRefuseUnreadable(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "no_such_package", path: "example.invalid/gosource/absent"},
		{name: "relative_path", path: "./gosource-absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := dependencyCallbackProofRequest(t, t.TempDir())
			facts, err := dependencyCallbackProofSources(context.Background(), req, test.path)
			if err == nil {
				t.Fatalf("admitted %s: %+v", test.path, facts)
			}
			if !strings.Contains(err.Error(), test.path) {
				t.Fatalf("refusal for %s does not name it: %v", test.path, err)
			}
			if loadDependencyFunctionCallbackLifetimeProof(context.Background(), req, test.path, "Parse", "", nil, []int{0}) {
				t.Fatalf("proof of %s.Parse admitted without sources", test.path)
			}
		})
	}
}

// A cgo package is listable and still unreadable: the proof parses Go source
// only, so admitting one would prove a body the compiler does not build.
func TestS319CallbackProofSourcesRefuseCgo(t *testing.T) {
	req := dependencyCallbackProofRequest(t, t.TempDir())
	facts, err := bashPPGoListFacts(context.Background(), req, "runtime/cgo")
	if err != nil || len(facts.CgoFiles) == 0 {
		t.Skipf("runtime/cgo is not a cgo package here: err=%v facts=%+v", err, facts)
	}
	if dependencyCallbackProofSourcesUsable(facts) {
		t.Fatalf("cgo package admitted as readable: %+v", facts)
	}
	if _, err := dependencyCallbackProofSources(context.Background(), req, "runtime/cgo"); err == nil {
		t.Fatal("runtime/cgo sources admitted")
	}
}

// Containment decides whether the retry landed in the toolchain the worker is
// built with, so a sibling that merely shares a prefix must not pass for one.
func TestS319CallbackProofWithin(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name string
		dir  string
		root string
		want bool
	}{
		{name: "same", dir: root, root: root, want: true},
		{name: "nested", dir: filepath.Join(root, "src", "cmd"), root: filepath.Join(root, "src"), want: true},
		{name: "trailing_separator", dir: filepath.Join(root, "src", "cmd"), root: filepath.Join(root, "src") + string(filepath.Separator), want: true},
		{name: "parent", dir: root, root: filepath.Join(root, "src"), want: false},
		{name: "shared_prefix_sibling", dir: root + "-other", root: root, want: false},
		{name: "empty_dir", dir: "", root: root, want: false},
		{name: "empty_root", dir: root, root: "", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := dependencyCallbackProofWithin(test.dir, test.root); got != test.want {
				t.Fatalf("within(%q, %q) = %v, want %v", test.dir, test.root, got, test.want)
			}
		})
	}
}
