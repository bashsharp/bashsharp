package polyglot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// RustDependencyContext is an explicit, content-addressed crates.io closure.
// Paths in Files are relative to the JSON file, never searched in the project.
// SourceSHA256 identifies the exact aggregated fence bytes, before wrapping.
type RustDependencyContext struct {
	DiagnosticCapture *RustDiagnosticCapture    `json:"diagnostic_capture,omitempty"`
	CacheDir          string                    `json:"cache_dir,omitempty"`
	Target            string                    `json:"target,omitempty"` // binary (default) or proc-macro
	Schema            int                       `json:"schema"`
	SourceSHA256      string                    `json:"source_sha256"`
	RustcSHA256       string                    `json:"rustc_sha256"`
	CargoSHA256       string                    `json:"cargo_sha256"`
	RustcVersion      string                    `json:"rustc_version"`
	CargoVersion      string                    `json:"cargo_version"`
	Dependencies      map[string]RustDependency `json:"dependencies"`
	Files             map[string]string         `json:"files"`
	BuildTools        map[string]RustBuildTool  `json:"build_tools"`
	ProviderFiles     map[string]string         `json:"provider_files,omitempty"`
}

// RustBuildTool pins a native compiler, archiver or linker, including wrappers.
// ProviderFiles additionally binds the SDK binaries those wrappers invoke.
type RustBuildTool struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type RustDependency struct {
	Path            string   `json:"path,omitempty"`
	Version         string   `json:"version"`
	Features        []string `json:"features,omitempty"`
	DefaultFeatures bool     `json:"default_features"`
}

const rustContextLimit = 8 << 20
const rustClosureLimit = 512 << 20

var rustCrateName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
var rustExactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$`)
var rustFeature = regexp.MustCompile(`^[A-Za-z0-9_+/-]+$`)

func rustSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func rustDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

func rustReadBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("not a bounded regular file: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(data) > int(limit) {
		return nil, fmt.Errorf("file exceeds limit: %s", path)
	}
	return data, err
}

func readRustContext(path, digest string) (RustDependencyContext, error) {
	var c RustDependencyContext
	data, err := rustReadBounded(path, rustContextLimit)
	if err != nil {
		return c, err
	}
	if digest != "" && rustSHA(data) != digest {
		return c, fmt.Errorf("context changed after discovery")
	}
	if err := rustUniqueJSON(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("trailing context data")
	}
	if c.Schema != 1 || !rustDigest(c.SourceSHA256) || !rustDigest(c.RustcSHA256) || !rustDigest(c.CargoSHA256) || c.RustcVersion == "" || c.CargoVersion == "" {
		return c, fmt.Errorf("schema, source and provider identities required")
	}
	if c.Target != "" && c.Target != "binary" && c.Target != "proc-macro" {
		return c, fmt.Errorf("unsupported Rust target %q", c.Target)
	}
	if c.CacheDir != "" && !filepath.IsAbs(c.CacheDir) {
		return c, fmt.Errorf("cache_dir must be absolute")
	}
	if c.DiagnosticCapture != nil && (c.DiagnosticCapture.Schema != 1 || !c.DiagnosticCapture.LongType || c.CacheDir == "") {
		return c, fmt.Errorf("diagnostic_capture requires schema 1, long_type true and cache_dir")
	}
	if len(c.BuildTools) != 4 {
		return c, fmt.Errorf("CC, CXX, AR and LINKER provider identities required")
	}
	for _, name := range []string{"CC", "CXX", "AR", "LINKER"} {
		tool := c.BuildTools[name]
		if !filepath.IsAbs(tool.Path) || !rustDigest(tool.SHA256) {
			return c, fmt.Errorf("invalid build tool %s", name)
		}
	}
	if len(c.ProviderFiles) > 40000 {
		return c, fmt.Errorf("too many provider files")
	}
	for path, hash := range c.ProviderFiles {
		if !filepath.IsAbs(path) || !rustDigest(hash) {
			return c, fmt.Errorf("invalid provider file")
		}
	}

	if c.Target != "proc-macro" {
		if len(c.Dependencies) < 3 || len(c.Dependencies) > 64 {
			return c, fmt.Errorf("expected 3..64 direct dependencies")
		}
		for _, name := range []string{"serde", "serde_json", "base64"} {
			if _, ok := c.Dependencies[name]; !ok {
				return c, fmt.Errorf("missing bridge dependency %s", name)
			}
		}
	} else if len(c.Dependencies) > 64 {
		return c, fmt.Errorf("too many direct dependencies")
	}
	for name, dep := range c.Dependencies {
		if dep.Path != "" && dep.Path != "packages/"+name {
			return c, fmt.Errorf("path dependency must be packages/<crate>: %s", name)
		}
		if dep.Path != "" && !rustDigest(c.Files[dep.Path+"/Cargo.toml"]) {
			return c, fmt.Errorf("path dependency manifest not inventoried: %s", name)
		}
		if !rustCrateName.MatchString(name) || !rustExactVersion.MatchString(dep.Version) || len(dep.Features) > 64 {
			return c, fmt.Errorf("invalid exact dependency %q", name)
		}
		for _, f := range dep.Features {
			if !rustFeature.MatchString(f) {
				return c, fmt.Errorf("invalid feature %q", f)
			}
		}
	}
	if len(c.Files) < 1 || len(c.Files) > 40000 || !rustDigest(c.Files["Cargo.lock"]) {
		return c, fmt.Errorf("bounded lock and vendor inventory required")
	}
	seen := map[string]bool{}
	for path, digest := range c.Files {
		if !rustDigest(digest) || strings.ContainsAny(path, "\\:\x00") || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "/") || (path != "Cargo.lock" && !strings.HasPrefix(path, "vendor/") && !rustPackageFile(c, path) && !rustRootModule(path)) {
			return c, fmt.Errorf("invalid closure path or digest %q", path)
		}
		key := strings.ToLower(path)
		if seen[key] {
			return c, fmt.Errorf("closure path collision: %s", path)
		}
		seen[key] = true
	}
	for key := range seen {
		for parent := filepath.ToSlash(filepath.Dir(key)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if seen[parent] {
				return c, fmt.Errorf("closure file/directory collision")
			}
		}
	}
	return c, nil
}

// discoverRustContext captures an immutable preparation snapshot and binds its
// inventory digest into the environment identity. Lowering drops the snapshot.
func discoverRustContext(plan *EnvironmentPlan, env map[string]string) error {
	path := env["BASHPP_RUST_CONTEXT"]
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(plan.SourceDir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	data, err := rustReadBounded(path, rustContextLimit)
	if err != nil {
		return fmt.Errorf("RUST-ECONTEXT: %w", err)
	}
	c, err := readRustContext(path, rustSHA(data))
	if err != nil {
		return fmt.Errorf("RUST-ECONTEXT: %w", err)
	}
	plan.rustContext, err = snapshotRustContext(c, path)
	if err != nil {
		return fmt.Errorf("RUST-ECONTEXT: %w", err)
	}
	plan.RustTarget = c.Target
	plan.Env = append(plan.Env, "BASHPP_RUST_CONTEXT="+path, "BASHPP_RUST_CONTEXT_SHA256="+rustSHA(data))
	sort.Strings(plan.Env)
	return nil
}

func (r Rust) dependencyContext() (string, string) {
	path, digest := r.ContextFile, r.ContextSHA256
	if r.Environment != nil {
		for _, entry := range r.Environment.Env {
			k, v, _ := strings.Cut(entry, "=")
			switch k {
			case "BASHPP_RUST_CONTEXT":
				path = v
			case "BASHPP_RUST_CONTEXT_SHA256":
				digest = v
			}
		}
	}
	return path, digest
}

// target is selected by the authenticated context, never by a second overlay
// or an inferred source shape. A standalone Rust value reads the same context.
func (r Rust) target() (string, error) {
	path, digest := r.dependencyContext()
	if path == "" {
		if r.Environment != nil && r.Environment.RustTarget != "" && r.Environment.RustTarget != "binary" {
			return "", fmt.Errorf("RUST-ECONTEXT: Rust target requires a locked context")
		}
		return "binary", nil
	}
	var c RustDependencyContext
	if r.Environment != nil && r.Environment.rustContext != nil {
		c = r.Environment.rustContext.context
	} else {
		var err error
		c, err = readRustContext(path, digest)
		if err != nil {
			return "", fmt.Errorf("RUST-ECONTEXT: %w", err)
		}
	}
	if r.Environment != nil && r.Environment.RustTarget != c.Target {
		return "", fmt.Errorf("RUST-ECONTEXT: Rust target changed after discovery")
	}
	if c.Target == "" {
		return "binary", nil
	}
	return c.Target, nil
}

func (c RustDependencyContext) manifest() string { return c.manifestAt("") }
func (c RustDependencyContext) manifestAt(base string) string {
	var b strings.Builder
	b.WriteString("[package]\nname = \"bashpp_module\"\nversion = \"0.0.0\"\nedition = \"2024\"\n[workspace]\n")
	names := make([]string, 0, len(c.Dependencies))
	for n := range c.Dependencies {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteString("exclude = [")
	for _, n := range names {
		if p := c.Dependencies[n].Path; p != "" {
			b.WriteString(strconv.Quote(p) + ",")
		}
	}
	b.WriteString("]\n[dependencies]\n")
	for _, n := range names {
		d := c.Dependencies[n]
		fmt.Fprintf(&b, "%s = { version = %s, default-features = %t, features = [", n, strconv.Quote("="+d.Version), d.DefaultFeatures)
		for i, f := range d.Features {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(f))
		}
		b.WriteString("]")
		if d.Path != "" {
			path := d.Path
			if base != "" {
				path = filepath.Join(base, filepath.FromSlash(path))
			}
			fmt.Fprintf(&b, ", path = %s", strconv.Quote(path))
		}
		b.WriteString(" }\n")
	}
	b.WriteString("[profile.dev]\ndebug = false\n")
	return b.String()
}

// copyClosure uses a rooted filesystem to reject symlinks, traversal and special
// files, hashes the bytes actually copied, and never copies extra ambient files.
func (c RustDependencyContext) visitClosure(from string, visit func(string, []byte) error) error {
	root, err := os.OpenRoot(from)
	if err != nil {
		return err
	}
	defer root.Close()
	total := 0
	for path, want := range c.Files {
		parts := strings.Split(path, "/")
		for i := range parts {
			info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink in closure: %s", path)
			}
		}
		f, err := root.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 32<<20 {
			f.Close()
			return fmt.Errorf("invalid closure file %s", path)
		}
		data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		f.Close()
		if err != nil {
			return err
		}
		total += len(data)
		if len(data) > 32<<20 || total > rustClosureLimit {
			return fmt.Errorf("closure size exceeded")
		}
		if rustSHA(data) != want {
			return fmt.Errorf("closure digest mismatch: %s", path)
		}
		if path == "Cargo.lock" {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "source =") && line != `source = "registry+https://github.com/rust-lang/crates.io-index"` {
					return fmt.Errorf("only crates.io registry lock sources supported")
				}
			}
		}
		if err := visit(path, data); err != nil {
			return err
		}
	}
	return nil
}

func writeRustClosureFile(to, path string, data []byte) error {
	dest := filepath.Join(to, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0600)
}
func (c RustDependencyContext) copyClosure(from, to string) error {
	return c.visitClosure(from, func(path string, data []byte) error { return writeRustClosureFile(to, path, data) })
}

// No caller can mutate this snapshot; Clone may share it. Strings own the
// authenticated bytes and no references to the original filesystem survive.
type rustDependencySnapshot struct {
	context RustDependencyContext
	files   map[string]string
}

func snapshotRustContext(c RustDependencyContext, path string) (*rustDependencySnapshot, error) {
	snapshot := &rustDependencySnapshot{context: c, files: map[string]string{}}
	err := c.visitClosure(filepath.Dir(path), func(path string, data []byte) error { snapshot.files[path] = string(data); return nil })
	return snapshot, err
}

func rustProviderHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PrewarmDependencies builds only the explicit locked closure. Callers retain
// their own separate prerequisite deadline; normal preparation never extends it.
func (r Rust) PrewarmDependencies(ctx context.Context, source string) error {
	path, digest := r.dependencyContext()
	if path == "" {
		return fmt.Errorf("RUST-ECONTEXT: prewarm requires an explicit context")
	}
	_, err := r.buildLockedMode(ctx, source, "", path, digest, true)
	return err
}
func (r Rust) buildLocked(ctx context.Context, source, generated, path, digest string) ([]byte, error) {
	return r.buildLockedMode(ctx, source, generated, path, digest, false)
}
func (r Rust) buildLockedMode(ctx context.Context, source, generated, path, digest string, prewarm bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var c RustDependencyContext
	var snapshot *rustDependencySnapshot
	if r.Environment != nil {
		snapshot = r.Environment.rustContext
	}
	if snapshot != nil {
		c = snapshot.context
	} else {
		var err error
		c, err = readRustContext(path, digest)
		if err != nil {
			return nil, fmt.Errorf("RUST-ECONTEXT: %w", err)
		}
		snapshot, err = snapshotRustContext(c, path)
		if err != nil {
			return nil, fmt.Errorf("RUST-ECONTEXT: %w", err)
		}
	}
	if rustSHA([]byte(source)) != c.SourceSHA256 {
		return nil, fmt.Errorf("RUST-ECONTEXT: source identity mismatch")
	}
	procMacro := c.Target == "proc-macro"
	cargo, err := r.cargo()
	if err != nil {
		return nil, fmt.Errorf("RUST-EPROVIDER: %w", err)
	}
	if len(cargo) != 1 {
		return nil, fmt.Errorf("RUST-EPROVIDER: cargo wrappers with arguments unsupported")
	}
	rustc, err := exec.LookPath(r.executable())
	if err != nil {
		return nil, fmt.Errorf("RUST-EPROVIDER: %w", err)
	}
	cargoPath, err := exec.LookPath(cargo[0])
	if err != nil {
		return nil, fmt.Errorf("RUST-EPROVIDER: %w", err)
	}
	dir, err := os.MkdirTemp("", "bashpp-rust-locked-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	// Cargo reads ancestor configuration even with a private CARGO_HOME.
	for parent := filepath.Dir(dir); ; parent = filepath.Dir(parent) {
		for _, name := range []string{"config", "config.toml"} {
			if _, e := os.Lstat(filepath.Join(parent, ".cargo", name)); !os.IsNotExist(e) {
				return nil, fmt.Errorf("RUST-ECONTEXT: ambient ancestor Cargo configuration")
			}
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	target := filepath.Join(dir, "target")

	var inherited []string
	if r.Environment != nil {
		inherited = r.Environment.Env
	} else {
		inherited = os.Environ()
	}
	selected := envMap(inherited)
	clean := nativeLaunchEnvironment(selected)
	for _, name := range []string{"RUSTUP_HOME", "RUSTUP_TOOLCHAIN"} {
		if v := selected[name]; v != "" {
			clean = append(clean, name+"="+v)
		}
	}
	clean = append(clean, "CARGO_HOME="+filepath.Join(dir, "cargo-home"), "CARGO_TARGET_DIR="+target, "CARGO_NET_OFFLINE=true", "CARGO_TERM_COLOR=never", "RUSTC="+rustc)
	for path, want := range c.ProviderFiles {
		got, e := rustProviderHash(path)
		if e != nil || got != want {
			return nil, fmt.Errorf("RUST-EPROVIDER: SDK identity mismatch: %s", path)
		}
	}
	for name, tool := range c.BuildTools {
		got, e := rustProviderHash(tool.Path)
		if e != nil || got != tool.SHA256 {
			return nil, fmt.Errorf("RUST-EPROVIDER: build tool identity mismatch: %s", name)
		}
		if name == "LINKER" {
			clean = append(clean, "CARGO_ENCODED_RUSTFLAGS=-C\x1flinker="+tool.Path)
		} else {
			clean = append(clean, name+"="+tool.Path)
		}
	}

	for _, p := range []struct{ path, hash, version, arg string }{{rustc, c.RustcSHA256, c.RustcVersion, "-vV"}, {cargoPath, c.CargoSHA256, c.CargoVersion, "-V"}} {
		got, e := rustProviderHash(p.path)
		if e != nil || got != p.hash {
			return nil, fmt.Errorf("RUST-EPROVIDER: executable identity mismatch: %s", p.path)
		}
		cmd := exec.CommandContext(ctx, p.path, p.arg)
		cmd.Dir = dir
		cmd.Env = clean
		out, e := cmd.CombinedOutput()
		if e != nil || strings.TrimSpace(string(out)) != p.version {
			return nil, fmt.Errorf("RUST-EPROVIDER: version identity mismatch: %s", p.path)
		}
	}
	closure := dir
	if c.CacheDir != "" {
		closure, target, err = snapshot.materializeCache(ctx)
		if err != nil {
			return nil, fmt.Errorf("RUST-ECONTEXT: %w", err)
		}
		for i, entry := range clean {
			if strings.HasPrefix(entry, "CARGO_TARGET_DIR=") {
				clean[i] = "CARGO_TARGET_DIR=" + target
			}
		}
		if err := writeRustClosureFile(dir, "Cargo.lock", []byte(snapshot.files["Cargo.lock"])); err != nil {
			return nil, err
		}
	} else {
		if prewarm {
			return nil, fmt.Errorf("RUST-ECONTEXT: prewarm requires cache_dir")
		}
		for path, data := range snapshot.files {
			if err := writeRustClosureFile(dir, path, []byte(data)); err != nil {
				return nil, fmt.Errorf("RUST-ECONTEXT: %w", err)
			}
		}
	}
	// Root modules belong to this isolated source build, including with a shared
	// dependency cache. Never discover neighboring files from the caller's tree.
	for path, data := range snapshot.files {
		if rustRootModule(path) {
			if err := writeRustClosureFile(dir, path, []byte(data)); err != nil {
				return nil, fmt.Errorf("RUST-ECONTEXT: %w", err)
			}
		}
	}
	// Unique root output names prevent concurrent source builds sharing a target
	// from overwriting each other. Dependency crates retain their stable identity.
	name := "m" + rustSHA([]byte(dir))[:16]
	defer func() {
		for _, pattern := range []string{filepath.Join(target, "debug", name+"*"), filepath.Join(target, "debug", "deps", name+"-*"), filepath.Join(target, "debug", ".fingerprint", name+"-*"), filepath.Join(target, "debug", "incremental", name+"-*")} {
			paths, _ := filepath.Glob(pattern)
			for _, path := range paths {
				_ = os.RemoveAll(path)
			}
		}
	}()
	sourcePath := "src/main.rs"
	placeholder := "fn main() {}\n"
	manifest := c.manifestAt(closure)
	if procMacro {
		sourcePath = "src/lib.rs"
		placeholder = "extern crate proc_macro;\n"
		manifest += fmt.Sprintf("\n[lib]\nname = %s\nproc-macro = true\npath = \"src/lib.rs\"\n", strconv.Quote(name))
	} else {
		manifest += fmt.Sprintf("\n[[bin]]\nname = %s\npath = \"src/main.rs\"\n", strconv.Quote(name))
	}
	config := "[source.crates-io]\nreplace-with = \"vendored-sources\"\n[source.vendored-sources]\ndirectory = " + strconv.Quote(filepath.ToSlash(filepath.Join(closure, "vendor"))) + "\n"

	for name, data := range map[string]string{"Cargo.toml": manifest, ".cargo/config.toml": config, sourcePath: placeholder} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			return nil, err
		}
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, cargoPath, args...)
		cmd.Dir = dir
		cmd.Env = clean
		return cmd.CombinedOutput()
	}
	// Resolve without executing dependency build scripts, and reject any package
	// outside the supplied snapshot (including path deps hidden in vendor TOML).
	out, err := run("metadata", "--frozen", "--offline", "--format-version=1")
	if err != nil {
		return nil, fmt.Errorf("RUST-EDEPENDENCY: %s", rustFailure(string(out), err))
	}
	var metadata struct {
		Packages []struct {
			ManifestPath string `json:"manifest_path"`
			Source       string `json:"source"`
			Name         string `json:"name"`
			Version      string `json:"version"`
		}
	}
	if err := json.Unmarshal(out, &metadata); err != nil {
		return nil, fmt.Errorf("RUST-EDEPENDENCY: invalid metadata: %w", err)
	}
	for _, p := range metadata.Packages {
		if p.ManifestPath == filepath.Join(dir, "Cargo.toml") {
			continue
		}
		rel, e := filepath.Rel(closure, p.ManifestPath)
		rel = filepath.ToSlash(rel)
		if e != nil {
			return nil, fmt.Errorf("RUST-EDEPENDENCY: invalid package location")
		}

		if strings.HasPrefix(rel, "vendor/") && p.Source == "registry+https://github.com/rust-lang/crates.io-index" {
			continue
		}
		dep, ok := c.Dependencies[p.Name]
		if ok && dep.Path != "" && rel == dep.Path+"/Cargo.toml" && p.Source == "" && dep.Version == p.Version {
			continue
		}
		return nil, fmt.Errorf("RUST-EDEPENDENCY: package outside declared closure: %s", p.Name)
	}

	out, err = run("build", "--quiet", "--frozen", "--offline", "--message-format=json")
	if err != nil {
		return nil, fmt.Errorf("RUST-EDEPENDENCY: %s", rustFailure(string(out), err))
	}
	if prewarm {
		return nil, nil
	}
	if procMacro {
		generated = source
	}
	if err := os.WriteFile(filepath.Join(dir, sourcePath), []byte(generated), 0600); err != nil {
		return nil, err
	}
	out, err = run("build", "--quiet", "--frozen", "--offline", "--message-format=json")
	if captureErr := captureRustDiagnostics(c, source, generated, name, target, filepath.Join(dir, sourcePath), out); captureErr != nil {
		return nil, fmt.Errorf("RUST-ECAPTURE: %v; %s", captureErr, rustFailure(string(out), err))
	}
	if err != nil {
		class := rustBuildFailureClass(out, filepath.Join(dir, sourcePath), len(source))
		return nil, fmt.Errorf("%s: %s", class, rustFailure(string(out), err))
	}
	if procMacro {
		return nil, nil
	}
	output := filepath.Join(target, "debug", name)
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	return os.ReadFile(output)
}

// Only this exact compiler structure distinguishes an undeclared authored type
// from E0433 dependency/module resolution failures. Unknown forms stay closed.
var rustMissingType = regexp.MustCompile("^cannot find type `([A-Za-z_][A-Za-z0-9_]*)` in this scope$")

func rustBuildFailureClass(out []byte, root string, sourceBytes int) string {
	source := false
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		var m struct {
			Reason string
			Target struct {
				SrcPath string `json:"src_path"`
			}
			Message struct {
				Level   string
				Message string
				Spans   []struct {
					FileName  string `json:"file_name"`
					ByteStart *int   `json:"byte_start"`
					Label     string
					Expansion json.RawMessage
					ByteEnd   int  `json:"byte_end"`
					Primary   bool `json:"is_primary"`
				}
				Code *struct{ Code string }
			}
		}
		if json.Unmarshal(line, &m) != nil {
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
				return "RUST-EBUILD"
			}
			continue
		}
		if m.Reason != "compiler-message" || m.Message.Level != "error" {
			continue
		}
		if m.Target.SrcPath != root {
			return "RUST-EDEPENDENCY"
		}
		if m.Message.Code != nil {
			switch m.Message.Code.Code {
			case "E0432", "E0463", "E0460", "E0514", "E0583":
				return "RUST-EDEPENDENCY"
			}
		}
		if m.Message.Code == nil {
			return "RUST-EBUILD"
		}
		if m.Message.Code.Code == "E0433" {
			match := rustMissingType.FindStringSubmatch(m.Message.Message)
			if match == nil {
				return "RUST-EDEPENDENCY"
			}
			primary := false
			for _, span := range m.Message.Spans {
				if !span.Primary {
					continue
				}
				if (span.FileName != root && span.FileName != "src/"+filepath.Base(root)) ||
					span.ByteStart == nil || *span.ByteStart < 0 || span.ByteEnd <= *span.ByteStart || span.ByteEnd > sourceBytes ||
					(len(span.Expansion) != 0 && string(span.Expansion) != "null") {
					return "RUST-EBUILD"
				}
				if span.Label != "use of undeclared type `"+match[1]+"`" {
					return "RUST-EDEPENDENCY"
				}
				primary = true
			}
			if !primary {
				return "RUST-EBUILD"
			}
			source = true
			continue
		}
		inSource := false
		for _, span := range m.Message.Spans {
			if span.Primary && (span.FileName == root || filepath.ToSlash(span.FileName) == "src/"+filepath.Base(root)) && span.ByteEnd > 0 && span.ByteEnd <= sourceBytes {
				inSource = true
			}
		}
		if !inSource {
			return "RUST-EBUILD"
		}
		source = true
	}
	if source {
		return "RUST-ESOURCE"
	}
	return "RUST-EBUILD"
}

// Reject duplicate keys before decoding maps; otherwise a repeated digest or
// provider identity could be silently replaced by encoding/json.
func rustUniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err := rustUniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := rustUniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid JSON container")
	}
	_, err = d.Token()
	return err
}

// The generated worker owns these two root entrypoints on every platform.
func rustRootModule(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(path, "src/") && strings.HasSuffix(path, ".rs") && lower != "src/main.rs" && lower != "src/lib.rs" && !strings.HasPrefix(lower, "src/main.rs/") && !strings.HasPrefix(lower, "src/lib.rs/")
}

func rustPackageFile(c RustDependencyContext, path string) bool {
	for _, dep := range c.Dependencies {
		if dep.Path != "" && strings.HasPrefix(path, dep.Path+"/") {
			return true
		}
	}
	return false
}

// A cache is opt-in and keyed by the entire closure/provider tuple, excluding
// only the fence source and cache location. Sources are verified on every use;
// Cargo serializes its target directory. No ambient target is ever consulted.
func (s *rustDependencySnapshot) materializeCache(ctx context.Context) (string, string, error) {
	identity := s.context
	identity.SourceSHA256 = ""
	identity.CacheDir = ""
	data, err := json.Marshal(identity)
	if err != nil {
		return "", "", err
	}
	base := filepath.Join(s.context.CacheDir, rustSHA(data))
	if err = os.MkdirAll(base, 0700); err != nil {
		return "", "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", "", err
	}
	closure := filepath.Join(base, "closure")
	if _, err = os.Lstat(closure); os.IsNotExist(err) {
		stage, err := os.MkdirTemp(base, "stage-")
		if err != nil {
			return "", "", err
		}
		defer os.RemoveAll(stage)
		for path, data := range s.files {
			if err = ctx.Err(); err != nil {
				return "", "", err
			}
			if err = writeRustClosureFile(stage, path, []byte(data)); err != nil {
				return "", "", err
			}
		}
		if err = os.Rename(stage, closure); err != nil {
			if _, statErr := os.Stat(closure); statErr != nil {
				return "", "", err
			}
		}
	}
	count := 0
	err = filepath.WalkDir(closure, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in cached closure")
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(closure, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		want, ok := s.context.Files[rel]
		if !ok {
			return fmt.Errorf("undeclared cached closure file")
		}
		b, err := rustReadBounded(path, 32<<20)
		if err != nil {
			return err
		}
		if rustSHA(b) != want {
			return fmt.Errorf("cached closure changed: %s", rel)
		}
		count++
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if count != len(s.files) {
		return "", "", fmt.Errorf("cached closure incomplete")
	}
	return closure, filepath.Join(base, "target"), nil
}
