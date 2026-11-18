//go:build full

package interp_test

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

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceOrderedOutputInterleaving uses one writer for both descriptors:
// its byte sequence is the observable ordering between stdout and stderr.
func TestGoSourceOrderedOutputInterleaving(t *testing.T) {
	path := filepath.Join("testdata", "sprint153", "output", "interleaving", "main.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run != 2; run++ {
		want := runOrderedNative(t, path)
		got := runOrderedInterpreter(t, path, string(source))
		if got.status != want.status || got.combined != want.combined {
			t.Fatalf("native bridge output barrier violated: run %d interpreter=%+v native=%+v", run, got, want)
		}
		if !orderedOutputEqual(got, want) {
			t.Fatal("equal captures compared unequal")
		}
		corrupt := got
		corrupt.combined = "native-stderr-2\\n" + strings.Replace(corrupt.combined, "native-stderr-2\\n", "", 1)
		if orderedOutputEqual(corrupt, want) {
			t.Fatal("merged/corrupted output order compared equal")
		}
	}
	if got := runOrderedInterpreter(t, filepath.Join("testdata", "sprint153", "output", "interleaving", "supported.go"), `package main
import "fmt"
func main() { fmt.Println("native-stdout-only") }`); got != (orderedOutput{combined: "native-stdout-only\n"}) {
		t.Fatalf("supported control: %+v", got)
	}
}

func TestGoSourcePanicHonorsGoTracebackNone(t *testing.T) {
	got := runOrderedInterpreter(t, "panic.go", "package main\nfunc main(){panic(\"final-panic\")}\n")
	want := orderedOutput{combined: "panic: final-panic\n", status: 2}
	if got != want {
		t.Fatalf("interpreter=%+v want=%+v", got, want)
	}
}

func TestGoSourceSelectDefaultPreservesPriorNativeOutput(t *testing.T) {
	got := runOrderedInterpreter(t, "select-default.go", `package main
import "fmt"
func main() {
	fmt.Println("native-before-default")
	select {
	case <-make(chan int):
		println("unreachable")
	default:
		println("local-after-default")
	}
}`)
	want := orderedOutput{combined: "native-before-default\nlocal-after-default\n"}
	if got != want {
		t.Fatalf("select default output order: got %+v want %+v", got, want)
	}
}

// The fast worker-side marker cannot be written after the interpreted program
// closes a dependency descriptor. The host-held write end remains available as
// the ordering fallback, so the bridge reply and later local output cannot hang.
func TestGoSourceOutputBarrierAfterNativeClose(t *testing.T) {
	got := runOrderedInterpreter(t, "close-stdout.go", `package main
import "os"
func main() {
	os.Stdout.Close()
	println("local-after-close")
}`)
	want := orderedOutput{combined: "local-after-close\n"}
	if got != want {
		t.Fatalf("output after native close: got %+v want %+v", got, want)
	}
}

type orderedOutput struct {
	combined string
	status   int
}

func orderedOutputEqual(got, want orderedOutput) bool { return got == want }

func runOrderedNative(t *testing.T, path string) orderedOutput {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "native")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, path)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native: %v: %s", err, output)
	}
	var combined bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "GOTRACEBACK=none")
	cmd.Stdout, cmd.Stderr = &combined, &combined
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("native run: %v", err)
	}
	return orderedOutput{combined: combined.String(), status: exit.ExitCode()}
}

func runOrderedInterpreter(t *testing.T, path, source string) orderedOutput {
	t.Helper()
	p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var combined bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Env(expand.ListEnviron("GOTRACEBACK=none")), interp.StdIO(nil, &combined, &combined))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), p.File)
	var status interp.ExitStatus
	if err == nil {
		return orderedOutput{combined: combined.String()}
	}
	if !errors.As(err, &status) {
		t.Fatalf("interpreter run: %v; output=%q", err, combined.String())
	}
	return orderedOutput{combined: combined.String(), status: int(status)}
}
