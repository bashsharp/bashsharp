package polyglot

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRustContext() RustDependencyContext {
	hash := strings.Repeat("a", 64)
	return RustDependencyContext{Schema: 1, SourceSHA256: hash, RustcSHA256: hash, CargoSHA256: hash, RustcVersion: "rustc", CargoVersion: "cargo", Dependencies: map[string]RustDependency{"serde": {Version: "1.0.229", Features: []string{"derive"}, DefaultFeatures: true}, "serde_json": {Version: "1.0.151", DefaultFeatures: true}, "base64": {Version: "0.22.1", DefaultFeatures: true}}, BuildTools: map[string]RustBuildTool{"CC": {Path: "/bin/cc", SHA256: hash}, "CXX": {Path: "/bin/c++", SHA256: hash}, "AR": {Path: "/bin/ar", SHA256: hash}, "LINKER": {Path: "/bin/cc", SHA256: hash}}, Files: map[string]string{"Cargo.lock": hash, "vendor/crate/Cargo.toml": hash}}
}
func writeRustContext(t *testing.T, dir string, c RustDependencyContext) string {
	t.Helper()
	data, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "context.json")
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}
func TestRustContextAdmission(t *testing.T) {
	for name, change := range map[string]func(*RustDependencyContext){
		"unsupported-target": func(c *RustDependencyContext) { c.Target = "staticlib" },
		"schema":             func(c *RustDependencyContext) { c.Schema = 2 },
		"source":             func(c *RustDependencyContext) { c.SourceSHA256 = "" },
		"provider":           func(c *RustDependencyContext) { c.CargoSHA256 = "" },
		"unlocked":           func(c *RustDependencyContext) { c.Dependencies["rand"] = RustDependency{Version: "0.9"} },
		"injection": func(c *RustDependencyContext) {
			c.Dependencies["x\n[patch.crates-io]"] = RustDependency{Version: "1.0.0"}
		},
		"missing-bridge":           func(c *RustDependencyContext) { delete(c.Dependencies, "serde") },
		"traversal":                func(c *RustDependencyContext) { c.Files["vendor/../../escape"] = strings.Repeat("a", 64) },
		"absolute":                 func(c *RustDependencyContext) { c.Files["/vendor/file"] = strings.Repeat("a", 64) },
		"backslash":                func(c *RustDependencyContext) { c.Files[`vendor\file`] = strings.Repeat("a", 64) },
		"case-collision":           func(c *RustDependencyContext) { c.Files["vendor/CRATE/Cargo.toml"] = strings.Repeat("a", 64) },
		"file-directory-collision": func(c *RustDependencyContext) { c.Files["vendor/crate"] = strings.Repeat("a", 64) },
		"unbound-tools":            func(c *RustDependencyContext) { delete(c.BuildTools, "CC") },
		"reserved-directory":       func(c *RustDependencyContext) { c.Files["src/main.rs/child.rs"] = c.SourceSHA256 },
		"reserved-main":            func(c *RustDependencyContext) { c.Files["src/main.rs"] = c.SourceSHA256 },
		"reserved-lib":             func(c *RustDependencyContext) { c.Files["src/lib.rs"] = c.SourceSHA256 },
		"reserved-case":            func(c *RustDependencyContext) { c.Files["src/Main.rs"] = c.SourceSHA256 },
		"module-traversal":         func(c *RustDependencyContext) { c.Files["src/../escape.rs"] = c.SourceSHA256 },
		"module-collision": func(c *RustDependencyContext) {
			c.Files["src/a.rs"] = c.SourceSHA256
			c.Files["src/A.rs"] = c.SourceSHA256
		},
		"ambient-config": func(c *RustDependencyContext) { c.Files[".cargo/config.toml"] = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			c := testRustContext()
			change(&c)
			path := writeRustContext(t, t.TempDir(), c)
			if _, e := readRustContext(path, ""); e == nil {
				t.Fatal("admitted invalid context")
			}
		})
	}
	t.Run("duplicate-json", func(t *testing.T) {
		path := writeRustContext(t, t.TempDir(), testRustContext())
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		b = append([]byte(`{"schema":1,`), b[1:]...)
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := readRustContext(path, ""); e == nil {
			t.Fatal("accepted duplicate key")
		}
	})
	t.Run("oversized-json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "context.json")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.Truncate(rustContextLimit + 1); e != nil {
			t.Fatal(e)
		}
		f.Close()
		if _, e = readRustContext(path, ""); e == nil {
			t.Fatal("accepted oversized context")
		}
	})
	t.Run("oversized-file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "Cargo.lock")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		f.Truncate((32 << 20) + 1)
		f.Close()
		c := testRustContext()
		c.Files = map[string]string{"Cargo.lock": strings.Repeat("a", 64)}
		if e = c.copyClosure(dir, t.TempDir()); e == nil {
			t.Fatal("accepted oversized closure file")
		}
	})
	t.Run("immutable-discovery", func(t *testing.T) {
		dir := t.TempDir()
		c := testRustContext()
		c.Files = map[string]string{"Cargo.lock": rustSHA([]byte("locked")), "vendor/crate/Cargo.toml": rustSHA([]byte("manifest"))}
		for path, data := range map[string]string{"Cargo.lock": "locked", "vendor/crate/Cargo.toml": "manifest"} {
			if e := writeRustClosureFile(dir, path, []byte(data)); e != nil {
				t.Fatal(e)
			}
		}
		path := writeRustContext(t, dir, c)
		plan := EnvironmentPlan{SourceDir: dir}
		if e := discoverRustContext(&plan, map[string]string{"BASHPP_RUST_CONTEXT": "context.json"}); e != nil {
			t.Fatal(e)
		}
		r := Rust{Environment: &plan}
		p, d := r.dependencyContext()
		if p != path || !rustDigest(d) {
			t.Fatal(p, d)
		}
		if e := os.WriteFile(path, []byte("{}"), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := readRustContext(p, d); e == nil || !strings.Contains(e.Error(), "changed") {
			t.Fatal(e)
		}
	})
	t.Run("no-ambient-context", func(t *testing.T) {
		t.Setenv("BASHPP_RUST_CONTEXT", "missing.json")
		if p, _ := (Rust{}).dependencyContext(); p != "" {
			t.Fatal(p)
		}
	})
	t.Run("copy-hash", func(t *testing.T) {
		dir := t.TempDir()
		c := testRustContext()
		c.Files = map[string]string{"Cargo.lock": rustSHA([]byte("pinned"))}
		os.WriteFile(filepath.Join(dir, "Cargo.lock"), []byte("changed"), 0600)
		if e := c.copyClosure(dir, t.TempDir()); e == nil {
			t.Fatal("accepted changed lock")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "lock")
		os.WriteFile(target, []byte("pinned"), 0600)
		if e := os.Symlink(target, filepath.Join(dir, "Cargo.lock")); e != nil {
			t.Fatal(e)
		}
		c := testRustContext()
		c.Files = map[string]string{"Cargo.lock": rustSHA([]byte("pinned"))}
		if e := c.copyClosure(dir, t.TempDir()); e == nil {
			t.Fatal("accepted symlink")
		}
	})
}

func TestRustContextDiagnosticClasses(t *testing.T) {
	for _, tc := range []struct{ code, target, want string }{{"E0308", "/src/main.rs", "RUST-ESOURCE"}, {"E0432", "/src/main.rs", "RUST-EDEPENDENCY"}, {"E0463", "/src/main.rs", "RUST-EDEPENDENCY"}, {"E0583", "/src/main.rs", "RUST-EDEPENDENCY"}, {"E0308", "/vendor/a/lib.rs", "RUST-EDEPENDENCY"}} {
		msg := map[string]any{"reason": "compiler-message", "target": map[string]string{"src_path": tc.target}, "message": map[string]any{"level": "error", "code": map[string]string{"code": tc.code}, "spans": []map[string]any{{"file_name": "src/main.rs", "byte_end": 10, "is_primary": true}}}}
		b, _ := json.Marshal(msg)
		if got := rustBuildFailureClass(b, "/src/main.rs", 100); got != tc.want {
			t.Errorf("%s: %s", tc.code, got)
		}
	}
	for name, msg := range map[string]string{
		"zero-span": `{"reason":"compiler-message","target":{"src_path":"/src/main.rs"},"message":{"level":"error","code":{"code":"E0308"},"spans":[{"file_name":"src/main.rs","byte_end":0,"is_primary":true}]}}`,
		"linker":    `{"reason":"compiler-message","target":{"src_path":"/src/main.rs"},"message":{"level":"error","code":null}}`,
		"generated": `{"reason":"compiler-message","target":{"src_path":"/src/main.rs"},"message":{"level":"error","code":{"code":"E0277"},"spans":[{"file_name":"src/main.rs","byte_end":200,"is_primary":true}]}}`,
	} {
		if got := rustBuildFailureClass([]byte(msg), "/src/main.rs", 100); got != "RUST-EBUILD" {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := rustBuildFailureClass([]byte("linker unavailable"), "/src/main.rs", 100); got != "RUST-EBUILD" {
		t.Fatal(got)
	}
}

// Provisioning is test-only and offline. Product preparation must consume the
// resulting locked inventory without generating or changing its resolution.
func provisionRustContext(t *testing.T, source string) (Rust, string, RustDependencyContext) {
	return provisionRustContextTarget(t, source, "", nil)
}

func provisionRustContextTarget(t *testing.T, source, target string, extra map[string]RustDependency) (Rust, string, RustDependencyContext) {
	t.Helper()
	if os.Getenv("BASHPP_RUST_CONTEXT_GATE") != "1" {
		t.Skip("explicit offline Rust context gate")
	}
	rustc, e := exec.LookPath("rustc")
	if e != nil {
		t.Fatal(e)
	}
	r := Rust{Command: rustc}
	cargo, e := r.cargo()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	c := testRustContext()
	c.Target = target
	c.Dependencies["rand"] = RustDependency{Version: "0.9.3", DefaultFeatures: true}
	for name, dep := range extra {
		c.Dependencies[name] = dep
	}
	for name, command := range map[string]string{"CC": "cc", "CXX": "c++", "AR": "ar", "LINKER": "cc"} {
		path, e := exec.LookPath(command)
		if e != nil {
			t.Fatal(e)
		}
		hash, e := rustProviderHash(path)
		if e != nil {
			t.Fatal(e)
		}
		c.BuildTools[name] = RustBuildTool{Path: path, SHA256: hash}
	}
	c.Dependencies["answer"] = RustDependency{Version: "0.1.0", Path: "packages/answer", DefaultFeatures: true}
	for path, data := range map[string]string{"packages/answer/Cargo.toml": "[package]\nname=\"answer\"\nversion=\"0.1.0\"\nedition=\"2024\"\n[workspace]\n", "packages/answer/src/lib.rs": "pub fn answer() -> i64 { 42 }\n"} {
		if e := writeRustClosureFile(dir, path, []byte(data)); e != nil {
			t.Fatal(e)
		}
	}
	c.SourceSHA256 = rustSHA([]byte(source))
	c.RustcSHA256, e = rustProviderHash(rustc)
	if e != nil {
		t.Fatal(e)
	}
	c.CargoSHA256, e = rustProviderHash(cargo[0])
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []struct {
		exe, arg string
		dest     *string
	}{{rustc, "-vV", &c.RustcVersion}, {cargo[0], "-V", &c.CargoVersion}} {
		b, e := exec.Command(p.exe, p.arg).CombinedOutput()
		if e != nil {
			t.Fatalf("provider: %v %s", e, b)
		}
		*p.dest = strings.TrimSpace(string(b))
	}
	os.Mkdir(filepath.Join(dir, "src"), 0700)
	manifest := c.manifest()
	if target == "proc-macro" {
		os.WriteFile(filepath.Join(dir, "src/lib.rs"), []byte("extern crate proc_macro;\n"), 0600)
		manifest += "\n[lib]\nproc-macro = true\npath = \"src/lib.rs\"\n"
	} else {
		os.WriteFile(filepath.Join(dir, "src/main.rs"), []byte("fn main() {}"), 0600)
	}
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(manifest), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cargo[0], "vendor", "--offline", "vendor")
	cmd.Dir = dir
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("offline test provisioning requires pinned cached crates: %v\n%s", e, b)
	}
	c.Files = map[string]string{}
	e = filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if rel != "Cargo.lock" && !strings.HasPrefix(rel, "vendor/") && !strings.HasPrefix(rel, "packages/") {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		c.Files[rel] = rustSHA(b)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	r.ContextFile = writeRustContext(t, dir, c)
	b, _ := os.ReadFile(r.ContextFile)
	r.ContextSHA256 = rustSHA(b)
	return r, dir, c
}

func TestRustProcMacroTarget(t *testing.T) {
	const valid = `use proc_macro::TokenStream;

#[proc_macro_derive(Example)]
pub fn derive_example(_input: TokenStream) -> TokenStream {
    TokenStream::new()
}


`
	r, dir, c := provisionRustContextTarget(t, valid, "proc-macro", nil)
	selected, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(dir, "program.bpp"), Language: "rust", Environ: append(os.Environ(), "BASHPP_RUST_CONTEXT="+r.ContextFile)})
	if err != nil || selected.RustTarget != "proc-macro" || selected.Clone().RustTarget != "proc-macro" || selected.Fingerprint == "" {
		t.Fatalf("discovered proc-macro target = %+v, %v", selected, err)
	}
	exports, artifact, err := r.AnalyzeArtifact(context.Background(), valid)
	if err != nil || len(exports) != 0 || artifact != "" {
		t.Fatalf("valid proc-macro: exports=%v artifact bytes=%d err=%v", exports, len(artifact), err)
	}
	r.Environment = &selected
	plans, err := Prepare(context.Background(), []Block{{Language: "rust", Source: valid}}, map[string]Analyzer{"rust": r})
	if err != nil || len(plans) != 1 || plans[0].RustTarget != c.Target || len(plans[0].Exports) != 0 || plans[0].Artifact != "" {
		t.Fatalf("proc-macro plan = %+v, %v", plans, err)
	}
	if got, err := r.target(); err != nil || got != "proc-macro" {
		t.Fatalf("target = %q, %v", got, err)
	}
	module := Start(plans[0], r)
	if _, err := module.Call(context.Background(), "derive_example"); err == nil || !strings.Contains(err.Error(), "no callable exports") || module.alive() {
		t.Fatalf("library call started worker or lacked clear rejection: %v", err)
	}

	const original = `use proc_macro::TokenStream;
use quote::quote;

#[proc_macro_derive(HelloMacro)]
pub fn hello_macro_derive(input: TokenStream) -> TokenStream {
    // Construct a representation of Rust code as a syntax tree
    // that we can manipulate.
    let ast = syn::parse(input).unwrap();

    // Build the trait implementation.
    impl_hello_macro(&ast)
}
`
	if got := rustSHA([]byte(original)); got != "fa9582baa1dd17eef3b7d76804ceabfa50453d77d9c7137f1f452cb43280319e" {
		t.Fatalf("upstream L299 source changed: %s", got)
	}
	extra := map[string]RustDependency{
		"quote": {Version: "1.0.47", DefaultFeatures: true},
		"syn":   {Version: "2.0.119", DefaultFeatures: true},
	}
	r, _, _ = provisionRustContextTarget(t, original, "proc-macro", extra)
	_, artifact, err = r.AnalyzeArtifact(context.Background(), original)
	if err == nil || !strings.Contains(err.Error(), "RUST-ESOURCE") || !strings.Contains(err.Error(), "E0425") || !strings.Contains(err.Error(), "cannot find function `impl_hello_macro` in this scope") || !strings.Contains(err.Error(), "src/lib.rs:11:5") || !strings.Contains(err.Error(), `"reason":"compiler-message"`) || !strings.Contains(err.Error(), `"reason":"build-finished","success":false`) || artifact != "" {
		t.Fatalf("original L299 diagnostic = %v, artifact bytes=%d", err, len(artifact))
	}
	r, _, _ = provisionRustContextTarget(t, original, "proc-macro", nil)
	_, _, err = r.AnalyzeArtifact(context.Background(), original)
	if err == nil || !strings.Contains(err.Error(), "RUST-EDEPENDENCY") || strings.Contains(err.Error(), "RUST-ESOURCE") {
		t.Fatalf("missing quote/syn closure accepted as source diagnostic: %v", err)
	}
}

// This gate consumes the tests46 source and vendor receipt, rather than the
// developer's Cargo cache. The parent runs it with the pinned Linux provider.
func TestRustProcMacroPinnedReceipt(t *testing.T) {
	fixture := os.Getenv("BASHPP_RUST_PINNED_FIXTURE")
	if fixture == "" {
		t.Skip("parent-only pinned Rust receipt gate")
	}
	const sourceHash = "fa9582baa1dd17eef3b7d76804ceabfa50453d77d9c7137f1f452cb43280319e"
	const manifestHash = "76609169c9bcf0a747f1d3ceb82a814643b86cb20e97906f2d366d74914e2def"
	const lockHash = "b3198cf31d3c7b3619ffe15428db498cde8120bc067c06ff05b7eaf203833d26"
	const rustcHash = "4a84e05991ad6f2a84c1361c29b52b38c390365bd1fc269b1936c88d482a8928"
	const cargoHash = "a73b2c25573d251489101c0d8f19ad3702eb9761166de5ed8437b472b6c038ce"
	var inventory struct {
		Files map[string]string `json:"files"`
	}
	preparation, err := os.ReadFile(filepath.Join(fixture, "preparation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(preparation, &inventory); err != nil {
		t.Fatalf("pinned preparation inventory: %v", err)
	}
	caseDir := filepath.Join(fixture, "book-ch20-05-macros-L299")
	readPinned := func(path, want string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || rustSHA(data) != want {
			t.Fatalf("pinned file changed: %s: %v", path, err)
		}
		return data
	}
	source := readPinned(filepath.Join(caseDir, "source.rs"), sourceHash)
	readPinned(filepath.Join(caseDir, "Cargo.toml"), manifestHash)
	lock := readPinned(filepath.Join(caseDir, "Cargo.lock"), lockHash)
	rustc := os.Getenv("BASHPP_RUST_PINNED_RUSTC")
	if rustc == "" {
		t.Fatal("pinned rustc path required")
	}
	r := Rust{Command: rustc}
	cargo, err := r.cargo()
	if err != nil || len(cargo) != 1 {
		t.Fatalf("pinned cargo: %v", err)
	}
	for _, provider := range []struct{ path, hash string }{{rustc, rustcHash}, {cargo[0], cargoHash}} {
		got, err := rustProviderHash(provider.path)
		if err != nil || got != provider.hash {
			t.Fatalf("pinned provider mismatch: %s: %v", provider.path, err)
		}
	}
	dir := t.TempDir()
	c := testRustContext()
	c.Target = "proc-macro"
	c.SourceSHA256 = sourceHash
	c.Dependencies = map[string]RustDependency{
		"quote": {Version: "1.0.36", DefaultFeatures: true},
		"syn":   {Version: "2.0.59", DefaultFeatures: true},
	}
	c.Files = map[string]string{}
	for path, want := range inventory.Files {
		if !strings.HasPrefix(path, "vendor/") {
			continue
		}
		data := readPinned(filepath.Join(fixture, filepath.FromSlash(path)), want)
		if err := writeRustClosureFile(dir, path, data); err != nil {
			t.Fatal(err)
		}
		c.Files[path] = want
	}
	if len(c.Files) == 0 {
		t.Fatal("empty pinned vendor inventory")
	}
	// The generated package has a different name/version from the upstream
	// crate; only that root lock entry changes. All dependency checksums stay.
	adapted := bytes.Replace(lock, []byte("name = \"hello_macro_derive\"\nversion = \"0.1.0\""), []byte("name = \"bashpp_module\"\nversion = \"0.0.0\""), 1)
	if bytes.Equal(lock, adapted) {
		t.Fatal("upstream lock root changed")
	}
	if err := writeRustClosureFile(dir, "Cargo.lock", adapted); err != nil {
		t.Fatal(err)
	}
	c.Files["Cargo.lock"] = rustSHA(adapted)
	for name, command := range map[string]string{"CC": "cc", "CXX": "c++", "AR": "ar", "LINKER": "cc"} {
		path, err := exec.LookPath(command)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := rustProviderHash(path)
		if err != nil {
			t.Fatal(err)
		}
		c.BuildTools[name] = RustBuildTool{Path: path, SHA256: hash}
	}
	c.RustcSHA256, c.CargoSHA256 = rustcHash, cargoHash
	for _, p := range []struct {
		path, arg string
		version   *string
	}{{rustc, "-vV", &c.RustcVersion}, {cargo[0], "-V", &c.CargoVersion}} {
		out, err := exec.Command(p.path, p.arg).CombinedOutput()
		if err != nil {
			t.Fatalf("pinned provider version: %v", err)
		}
		*p.version = strings.TrimSpace(string(out))
	}
	check := func(body []byte, wantError bool) {
		t.Helper()
		c.SourceSHA256 = rustSHA(body)
		r.ContextFile = writeRustContext(t, dir, c)
		data, err := os.ReadFile(r.ContextFile)
		if err != nil {
			t.Fatal(err)
		}
		r.ContextSHA256 = rustSHA(data)
		exports, artifact, err := r.AnalyzeArtifact(context.Background(), string(body))
		if len(exports) != 0 || artifact != "" {
			t.Fatalf("proc-macro leaked worker exports/artifact: %v %d", exports, len(artifact))
		}
		if wantError {
			if err == nil || !strings.Contains(err.Error(), "RUST-ESOURCE") || !strings.Contains(err.Error(), "E0425") || !strings.Contains(err.Error(), "impl_hello_macro") || !strings.Contains(err.Error(), "src/lib.rs:11:5") || !strings.Contains(err.Error(), `"reason":"compiler-message"`) || !strings.Contains(err.Error(), `"reason":"build-finished","success":false`) {
				t.Fatalf("pinned L299 diagnostic: %v", err)
			}
		} else if err != nil {
			t.Fatalf("minimal proc-macro control: %v", err)
		}
	}
	check(source, true)
	check([]byte("use proc_macro::TokenStream;\n#[proc_macro_derive(Example)]\npub fn derive_example(_: TokenStream) -> TokenStream { TokenStream::new() }\n"), false)
}

func TestRustLockedContextArtifact(t *testing.T) {
	source := "pub fn sample() -> i64 { use rand::Rng; rand::rng().random_range(answer::answer()..43) }\n"
	r, dir, c := provisionRustContext(t, source)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Setenv("RUSTFLAGS", "--invalid-ambient-rust-flags")
	t.Setenv("CARGO_ENCODED_RUSTFLAGS", "--invalid-ambient-flags")
	t.Setenv("CC", "/missing/ambient-cc")
	t.Run("wrong-source", func(t *testing.T) {
		if _, _, e := r.AnalyzeArtifact(ctx, source+"\n"); e == nil || !strings.Contains(e.Error(), "RUST-ECONTEXT") {
			t.Fatal(e)
		}
	})
	t.Run("provider", func(t *testing.T) {
		bad := c
		bad.RustcSHA256 = strings.Repeat("0", 64)
		p := writeRustContext(t, dir, bad)
		defer writeRustContext(t, dir, c)
		rr := r
		rr.ContextFile = p
		rr.ContextSHA256 = ""
		if _, _, e := rr.AnalyzeArtifact(ctx, source); e == nil || !strings.Contains(e.Error(), "RUST-EPROVIDER") {
			t.Fatal(e)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		dead, stop := context.WithCancel(ctx)
		stop()
		if _, _, e := r.AnalyzeArtifact(dead, source); e == nil {
			t.Fatal("ignored cancellation")
		}
	})
	env, e := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(dir, "input.bsh"), Language: "rust", Environ: append(os.Environ(), "BASHPP_RUSTC="+r.Command, "BASHPP_RUST_CONTEXT="+r.ContextFile)})
	if e != nil {
		t.Fatal(e)
	}
	env.Dir = t.TempDir()
	r.Environment = &env
	if e = os.RemoveAll(dir); e != nil {
		t.Fatal(e)
	}
	plans, e := Prepare(ctx, []Block{{Language: "rust", Source: source}}, map[string]Analyzer{"rust": r})
	if e != nil {
		t.Fatal(e)
	}
	if plans[0].Source != source || len(plans[0].Artifact) == 0 {
		t.Fatal("lost source or executable")
	}
	// The same artifact transport used by lowering must run with no source,
	// context, vendored crates, registry or compiler available.
	var transport bytes.Buffer
	if e := gob.NewEncoder(&transport).Encode(plans[0]); e != nil {
		t.Fatal(e)
	}
	var plan Plan
	if e := gob.NewDecoder(&transport).Decode(&plan); e != nil {
		t.Fatal(e)
	}
	if e = os.RemoveAll(dir); e != nil {
		t.Fatal(e)
	}
	module := Start(plan, Rust{Command: "/missing/rustc", ContextFile: "/missing/context"})
	defer module.Close()
	result, e := module.Call(ctx, "sample")
	if e != nil || result.Value != int64(42) {
		t.Fatalf("artifact call: %#v %v", result, e)
	}
}

func TestRustLockedContextSourceAndDependencyFailures(t *testing.T) {
	source := "pub fn sample() -> i64 { let _: bool = 42; 0 }\n"
	r, dir, c := provisionRustContext(t, source)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, _, e := r.AnalyzeArtifact(ctx, source); e == nil || !strings.Contains(e.Error(), "RUST-ESOURCE:") || !strings.Contains(e.Error(), "E0308") {
		t.Fatalf("source diagnostic: %v", e)
	}
	// A different exact version cannot cause Cargo to update the supplied lock.
	original := c.Dependencies["rand"]
	changed := original
	changed.Version = "0.9.4"
	c.Dependencies["rand"] = changed
	r.ContextFile = writeRustContext(t, dir, c)
	r.ContextSHA256 = ""
	if _, _, e := r.AnalyzeArtifact(ctx, source); e == nil || !strings.Contains(e.Error(), "RUST-EDEPENDENCY:") {
		t.Fatalf("stale lock: %v", e)
	}
	c.Dependencies["rand"] = original
	// Remove a required package from the declared closure, not merely the disk.
	for name := range c.Files {
		if strings.HasPrefix(name, "vendor/rand/") {
			delete(c.Files, name)
		}
	}
	r.ContextFile = writeRustContext(t, dir, c)
	r.ContextSHA256 = ""
	if _, _, e := r.AnalyzeArtifact(ctx, source); e == nil || !strings.Contains(e.Error(), "RUST-EDEPENDENCY:") || strings.Contains(e.Error(), "RUST-ESOURCE:") {
		t.Fatalf("dependency failure: %v", e)
	}
}

func TestRustLockedContextPrewarm(t *testing.T) {
	source := "pub fn sample() -> i64 { answer::answer() }\n"
	r, dir, c := provisionRustContext(t, source)
	c.CacheDir = t.TempDir()
	r.ContextFile = writeRustContext(t, dir, c)
	r.ContextSHA256 = ""
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := r.PrewarmDependencies(ctx, source); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotRustContext(c, r.ContextFile)
	if err != nil {
		t.Fatal(err)
	}
	closure, target, err := snapshot.materializeCache(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := filepath.Glob(filepath.Join(target, "debug", "deps", "libserde-*.rlib"))
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("prewarm artifacts: %v %v", dependencies, err)
	}
	before, err := os.Stat(dependencies[0])
	if err != nil {
		t.Fatal(err)
	}
	// Cargo shares dependency outputs but each source build owns its binary.
	results := make(chan error, 2)
	for range 2 {
		go func() { _, _, err := r.AnalyzeArtifact(ctx, source); results <- err }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(dependencies[0])
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("dependency recompiled after prewarm")
	}
	// Cache identity excludes only source and location, never provider or closure.
	altered := *snapshot
	altered.context = snapshot.context
	altered.context.SourceSHA256 = strings.Repeat("b", 64)
	same, _, err := altered.materializeCache(ctx)
	if err != nil || same != closure {
		t.Fatalf("source changed dependency cache: %s %v", same, err)
	}
	altered.context.RustcVersion += "changed"
	different, _, err := altered.materializeCache(ctx)
	if err != nil || different == closure {
		t.Fatalf("provider identity reused cache: %s %v", different, err)
	}
	if err = os.WriteFile(filepath.Join(closure, "Cargo.lock"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.AnalyzeArtifact(ctx, source); err == nil || !strings.Contains(err.Error(), "cached closure changed") {
		t.Fatalf("cache mutation: %v", err)
	}
}

func TestRustLockedContextRootModules(t *testing.T) {
	source := "mod front_of_house;\npub fn sample() -> i64 { front_of_house::answer() }\n"
	r, dir, c := provisionRustContext(t, source)
	module := "pub fn answer() -> i64 { 42 }\n"
	path := "src/front_of_house.rs"
	if err := writeRustClosureFile(dir, path, []byte(module)); err != nil {
		t.Fatal(err)
	}
	c.Files[path] = rustSHA([]byte(module))
	// Ambient modules must never be imported into the worker.
	if err := writeRustClosureFile(dir, "src/ambient.rs", []byte("compile_error!(\"ambient\");")); err != nil {
		t.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			if cached {
				c.CacheDir = t.TempDir()
			}
			r.ContextFile = writeRustContext(t, dir, c)
			r.ContextSHA256 = ""
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			funcs, artifact, err := r.AnalyzeArtifact(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			plan := Plan{Language: "rust", Source: source, Exports: funcs, Artifact: artifact}
			worker := Start(plan, Rust{Command: "/missing/rustc"})
			defer worker.Close()
			result, err := worker.Call(ctx, "sample")
			if err != nil || result.Value != int64(42) {
				t.Fatalf("module: %#v %v", result, err)
			}
		})
	}
}

func TestRustContextRootModuleSnapshot(t *testing.T) {
	dir := t.TempDir()
	c := testRustContext()
	files := map[string]string{"Cargo.lock": "locked", "vendor/crate/Cargo.toml": "manifest", "src/front_of_house.rs": "pub fn answer() {}"}
	c.Files = map[string]string{}
	for path, data := range files {
		c.Files[path] = rustSHA([]byte(data))
		if err := writeRustClosureFile(dir, path, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	path := writeRustContext(t, dir, c)
	admitted, err := readRustContext(path, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotRustContext(admitted, path)
	if err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(dir, "src/front_of_house.rs")
	if err := os.WriteFile(module, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if snapshot.files["src/front_of_house.rs"] != files["src/front_of_house.rs"] {
		t.Fatal("snapshot changed")
	}
	if _, err := snapshotRustContext(admitted, path); err == nil {
		t.Fatal("admitted changed module")
	}
	if err := os.Remove(module); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "module.rs")
	if err := os.WriteFile(target, []byte(files["src/front_of_house.rs"]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, module); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotRustContext(admitted, path); err == nil {
		t.Fatal("admitted symlink module")
	}
	snapshot.context.CacheDir = t.TempDir()
	ctx := context.Background()
	results := make(chan error, 4)
	for range 4 {
		go func() { _, _, err := snapshot.materializeCache(ctx); results <- err }()
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := snapshot.materializeCache(dead); err == nil {
		t.Fatal("ignored cancelled cache validation")
	}
}
