//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceS319StandardWritersStayLocal(t *testing.T) {
	var mu sync.Mutex
	writes := 0
	bashPPNativeRequestTrace = func(q bashPPBridgeRequest) {
		if q.Selector == "Write" {
			mu.Lock()
			writes++
			mu.Unlock()
		}
	}
	defer func() { bashPPNativeRequestTrace = nil }()

	source := `package main
import (
	"fmt"
	"io"
	"os"
)
func main() {
	fmt.Print("a")
	var stdout io.Writer = os.Stdout
	stdout.Write([]byte("b"))
	fmt.Print("c")
	var stderr io.Writer = os.Stderr
	stderr.Write([]byte("err"))
	io.Discard.Write([]byte("discarded"))
	f, err := os.Create("ordinary.txt")
	if err != nil { panic(err) }
	f.Write([]byte("file"))
	f.Close()
	os.Remove("ordinary.txt")
	if err := os.Stdout.Close(); err != nil { panic(err) }
	if n, err := stdout.Write([]byte("closed")); n != 0 || err == nil { panic("closed stdout write") }
}
`
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(runner.Dir, "standard-writers.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "abc"; got != want {
		t.Fatalf("stdout=%q, want %q", got, want)
	}
	if got, want := stderr.String(), "err"; got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 2 {
		t.Fatalf("dependency writer bridge count=%d, want the ordinary file and closed stdout writes", writes)
	}
}

func TestGoSourceS281LocalFmtWriterStaysInInterpreter(t *testing.T) {
	var mu sync.Mutex
	steps := map[string]int{}
	goSourceReflectTrace = func(step string) {
		mu.Lock()
		steps[step]++
		mu.Unlock()
	}
	defer func() { goSourceReflectTrace = nil }()

	source := `package main

import "fmt"

type sink struct{ data string }

func (s *sink) Write(p []byte) (int, error) {
	s.data += string(p)
	return len(p), nil
}

func main() {
	var s sink
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&s, "%6d  ", i)
		fmt.Fprint(&s, ".  ")
		fmt.Fprintln(&s, "Name:", "node")
	}
	fmt.Println(len(s.data) > 0)
}
`
	var out, errout bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "fmtwriter.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.Run(ctx, p.File); err != nil {
		t.Fatalf("run: %v\nstderr=%q", err, errout.String())
	}
	if out.String() != "true\n" || errout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errout.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if got := steps["fmt.Fprintf"] + steps["fmt.Fprint"] + steps["fmt.Fprintln"]; got != 0 {
		t.Fatalf("local fmt writer crossed to dependency helper %d times; steps=%v", got, steps)
	}
}

func TestGoSourceS319VariadicFmtWriterFormatsBeforeLocalWrite(t *testing.T) {
	source := `package main

import "fmt"

type sink struct{ data string }
func (s *sink) Write(p []byte) (int, error) { s.data += string(p); return len(p), nil }

type label int
func (n label) String() string { return fmt.Sprintf("label:%d", int(n)) }

func emit(s *sink, format string, args ...any) { fmt.Fprintf(s, format, args...) }

func main() {
	var s sink
	emit(&s, "%s/%T/%04d", label(3), int16(4), int8(5))
	fmt.Println(s.data)
}
`
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(runner.Dir, "fmtwriter-spread.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "label:3/int16/0005\n"; got != want || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want stdout %q", got, stderr.String(), want)
	}
}
