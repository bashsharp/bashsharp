//go:build full

package interp_test

// Sprint: #374; Story: #1526; Story-ID: 67da7564f1af

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceS374TestBinaryIdentity ensures an interpreted package test
// exposes executable paths to its generated test main and to its actual test
// callback. The paths need not name a native copy of the interpreted program:
// the launcher re-enters the interpreter. They must nevertheless be executable
// paths, rather than the generated _testmain.go source.
func TestGoSourceS374TestBinaryIdentity(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"

	_test "example.com/test"
)

var tests = []testing.InternalTest{{"TestBinary", _test.TestBinary}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	executable, err := os.Executable()
	if err != nil { panic(err) }
	fmt.Printf("main argv0=%s executable=%s\n", os.Args[0], executable)
	_, err = os.Stat(executable)
	if err != nil { panic("os.Executable did not return an executable") }
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)
	testPackage := gosource.PackageSpec{Path: "example.com/test", Sources: []gosource.Source{s249Source("test.go", `package test

import (
	"fmt"
	"os"
	"testing"
)

func TestBinary(t *testing.T) {
	executable, err := os.Executable()
	if err != nil { t.Fatal(err) }
	_, err = os.Stat(executable)
	if err != nil { t.Fatal("os.Executable did not return an executable") }
	fmt.Printf("test argv0=%s executable=%s\n", os.Args[0], executable)
}
`)}}
	program, err := gosource.Load([]gosource.Source{driver}, gosource.Options{
		RunMain: true, ImportPath: "example.com/test.test", TestMain: true, Packages: []gosource.PackageSpec{testPackage},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.StdIO(nil, &stdout, &stderr),
		interp.GoSourceIdentity("example.com/test.test", true),
		interp.GoSourceReexecPlan(self, "-test.run=^$", "--"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	var status interp.ExitStatus
	if err != nil && (!errors.As(err, &status) || status != 0) {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	paths := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasSuffix(fields[1], "argv0="+strings.TrimPrefix(fields[1], "argv0=")) || !strings.HasPrefix(fields[2], "executable=") {
			continue
		}
		paths[fields[0]+" argv0"] = strings.TrimPrefix(fields[1], "argv0=")
		paths[fields[0]+" executable"] = strings.TrimPrefix(fields[2], "executable=")
	}
	for _, name := range []string{"main argv0", "main executable", "test argv0", "test executable"} {
		path := paths[name]
		if path == "" || strings.HasSuffix(path, ".go") || !filepath.IsAbs(path) {
			t.Errorf("%s = %q, want an absolute executable path; stdout=%q", name, path, stdout.String())
			continue
		}
	}
	if paths["test argv0"] != paths["main argv0"] {
		t.Errorf("test binary identities = %#v, want generated main and testing callback to share the worker executable", paths)
	}
}
