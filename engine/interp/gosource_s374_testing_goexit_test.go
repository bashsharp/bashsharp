package interp_test

// Sprint: #374; Story-ID: ce0b33ec46f8

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// testing.T.Skip, Fatal, FailNow and SkipNow end the calling test with
// runtime.Goexit. The test body is interpreted, but the *testing.T lives in
// the dependency process, so the Goexit has to end the testing goroutine that
// owns the interpreted callback — after the interpreted frames' defers ran —
// rather than the goroutine which happened to serve the bridged method call.
// Otherwise the method call is never answered and the test hangs until the
// package timeout, hiding every later test.
func TestGoSourceS374TestingGoexitEndsTheInterpretedTest(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/lib_test", Sources: []gosource.Source{s249Source("lib_test.go", `package lib_test

import (
	"fmt"
	"testing"
)

func helper(t *testing.T) {
	defer fmt.Println("helper defer")
	t.Skip("skipped in a callee")
}

func TestSkip(t *testing.T) {
	defer fmt.Println("skip defer")
	t.Cleanup(func() { fmt.Println("skip cleanup") })
	helper(t)
	fmt.Println("unreachable after skip")
}

func TestFatal(t *testing.T) {
	defer fmt.Println("fatal defer")
	t.Cleanup(func() { fmt.Println("fatal cleanup") })
	t.Fatal("fatal message")
	fmt.Println("unreachable after fatal")
}

func TestFailNowInSubtest(t *testing.T) {
	ok := t.Run("sub", func(t *testing.T) {
		t.Fatalf("sub %s", "fatal")
	})
	fmt.Println("sub ok:", ok)
	t.SkipNow()
}

func TestAfter(t *testing.T) {
	fmt.Println("after ran")
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_xtest "example.com/lib_test"
)

var tests = []testing.InternalTest{
	{"TestSkip", _xtest.TestSkip},
	{"TestFatal", _xtest.TestFatal},
	{"TestFailNowInSubtest", _xtest.TestFailNowInSubtest},
	{"TestAfter", _xtest.TestAfter},
}

var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func init() {
	testdeps.ModulePath = "example.com/lib"
	testdeps.ImportPath = "example.com/lib"
}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest}, "-test.v", "-test.timeout=30s")
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if got.status != 1 {
		t.Errorf("status = %d, want 1\nstdout:\n%s\nstderr:\n%s", got.status, got.stdout, got.stderr)
	}
	for _, want := range []string{
		"helper defer\nskip defer\nskip cleanup\n",
		"--- SKIP: TestSkip",
		"fatal defer\nfatal cleanup\n",
		"--- FAIL: TestFatal",
		"fatal message",
		"--- FAIL: TestFailNowInSubtest/sub",
		"sub ok: false\n",
		"--- FAIL: TestFailNowInSubtest ",
		"after ran\n",
		"--- PASS: TestAfter",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	for _, unwanted := range []string{"unreachable", "timed out", "panic:"} {
		if strings.Contains(got.stdout+got.stderr, unwanted) {
			t.Errorf("output contains %q", unwanted)
		}
	}
	if t.Failed() {
		t.Logf("stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)
	}
}

// TestGoSourceS374ConstraintSkipInsideTestingCallback pins the
// cmd/internal/testdir failure mode from story #1537: a dependency-owned
// constraint.Expr calls an interpreted method-value predicate, the interpreted
// test reaches t.Skip, and the dependency callback must end the native testing
// goroutine with runtime.Goexit rather than returning the private goexit marker
// as an ordinary Eval result.
//
// Sprint: #374; Story: #1537; Story-ID: 875fb3251256
func TestGoSourceS374ConstraintSkipInsideTestingCallback(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/constraintskip_test", Sources: []gosource.Source{s249Source("constraintskip_test.go", `package constraintskip_test

import (
	"fmt"
	"go/build/constraint"
	"testing"
)

type context struct{}

func (*context) match(tag string) bool {
	switch tag {
	case "amd64":
		return true
	case "goexperiment.simd":
		return false
	default:
		panic(tag)
	}
}

func TestConstraintSkip(t *testing.T) {
	line := "//go:build goexperiment.simd && amd64"
	expr, err := constraint.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	if !expr.Eval((&context{}).match) {
		t.Skip(line)
	}
	fmt.Println("unreachable after build-constraint skip")
}

func TestAfter(t *testing.T) {
	fmt.Println("after ran")
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_xtest "example.com/constraintskip_test"
)

var tests = []testing.InternalTest{
	{"TestConstraintSkip", _xtest.TestConstraintSkip},
	{"TestAfter", _xtest.TestAfter},
}

var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func init() {
	testdeps.ModulePath = "example.com/constraintskip"
	testdeps.ImportPath = "example.com/constraintskip"
}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest}, "-test.v", "-test.timeout=30s")
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if got.status != 0 {
		t.Errorf("status = %d, want 0\nstdout:\n%s\nstderr:\n%s", got.status, got.stdout, got.stderr)
	}
	for _, want := range []string{
		"--- SKIP: TestConstraintSkip",
		"//go:build goexperiment.simd && amd64",
		"after ran\n",
		"--- PASS: TestAfter",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	for _, unwanted := range []string{"unreachable", "panic:", "original callback result count mismatch"} {
		if strings.Contains(got.stdout+got.stderr, unwanted) {
			t.Errorf("output contains %q", unwanted)
		}
	}
	if t.Failed() {
		t.Logf("stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)
	}
}
