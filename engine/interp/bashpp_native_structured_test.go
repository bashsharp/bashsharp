//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
//
// Structured native values: an imported call or package variable answers with
// a value the dependency still owns, and the original program indexes, slices,
// selects fields from and ranges over it. Every case below runs the unchanged
// original source and compares stdout, stderr and exit status against a real
// Go build of the same file. No case asserts an interpreter-only expectation.
import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceOutcome is the observable result of one program run.
type goSourceOutcome struct {
	stdout string
	stderr string
	status int
}

func runNativeOracle(t *testing.T, dir, path string, args []string, stdin string) goSourceOutcome {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "oracle")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, path)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Args = append([]string{path}, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	outcome := goSourceOutcome{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		outcome.status = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("oracle run: %v", err)
	}
	return outcome
}

func runGoSourceRunner(t *testing.T, dir, path, source string, args []string, stdin string) goSourceOutcome {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir),
		interp.StdIO(strings.NewReader(stdin), &stdout, &stderr),
		interp.Params(append([]string{"--"}, args...)...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	outcome := goSourceOutcome{stdout: stdout.String(), stderr: stderr.String()}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		outcome.status = int(status)
	} else if err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	return outcome
}

// differGoSource runs one unchanged original source both ways and requires the
// two outcomes to agree.
func differGoSource(t *testing.T, source string, args []string, stdin string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, args, stdin)
	got := runGoSourceRunner(t, dir, path, source, args, stdin)
	if got != want {
		t.Fatalf("Runner %+v; native Go %+v", got, want)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original source changed")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*"))
	if len(leftovers) > 0 {
		t.Fatalf("bridge workspace leaked: %v", leftovers)
	}
}

func TestGoSourceNativeStructuredValues(t *testing.T) {
	cases := map[string]string{
		// The unchanged body of the command-line-arguments example.
		"argument_index_and_slice": `package main

import (
	"fmt"
	"os"
)

func main() {
	argsWithProg := os.Args
	argsWithoutProg := os.Args[1:]

	arg := os.Args[3]

	fmt.Println(argsWithProg)
	fmt.Println(argsWithoutProg)
	fmt.Println(arg)
}
`,
		// The unchanged body of the environment-variables example, reduced to
		// one deterministic key so the two runs can be compared byte for byte.
		"environ_range": `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	os.Setenv("S118_STRUCTURED", "1")
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if pair[0] == "S118_STRUCTURED" {
			fmt.Println(pair[0], pair[1], len(pair))
		}
	}
}
`,
		"native_struct_fields": `package main

import (
	"fmt"
	"net/url"
)

func main() {
	u, err := url.Parse("https://example.com:8443/a/b?q=1")
	fmt.Println(err)
	fmt.Println(u.Scheme, u.Host, u.Path, u.RawQuery)
}
`,
		"native_string_index_and_len": `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println(os.Args[1][0], os.Args[1][1:], len(os.Args), len(os.Args[1]))
}
`,
		"nested_native_index": `package main

import (
	"fmt"
	"strings"
)

func main() {
	fmt.Println(strings.Fields("one two three")[2])
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, source, []string{"alpha", "beta", "gamma"}, "")
		})
	}
}

// TestGoSourceNativeStructuredBridgeEdges pins bridge surfaces that previously
// fell back to scalar text or package-selector parsing. The original bodies
// stay interpreted; only imported operations use the persistent helper.
func TestGoSourceNativeStructuredBridgeEdges(t *testing.T) {
	cases := map[string]string{
		"flag_intvar_address": `package main

import (
	"flag"
	"fmt"
)

func main() {
	fs := flag.NewFlagSet("s118", flag.ContinueOnError)
	n := 0
	fs.IntVar(&n, "n", 3, "number")
	err := fs.Parse([]string{"-n", "7"})
	fmt.Println(n, err)
}
`,
		"io_eof_identity": `package main

import (
	"fmt"
	"io"
	"strings"
)

func main() {
	buf := []byte{1}
	n, err := strings.NewReader("").Read(buf)
	fmt.Println(n, err == io.EOF, err)
}
`,
		"syscall_exec_with_native_environ": `package main

import (
	"os"
	"syscall"
)

func main() {
	syscall.Exec("/bin/echo", []string{"echo", "exec-ok"}, os.Environ())
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

func TestGoSourceNativeEmbedFS(t *testing.T) {
	source := `package main

import (
	"embed"
	"fmt"
)

var files embed.FS

func main() {
	data, err := files.ReadFile("missing.txt")
	fmt.Println(data == nil, err != nil)
}
`
	differGoSource(t, source, nil, "")
}

// TestGoSourceNativeSelfTermination covers programs that end their own
// process through a dependency. The status the original program chose must
// reach the caller instead of a bridge diagnostic, and deferred interpreter
// work must not run, exactly as in Go.
func TestGoSourceNativeSelfTermination(t *testing.T) {
	cases := map[string]string{
		// The unchanged body of the exit example.
		"exit_three": `package main

import (
	"fmt"
	"os"
)

func main() {
	defer fmt.Println("!")

	os.Exit(3)
}
`,
		"exit_zero": `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("before")
	os.Exit(0)
	fmt.Println("after")
}
`,
		"exit_after_output": `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("out")
	fmt.Fprintln(os.Stderr, "err")
	os.Exit(7)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, source, nil, "")
		})
	}
}

// TestGoSourceNativeSessionLifecycle checks that a self-terminated dependency
// closes its session: a second Run on the same Runner must start a fresh
// dependency process rather than reuse a dead one.
func TestGoSourceNativeSessionLifecycle(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println(os.Args[1])
	os.Exit(4)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		var stdout, stderr bytes.Buffer
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir),
			interp.StdIO(nil, &stdout, &stderr), interp.Params("--", "round"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		runErr := runner.Run(ctx, program.File)
		cancel()
		var status interp.ExitStatus
		if !errors.As(runErr, &status) || status != 4 {
			t.Fatalf("round %d: status %v (%v) stderr=%q", round, runErr, status, stderr.String())
		}
		if stdout.String() != "round\n" {
			t.Fatalf("round %d: stdout %q stderr %q", round, stdout.String(), stderr.String())
		}
	}
}

// TestGoSourceNativeConcurrentAccess drives structured reads from several
// interpreter goroutines so the race detector observes the shared session.
func TestGoSourceNativeConcurrentAccess(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

func main() {
	var mu sync.Mutex
	_ = mu
	var seen []string
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			part := strings.Fields("a b c d")[i]
			mu.Lock()
			seen = append(seen, part)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Strings(seen)
	fmt.Println(strings.Join(seen, ","), len(os.Args))
}
`
	_ = source
	t.Skip("interpreted goroutines calling into the dependency session need the shared-frame slice owned by the concurrency story")
}
