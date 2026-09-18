package transpile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiangli/bashsharp/front"
)

func TestGoSourceVersionWiring(t *testing.T) {
	bytes := []byte("package p\nvar _ = 0b101\n")
	files := []front.GoSourceFile{{Name: "original.go", Data: bytes}}
	for _, v := range []string{"go1.12", "go1.13", "go1.12"} {
		_, err := loadGoSource(files, front.GoSourceOptions{GoVersion: v, Dir: t.TempDir()})
		if v == "go1.12" {
			if err == nil || !strings.Contains(err.Error(), "binary literal requires go1.13 or later") {
				t.Fatalf("old language accepted: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTranspileGoSourceVersion(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "original.go"), filepath.Join(dir, "generated.go")
	bytes := []byte("package p\nvar _ = 0b101\n")
	if err := os.WriteFile(source, bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{{"--go-version=go1.12"}, {"--go-version", "go1.12"}} {
		args := append([]string{"--bashpp", "--source=go"}, flags...)
		args = append(args, source, "-o", output)
		exit, stderr := captureTranspileStderr(t, args)
		if exit != 2 || !strings.Contains(stderr, "binary literal requires go1.13 or later") {
			t.Fatalf("version dropped: exit=%d stderr=%q", exit, stderr)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("rejected source produced an artifact")
		}
	}
	for _, flags := range [][]string{{"--source=sh", "--go-version=go1.12"}, {"--source=go", "--go-version="}} {
		args := append([]string{"--bashpp"}, flags...)
		exit, _ := captureTranspileStderr(t, append(args, source, "-o", output))
		if exit != 2 {
			t.Fatal("invalid version invocation accepted")
		}
	}
	got, err := os.ReadFile(source)
	if err != nil || string(got) != string(bytes) {
		t.Fatalf("source changed: %v", err)
	}
}

func TestGoSourceTestBuiltinsWiring(t *testing.T) {
	files := []front.GoSourceFile{{Name: "builtins.go", Data: []byte("package p\nfunc f() { assert(true) }\n")}}
	_, err := loadGoSource(files, front.GoSourceOptions{Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "undefined: assert") {
		t.Fatalf("test builtins enabled by default: %v", err)
	}
	if _, err := loadGoSource(files, front.GoSourceOptions{Dir: t.TempDir(), TestBuiltins: true}); err != nil {
		t.Fatalf("test builtins not enabled: %v", err)
	}
}

func TestTranspileGoSourceTestBuiltins(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "original.go")
	if err := os.WriteFile(source, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalLoad := front.GoSourceLoad
	t.Cleanup(func() { front.GoSourceLoad = originalLoad })
	for _, flag := range []string{"--go-test-builtins", "--go-test-builtins=true"} {
		called := false
		front.GoSourceLoad = func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
			called = true
			if !opts.TestBuiltins {
				t.Fatalf("%s option dropped: %+v", flag, opts)
			}
			return nil, errors.New("stop after option capture")
		}
		output := filepath.Join(dir, strings.TrimPrefix(flag, "--")+".go")
		exit, stderr := captureTranspileStderr(t, []string{"--bashpp", "--source=go", flag, source, "-o", output})
		if exit != 2 || !called || !strings.Contains(stderr, "stop after option capture") {
			t.Fatalf("%s: exit=%d called=%v stderr=%q", flag, exit, called, stderr)
		}
	}
	output := filepath.Join(dir, "rejected.go")
	exit, stderr := captureTranspileStderr(t, []string{"--bashpp", "--source=sh", "--go-test-builtins", source, "-o", output})
	if exit != 2 || !strings.Contains(stderr, "requires --source=go") {
		t.Fatalf("flag without Go source accepted: exit=%d stderr=%q", exit, stderr)
	}
}

// Sprint: #154; Story: S154.1; Story-ID: 29abb27c8659
func TestTranspileGoSourceCheckEnvironment(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "original.go")
	if err := os.WriteFile(source, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalLoad := front.GoSourceLoad
	t.Cleanup(func() { front.GoSourceLoad = originalLoad })
	for _, tc := range []struct {
		flag string
		get  func(front.GoSourceOptions) bool
	}{
		{"--go-checker-branch-errors", func(o front.GoSourceOptions) bool { return o.CheckerBranchErrors }},
		{"--go-checker-branch-errors=true", func(o front.GoSourceOptions) bool { return o.CheckerBranchErrors }},
		{"--go-check-after-syntax-errors", func(o front.GoSourceOptions) bool { return o.CheckAfterSyntaxErrors }},
		{"--go-check-after-syntax-errors=true", func(o front.GoSourceOptions) bool { return o.CheckAfterSyntaxErrors }},
	} {
		called := false
		front.GoSourceLoad = func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
			called = true
			if !tc.get(opts) {
				t.Fatalf("%s option dropped: %+v", tc.flag, opts)
			}
			return nil, errors.New("stop after option capture")
		}
		output := filepath.Join(dir, strings.TrimPrefix(strings.SplitN(tc.flag, "=", 2)[0], "--")+".go")
		exit, stderr := captureTranspileStderr(t, []string{"--bashpp", "--source=go", tc.flag, source, "-o", output})
		if exit != 2 || !called || !strings.Contains(stderr, "stop after option capture") {
			t.Fatalf("%s: exit=%d called=%v stderr=%q", tc.flag, exit, called, stderr)
		}
		exit, stderr = captureTranspileStderr(t, []string{"--bashpp", "--source=sh", tc.flag, source, "-o", filepath.Join(dir, "rejected.go")})
		if exit != 2 || !strings.Contains(stderr, "requires --source=go") {
			t.Fatalf("%s without Go source accepted: exit=%d stderr=%q", tc.flag, exit, stderr)
		}
	}
}
