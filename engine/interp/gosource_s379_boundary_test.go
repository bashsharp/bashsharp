//go:build full

package interp_test

// Sprint: #379; Story: #1516; Story-ID: f3de4d65a886

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runGoSourceDependencyModuleError runs a program with a module-local
// dependency package the way differGoSourceDependencyModule does, but returns
// the runner's verdict instead of diffing it against native Go: these tests
// pin refusals.
func runGoSourceDependencyModuleError(t *testing.T, module, dependency, source string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":     "module " + module + "\n\ngo 1.27\n",
		"dep/dep.go": dependency,
		"main.go":    source,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "main.go")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var out bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), runner.Run(ctx, program.File)
}

const s379SinkDependency = `package dep

type Sink interface{ Write(p []byte) (int, error) }

type Keeper struct{ sink Sink }

var kept *Keeper

// Keep retains its argument: in a package global, through the struct it
// returns. A mirrored Write raised on it later would run after the request
// that handed it over has returned.
func Keep(s Sink) *Keeper {
	k := &Keeper{sink: s}
	kept = k
	println("dependency-kept")
	return k
}

// Emit only invokes its argument's method, directly and through a
// same-package helper, before it returns.
func Emit(s Sink, parts ...string) int {
	n := 0
	for _, part := range parts {
		n += write(s, part)
	}
	return n
}

func write(s Sink, part string) int {
	n, _ := s.Write([]byte(part))
	return n
}
`

// A program-module dependency is not on any reviewed list: the general
// method-callback bridge admits it only once its sources prove it cannot
// retain the callback-bearing value. A retainer is refused before it runs.
func TestS379ModuleDependencyRetainerRefused(t *testing.T) {
	out, err := runGoSourceDependencyModuleError(t, "example.com/s379retain", s379SinkDependency, `package main

import "example.com/s379retain/dep"

type sink struct{ n int }

func (s *sink) Write(p []byte) (int, error) { println("original-Write"); s.n += len(p); return len(p), nil }

func main() {
	dep.Keep(&sink{})
	println("after")
}
`)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("retaining dependency accepted: %v %q", err, out)
	}
	for _, marker := range []string{"dependency-kept", "original-Write", "after"} {
		if strings.Contains(out, marker) {
			t.Fatalf("retainer path executed: %q", out)
		}
	}
}

// The same dependency package's synchronous consumer is proven from its
// sources and admitted: the original Write runs on the parked request, and
// the pointee write it makes is reconciled, exactly as native Go observes.
func TestS379ModuleDependencySynchronousConsumerProven(t *testing.T) {
	differGoSourceDependencyModule(t, "example.com/s379sync", s379SinkDependency, `package main

import (
	"fmt"

	"example.com/s379sync/dep"
)

type sink struct{ n int }

func (s *sink) Write(p []byte) (int, error) {
	fmt.Println("write", string(p))
	s.n += len(p)
	return len(p), nil
}

func main() {
	var s sink
	total := dep.Emit(&s, "a", "bc", "def")
	fmt.Println(total, s.n)
}
`)
}
