package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRunnerExecRedirectPersistsAcrossFunctionReturn(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	originalPath := write("original", "original\n")
	write("replacement", "replacement\n")
	write("temporary", "temporary\n")

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "exec_in_function_persists",
			src:  "exec 8<replacement; restore() { exec 0<&8; }; restore; read value; printf %s \"$value\"",
			want: "replacement",
		},
		{
			name: "function_call_redirect_is_boundary",
			src:  "exec 8<replacement; restore() { exec 0<&8; }; restore <temporary; read value; printf %s \"$value\"",
			want: "original",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, err := os.Open(originalPath)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			file, err := syntax.NewParser().Parse(
				bytes.NewBufferString(test.src), "function-fd")
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(
				interp.Dir(dir), interp.StdIO(input, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatalf("run: %v; stderr: %s", err, stderr.String())
			}
			if got := stdout.String(); got != test.want {
				t.Fatalf("stdout = %q, want %q; stderr: %s", got, test.want, stderr.String())
			}
		})
	}
}
