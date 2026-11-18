package interp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestStrictPosixBuiltinPathLookup(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		strict     bool
		wantCode   uint8
		wantStdout string
		wantStderr bool // if true, expect non-empty stderr
		setupDir   func(string)
	}{
		{
			name:       "non-strict: PATH= echo runs builtin",
			script:     "PATH=; echo hello",
			strict:     false,
			wantCode:   0,
			wantStdout: "hello\n",
		},
		{
			name:       "strict: PATH= echo fails lookup",
			script:     "PATH=; echo hello",
			strict:     true,
			wantCode:   127,
			wantStdout: "",
			wantStderr: true,
		},
		{
			name:       "non-strict: PATH=./dir3 pwd runs builtin",
			script:     "PATH=./dir3 pwd",
			strict:     false,
			wantCode:   0,
			wantStdout: "should_be_ignored\n", // pwd outputs current dir
		},
		{
			name:       "strict: PATH=./dir3 pwd fails lookup",
			script:     "PATH=./dir3 pwd",
			strict:     true,
			wantCode:   127,
			wantStdout: "",
			wantStderr: true,
			setupDir: func(dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "dir3"), 0777); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "strict: function followed by assignment error kills shell",
			script: `
func() { echo not reached function; }
readonly a=A
a=B func
echo not reached command
`,
			strict:     true,
			wantCode:   1, // typical exit code for readonly assignment error
			wantStdout: "",
			wantStderr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setupDir != nil {
				tc.setupDir(dir)
			}

			var stdout, stderr strings.Builder
			r, err := New(
				WithStrictPosix(tc.strict),
				StdIO(nil, &stdout, &stderr),
				Dir(dir),
			)
			if err != nil {
				t.Fatal(err)
			}
			f, err := syntax.NewParser().Parse(strings.NewReader(tc.script), "")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			err = r.Run(ctx, f)

			// Determine actual exit code
			var exitCode uint8
			if err != nil {
				if e, ok := err.(ExitStatus); ok {
					exitCode = uint8(e)
				} else {
					exitCode = 1
				}
			}

			if exitCode != tc.wantCode {
				t.Errorf("expected exit code %d, got %d. err: %v", tc.wantCode, exitCode, err)
			}

			outStr := stdout.String()
			errStr := stderr.String()

			// special handling for pwd output because it's dynamic
			if strings.Contains(tc.script, "pwd") && exitCode == 0 {
				if outStr == "" {
					t.Errorf("expected pwd output, got empty")
				}
			} else if tc.wantStdout != "should_be_ignored\n" && outStr != tc.wantStdout {
				t.Errorf("expected stdout %q, got %q", tc.wantStdout, outStr)
			}

			if tc.wantStderr && errStr == "" {
				t.Errorf("expected stderr, got empty")
			}
			if !tc.wantStderr && errStr != "" {
				t.Errorf("expected empty stderr, got %q", errStr)
			}
		})
	}
}
