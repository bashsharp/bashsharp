package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/qiangli/bash/pathconv"
)

// A shell's cwd can differ from its embedding process's cwd. Script operands
// are readable files in that directory, not executable commands on PATH.
func TestRunPathScriptOperand(t *testing.T) {
	withStrictPosixEnv(t, "sh", false)
	dir := t.TempDir()
	pathDir := t.TempDir()
	const name = "test_slug.sh"
	for directory, body := range map[string]string{
		dir:     "printf 'cwd:%s:%s\\n' \"$0\" \"$1\"\n",
		pathDir: "printf 'wrong-PATH-script\\n'\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Make the PATH collision executable, so an accidental fallback really
	// would select it on Unix as well as Windows. The cwd script stays 0600.
	if err := os.Chmod(filepath.Join(pathDir, name), 0700); err != nil {
		t.Fatal(err)
	}
	for _, operand := range []string{name, "./" + name, pathconv.FromOS(filepath.Join(dir, name))} {
		t.Run(operand, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			r, err := interp.New(
				interp.Dir(dir),
				interp.Env(expand.ListEnviron("PATH="+pathconv.FromOS(pathDir))),
				interp.StdIO(nil, &stdout, &stderr),
				interp.Params("--", "argument"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := runPath(r, operand); err != nil {
				t.Fatalf("runPath(%q): %v; stderr=%s", operand, err, &stderr)
			}
			if want := "cwd:" + operand + ":argument\n"; stdout.String() != want {
				t.Fatalf("stdout = %q, want %q; stderr=%s", &stdout, want, &stderr)
			}
		})
	}
}
