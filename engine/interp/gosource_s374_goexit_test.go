//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceS374GoexitDifferential(t *testing.T) {
	cases := map[string]string{
		"goroutine_defers": `package main
import ("fmt"; "runtime")
func main() { done := make(chan struct{}); go func() { defer close(done); defer fmt.Println("last"); defer fmt.Println("first"); runtime.Goexit(); fmt.Println("unreachable") }(); <-done; fmt.Println("main") }
`,
		"nested_calls": `package main
import ("fmt"; "runtime")
func inner() { defer fmt.Println("inner defer"); runtime.Goexit() }
func middle() { defer fmt.Println("middle defer"); inner() }
func main() { done := make(chan struct{}); go func() { defer close(done); defer fmt.Println("outer defer"); middle() }(); <-done; fmt.Println("main") }
`,
		"goexit_in_defer": `package main
import ("fmt"; "runtime")
func main() { done := make(chan struct{}); go func() { defer close(done); defer func() { fmt.Println("goexit defer"); runtime.Goexit(); fmt.Println("unreachable defer") }(); defer fmt.Println("earlier defer"); return }(); <-done; fmt.Println("main") }
`,
		"recover_does_not_stop": `package main
import ("fmt"; "runtime")
func main() { done := make(chan struct{}); go func() { defer close(done); defer func() { fmt.Printf("recover=%v\\n", recover()) }(); defer fmt.Println("deferred"); runtime.Goexit() }(); <-done; fmt.Println("main") }
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "main.go")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			want, err := exec.Command("go", "run", path).Output()
			if err != nil {
				t.Fatalf("go run: %v", err)
			}
			program, err := gosource.Parse(bytes.NewReader([]byte(source)), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			runErr := runner.Run(ctx, program.File)
			if runErr != nil || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), want) {
				t.Fatalf("interpreter differs: err=%v stdout=%q stderr=%q; go run=%q", runErr, stdout.String(), stderr.String(), want)
			}
		})
	}
}

func TestGoSourceS374FixedbugsIssue5963(t *testing.T) {
	root := filepath.Join(runtime.GOROOT(), "test", "fixedbugs")
	source, err := os.ReadFile(filepath.Join(root, "issue5963.go"))
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	stdout, stderr, err := runGoSource(t, "issue5963", string(source))
	if err != nil || stderr != "" || stdout != "" {
		t.Fatalf("run: %v\nstdout: %q\nstderr: %q", err, stdout, stderr)
	}
}
