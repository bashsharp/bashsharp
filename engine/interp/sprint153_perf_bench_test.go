package interp_test

// Sprint: #153; Story: S153.6; Story-ID: 11fffa6b51be
//
// Interpreter call-path benchmarks over the outside-corpus scaled controls
// under testdata/sprint153/deadline/<root>/sizeN/main.go. Each benchmark
// loads one control through gosource and runs it through the interpreter;
// the native oracle is not consulted here because the controls are already
// pinned by TestSprint153Evaluator. Select one with
//
//	go test -run '^$' -bench 'Sprint153Deadline/peano/size6' -benchmem -cpuprofile cpu.out ./interp
import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func BenchmarkSprint153Deadline(b *testing.B) {
	root := filepath.Join("testdata", "sprint153", "deadline")
	roots, err := os.ReadDir(root)
	if err != nil {
		b.Fatal(err)
	}
	for _, r := range roots {
		if !r.IsDir() {
			continue
		}
		sizes, err := os.ReadDir(filepath.Join(root, r.Name()))
		if err != nil {
			b.Fatal(err)
		}
		for _, size := range sizes {
			if !size.IsDir() {
				continue
			}
			path := filepath.Join(root, r.Name(), size.Name(), "main.go")
			source, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			b.Run(r.Name()+"/"+size.Name(), func(b *testing.B) {
				benchGoSource(b, path, string(source))
			})
		}
	}
}

func benchGoSource(b *testing.B, path, source string) {
	b.Helper()
	dir := b.TempDir()
	for b.Loop() {
		if err := runBenchmarkGoSource(dir, path, source); err != nil {
			b.Fatal(err)
		}
	}
}

// A failed program is not a timing sample. In particular, ExitStatus is an
// execution failure too; accepting it can make a semantic regression appear
// faster by measuring only the prefix before the program exits.
func runBenchmarkGoSource(dir, path, source string) error {
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		return fmt.Errorf("gosource.Parse: %w", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr), interp.Params("--"))
	if err != nil {
		return err
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		return fmt.Errorf("Runner: %w; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	return nil
}

// BenchmarkSprint153File runs one Go program named by SPRINT153_BENCH_FILE
// through the interpreter, for profiling a program that is not a checked-in
// control (a corpus root, or a scratch program at a larger size).
func BenchmarkSprint153File(b *testing.B) {
	path := os.Getenv("SPRINT153_BENCH_FILE")
	if path == "" {
		b.Skip("SPRINT153_BENCH_FILE not set")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	benchGoSource(b, path, string(source))
}

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// BenchmarkGoSourceNativeCallHotpath isolates the repeated imported-call shape
// used by interpreted packages which inspect native data one element at a time.
// Keep the call generic: the benchmark is a bridge cost lock, not a corpus-root
// reproduction.
func BenchmarkGoSourceNativeCallHotpath(b *testing.B) {
	const calls = 10_000
	const source = `package main
import "strings"
func main() {
	for i := 0; i < 10000; i++ {
		if !strings.HasPrefix("dwarf", "d") { panic("unreachable") }
	}
}
`
	b.ReportMetric(calls, "calls/op")
	benchGoSource(b, "native-call-hotpath.go", source)
}
