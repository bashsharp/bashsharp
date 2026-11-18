package polyglot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustDiagnosticCapture(t *testing.T) {
	for _, mode := range []string{"valid", "disabled", "source", "span", "traversal", "worker", "symlink", "parent-symlink", "oversize", "destination-symlink", "immutable"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			deps := filepath.Join(target, "debug", "deps")
			if err := os.MkdirAll(deps, 0700); err != nil {
				t.Fatal(err)
			}
			source := "fn source() {}\n"
			worker := "m0123456789abcdef"
			c := testRustContext()
			c.CacheDir = dir
			c.SourceSHA256 = rustSHA([]byte(source))
			c.DiagnosticCapture = &RustDiagnosticCapture{1, true}
			path := filepath.Join(deps, worker+"-abc123.long-type-123.txt")
			content := []byte("a long type\n")
			if mode == "oversize" {
				content = []byte(strings.Repeat("x", (64<<10)+1))
			}
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "src", "main.rs")
			end := len(source)
			switch mode {
			case "disabled":
				c.DiagnosticCapture = nil
			case "source":
				c.SourceSHA256 = strings.Repeat("a", 64)
			case "span":
				end++
			case "traversal":
				path = deps + "/../deps/" + filepath.Base(path)
			case "worker":
				worker = "m1111111111111111"
			case "symlink":
				os.Rename(path, path+".real")
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				os.Rename(deps, deps+".real")
				if err := os.Symlink(deps+".real", deps); err != nil {
					t.Fatal(err)
				}
			case "destination-symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, "diagnostics")); err != nil {
					t.Fatal(err)
				}
			}
			record := map[string]any{"reason": "compiler-message", "target": map[string]any{"src_path": root}, "message": map[string]any{"message": "type mismatch", "spans": []any{map[string]any{"file_name": "src/main.rs", "byte_start": 0, "byte_end": end, "is_primary": true}}, "children": []any{map[string]any{"message": "the full name for the type has been written to '" + path + "'"}}}}
			out, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			err = captureRustDiagnostics(c, source, source+"worker", worker, target, root, out)
			if mode != "valid" && mode != "disabled" && mode != "immutable" {
				if err == nil {
					t.Fatal("unsafe capture accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			receipt := filepath.Join(dir, "diagnostics", c.SourceSHA256, worker, filepath.Base(path)+".json")
			data, err := os.ReadFile(receipt)
			if mode == "disabled" {
				if !os.IsNotExist(err) {
					t.Fatal("disabled capture wrote evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got RustDiagnosticAttachment
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Schema != "rust-long-type/v1" || got.WorkerSHA256 != rustSHA([]byte(source+"worker")) || got.SourceSHA256 != c.SourceSHA256 || got.Worker != worker || got.Path != path || got.SHA256 != rustSHA(content) || string(got.Bytes) != string(content) {
				t.Fatalf("invalid receipt: %+v", got)
			}
			if mode == "immutable" {
				if err := captureRustDiagnostics(c, source, source+"worker", worker, target, root, out); err == nil {
					t.Fatal("overwrote receipt")
				}
				again, _ := os.ReadFile(receipt)
				if string(again) != string(data) {
					t.Fatal("receipt changed")
				}
			}
		})
	}
}

func TestRustDiagnosticCaptureAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "schema", "false", "cache"} {
		t.Run(mode, func(t *testing.T) {
			c := testRustContext()
			c.CacheDir = t.TempDir()
			c.DiagnosticCapture = &RustDiagnosticCapture{1, true}
			switch mode {
			case "schema":
				c.DiagnosticCapture.Schema = 2
			case "false":
				c.DiagnosticCapture.LongType = false
			case "cache":
				c.CacheDir = ""
			}
			_, err := readRustContext(writeRustContext(t, t.TempDir(), c), "")
			if (err == nil) != (mode == "valid") {
				t.Fatalf("admission: %v", err)
			}
		})
	}
}

func TestRustDiagnosticCaptureIdentity(t *testing.T) {
	dir := t.TempDir()
	c := testRustContext()
	c.CacheDir = t.TempDir()
	c.Files = map[string]string{}
	for path, data := range map[string]string{"Cargo.lock": "locked", "vendor/crate/Cargo.toml": "manifest"} {
		c.Files[path] = rustSHA([]byte(data))
		if err := writeRustClosureFile(dir, path, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	discover := func() EnvironmentPlan {
		t.Helper()
		writeRustContext(t, dir, c)
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		p := EnvironmentPlan{SourceDir: dir, Executable: exe}
		if err := discoverRustContext(&p, map[string]string{"BASHPP_RUST_CONTEXT": "context.json"}); err != nil {
			t.Fatal(err)
		}
		p.Fingerprint, err = environmentFingerprint(p)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	plain := discover()
	c.DiagnosticCapture = &RustDiagnosticCapture{1, true}
	captured := discover()
	if plain.Fingerprint == captured.Fingerprint {
		t.Fatal("capture omitted from fingerprint")
	}
	cloned := captured.Clone()
	if cloned.rustContext.context.DiagnosticCapture == nil || !cloned.rustContext.context.DiagnosticCapture.LongType {
		t.Fatal("clone lost capture")
	}
	// Lowered plans carry exported fields, then re-admit the authenticated JSON.
	data, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	var lowered EnvironmentPlan
	if err := json.Unmarshal(data, &lowered); err != nil {
		t.Fatal(err)
	}
	path, digest := (Rust{Environment: &lowered}).dependencyContext()
	admitted, err := readRustContext(path, digest)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.DiagnosticCapture == nil || !admitted.DiagnosticCapture.LongType {
		t.Fatal("lowering lost capture")
	}
	c.DiagnosticCapture = nil
	writeRustContext(t, dir, c)
	if _, err := readRustContext(path, digest); err == nil {
		t.Fatal("accepted changed capture policy")
	}
	if cloned.rustContext.context.DiagnosticCapture == nil {
		t.Fatal("snapshot changed")
	}
}
