//go:build full

package lower_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRustLockedContextCompiledWithoutAssets(t *testing.T) {
	if os.Getenv("BASHPP_RUST_CONTEXT_GATE") != "1" {
		t.Skip("explicit offline Rust context gate")
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	read := func(path string) []byte {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	dir := t.TempDir()
	assets := filepath.Join(dir, "closure")
	if e := os.MkdirAll(filepath.Join(assets, "src"), 0700); e != nil {
		t.Fatal(e)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	rustc, e := exec.LookPath("rustc")
	if e != nil {
		t.Fatal(e)
	}
	cargo, e := exec.LookPath("cargo")
	if e != nil {
		t.Fatal(e)
	}
	body := "pub fn sample() -> i64 { use rand::Rng; rand::rng().random_range(42..43) }\n"
	source := "~~~rust as rs\n" + body + "~~~\nx := rs.sample()\necho $x\n"
	manifest := `[package]
name = "bashpp_module"
version = "0.0.0"
edition = "2024"
[workspace]
[dependencies]
serde = {version = "=1.0.229", features = ["derive"]}
serde_json = "=1.0.151"
base64 = "=0.22.1"
rand = "=0.9.3"
`
	write(filepath.Join(assets, "Cargo.toml"), []byte(manifest))
	write(filepath.Join(assets, "src/main.rs"), []byte("fn main() {}\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cargo, "vendor", "--offline", "vendor")
	cmd.Dir = assets
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("offline fixture provision: %v\n%s", e, b)
	}
	c := polyglot.RustDependencyContext{Schema: 1, SourceSHA256: hash([]byte(body)), RustcSHA256: hash(read(rustc)), CargoSHA256: hash(read(cargo)), Dependencies: map[string]polyglot.RustDependency{}, Files: map[string]string{}, BuildTools: map[string]polyglot.RustBuildTool{}}
	for name, version := range map[string]string{"serde": "1.0.229", "serde_json": "1.0.151", "base64": "0.22.1", "rand": "0.9.3"} {
		d := polyglot.RustDependency{Version: version, DefaultFeatures: true}
		if name == "serde" {
			d.Features = []string{"derive"}
		}
		c.Dependencies[name] = d
	}
	for _, p := range []struct {
		path, arg string
		dest      *string
	}{{rustc, "-vV", &c.RustcVersion}, {cargo, "-V", &c.CargoVersion}} {
		b, e := exec.CommandContext(ctx, p.path, p.arg).CombinedOutput()
		if e != nil {
			t.Fatal(e)
		}
		*p.dest = strings.TrimSpace(string(b))
	}
	for name, tool := range map[string]string{"CC": "cc", "CXX": "c++", "AR": "ar", "LINKER": "cc"} {
		p, e := exec.LookPath(tool)
		if e != nil {
			t.Fatal(e)
		}
		c.BuildTools[name] = polyglot.RustBuildTool{Path: p, SHA256: hash(read(p))}
	}
	e = filepath.WalkDir(assets, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(assets, path)
		rel = filepath.ToSlash(rel)
		if rel == "Cargo.lock" || strings.HasPrefix(rel, "vendor/") {
			c.Files[rel] = hash(read(path))
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	data, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	write(filepath.Join(assets, "context.json"), data)
	// Select the durable overlay, with no process-global context override.
	t.Setenv("BASHPP_RUST_CONTEXT", "")
	t.Setenv("BASHPP_RUSTC", rustc)
	overlay, e := json.Marshal(map[string]string{"language": "rust", "runtime": rustc, "rust_context": "closure/context.json"})
	if e != nil {
		t.Fatal(e)
	}
	write(filepath.Join(dir, "bashpp.json"), overlay)
	filename := filepath.Join(dir, "input.bsh")
	write(filename, []byte(source))
	file := parse(t, source, filename)
	var interpreted, stderr bytes.Buffer
	runner, e := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &interpreted, &stderr))
	if e != nil {
		t.Fatal(e)
	}
	if e = runner.Run(ctx, file); e != nil || interpreted.String() != "42\n" {
		t.Fatalf("interpreted: %v %q %s", e, interpreted.String(), stderr.String())
	}
	result, e := lower.Compile(file, lower.Options{})
	if e != nil {
		t.Fatal(e)
	}
	// The closure snapshot is preparation-only, not a generated Go asset.
	if bytes.Contains(result.Source, []byte(".cargo-checksum.json")) {
		t.Fatal("vendor assets leaked into generated source")
	}
	buildDir := t.TempDir()
	write(filepath.Join(buildDir, "main.go"), result.Source)
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(this)))
	write(filepath.Join(buildDir, "go.mod"), []byte(fmt.Sprintf("module rustcontextfixture\n\ngo %s\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => %s\n", strings.TrimPrefix(runtime.Version(), "go"), root)))
	binary := filepath.Join(buildDir, "program")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd = exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-o", binary, ".")
	cmd.Dir = buildDir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("compiled: %v\n%s", e, b)
	}
	if e = os.RemoveAll(assets); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filename); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(dir, "bashpp.json")); e != nil {
		t.Fatal(e)
	}
	runCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	cmd = exec.CommandContext(runCtx, binary)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir(), "CARGO_HOME="+t.TempDir(), "BASHPP_RUSTC=/missing/rustc", "BASHPP_RUST_CONTEXT=/missing/context")
	if b, e := cmd.CombinedOutput(); e != nil || string(b) != "42\n" {
		t.Fatalf("without source/compiler/assets: %v %q", e, b)
	}
}
