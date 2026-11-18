//go:build full

package interp_test

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// The generated Go test main runs package tests through testing's native
// scheduler while their bodies remain interpreted. Parallel subtests must
// therefore keep Go's parent barrier: T.Run returns when the child calls
// T.Parallel, the parent registers the remaining children and returns, and
// only then may the children resume subject to -test.parallel.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

const s281ParallelTestSource = `package paralleltest

import (
	"fmt"
	"sync"
	"testing"
)

func TestParallelRendezvous(t *testing.T) {
	const children = 3
	var ready sync.WaitGroup
	ready.Add(children)
	for i := 0; i < children; i++ {
		i := i
		t.Run(fmt.Sprintf("child%d", i), func(t *testing.T) {
			t.Parallel()
			ready.Done()
			ready.Wait()
			fmt.Println("completed", i)
		})
	}
}
`

func TestS281TestingMainStartParallelSubtestsRendezvous(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/paralleltest\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "parallel_test.go"), []byte(s281ParallelTestSource), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-v", "-parallel=3", ".")
	command.Dir = dir
	var native bytes.Buffer
	command.Stdout, command.Stderr = &native, &native
	if err := command.Run(); err != nil {
		t.Fatalf("native go test: %v\n%s", err, native.String())
	}

	testPackage := gosource.PackageSpec{
		Path:    "example.com/paralleltest",
		Sources: []gosource.Source{s249Source("parallel_test.go", s281ParallelTestSource)},
	}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"
	paralleltest "example.com/paralleltest"
)

var tests = []testing.InternalTest{{"TestParallelRendezvous", paralleltest.TestParallelRendezvous}}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{testPackage}, "-test.v", "-test.parallel=3")
	if err != nil || got.stderr != "" || got.status != 0 || !strings.HasSuffix(got.stdout, "PASS\n") {
		t.Fatalf("run=%v outcome=%+v", err, got)
	}
	assertS281ParallelTrace(t, "native", native.String())
	assertS281ParallelTrace(t, "interpreted", got.stdout)
}

func assertS281ParallelTrace(t *testing.T, mode, output string) {
	t.Helper()
	lastPause, firstContinue := -1, len(output)
	for i := 0; i < 3; i++ {
		for _, marker := range []string{
			fmt.Sprintf("=== RUN   TestParallelRendezvous/child%d\n", i),
			fmt.Sprintf("=== PAUSE TestParallelRendezvous/child%d\n", i),
			fmt.Sprintf("=== CONT  TestParallelRendezvous/child%d\n", i),
			fmt.Sprintf("completed %d\n", i),
		} {
			if strings.Count(output, marker) != 1 {
				t.Fatalf("%s parallel child marker %q missing or duplicated in %q", mode, marker, output)
			}
		}
		pause := strings.Index(output, fmt.Sprintf("=== PAUSE TestParallelRendezvous/child%d\n", i))
		if pause > lastPause {
			lastPause = pause
		}
		continued := strings.Index(output, fmt.Sprintf("=== CONT  TestParallelRendezvous/child%d\n", i))
		if continued < firstContinue {
			firstContinue = continued
		}
	}
	if lastPause < 0 || firstContinue <= lastPause {
		t.Fatalf("%s resumed a parallel child before the parent registered all children:\n%s", mode, output)
	}
}
