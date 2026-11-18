package polyglot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSourceFileUsesFenceToolchainAndPassesProcessContract(t *testing.T) {
	saved := ToolResolver
	ToolResolver = func(name string) ([]string, string, error) {
		if name != "python3" {
			return nil, "", fmt.Errorf("unexpected tool %s", name)
		}
		return []string{os.Args[0], "-test.run=TestSourceFileHelperProcess", "--"}, "test helper", nil
	}
	t.Cleanup(func() { ToolResolver = saved })
	path := filepath.Join(t.TempDir(), "program.py")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	status, err := RunSourceFile(path, []string{"arg"}, strings.NewReader("input"), &stdout, &stderr)
	if err != nil || status != 23 || stdout.String() != "stdout:input:arg" || stderr.String() != "stderr" {
		t.Fatalf("status=%d err=%v stdout=%q stderr=%q", status, err, stdout.String(), stderr.String())
	}
}

func TestSourceFileHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(os.Args) < separator+3 {
		return
	}
	in, _ := io.ReadAll(os.Stdin)
	fmt.Fprintf(os.Stdout, "stdout:%s:%s", in, os.Args[separator+2])
	fmt.Fprint(os.Stderr, "stderr")
	os.Exit(23)
}

func TestRunSourceFileDiagnosticNamesPositionAndFence(t *testing.T) {
	saved := ToolResolver
	ToolResolver = func(string) ([]string, string, error) { return nil, "", errors.New("missing runtime") }
	t.Cleanup(func() { ToolResolver = saved })
	path := filepath.Join(t.TempDir(), "program.py")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := RunSourceFile(path, nil, nil, io.Discard, io.Discard)
	if status != 2 || err == nil || !strings.Contains(err.Error(), path+":1:1:") || !strings.Contains(err.Error(), "~~~py fence") {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestRunSourceFileRefusesUnsupportedExtensions(t *testing.T) {
	cases := []struct {
		ext string
	}{
		{ext: ".fs"},
		{ext: ".fsx"},
		{ext: ".cs"},
		{ext: ".ps1"},
		{ext: ".unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "program"+tc.ext)
			if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
				t.Fatal(err)
			}
			status, err := RunSourceFile(path, nil, nil, io.Discard, io.Discard)
			if status != 2 {
				t.Fatalf("status = %d, want 2", status)
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			wantErr := fmt.Sprintf("%s:1:1: unsupported source extension %s", path, tc.ext)
			if err.Error() != wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), wantErr)
			}
		})
	}
}
