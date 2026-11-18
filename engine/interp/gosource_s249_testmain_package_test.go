package interp_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const s249PackageTestMainIdentity = "example.com/lib.test"

type s249GoSourceOutcome struct {
	stdout string
	stderr string
	status int
}

func runS249PackageTestMain(t *testing.T, sources []gosource.Source, packages []gosource.PackageSpec, args ...string) (s249GoSourceOutcome, error) {
	t.Helper()
	program, err := gosource.Load(sources, gosource.Options{
		RunMain: true, ImportPath: s249PackageTestMainIdentity, TestMain: true, Packages: packages,
	})
	if err != nil {
		return s249GoSourceOutcome{}, err
	}
	var stdout, stderr bytes.Buffer
	options := []interp.RunnerOption{
		interp.Lang(syntax.LangBashPP),
		interp.StdIO(nil, &stdout, &stderr),
		interp.GoSourceIdentity(s249PackageTestMainIdentity, true),
	}
	if len(args) > 0 {
		options = append(options, interp.Params(append([]string{"--"}, args...)...))
	}
	runner, err := interp.New(options...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	outcome := s249GoSourceOutcome{stdout: stdout.String(), stderr: stderr.String()}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		outcome.status = int(status)
		err = nil
	}
	return outcome, err
}

func s249Source(name, data string) gosource.Source {
	return gosource.Source{Name: name, Data: []byte(data)}
}

// cmd/go's package test harness keeps the descriptor slices in _testmain.go
// but points their function fields at a separately compiled/imported test
// package. The authenticated test-main fact must let testing.MainStart retain
// those exact descriptor slices instead of receiving copied descriptors, while
// the test bodies still execute under the interpreter.
func TestGoSourceS249PackageTestMainTransfersImportedDescriptors(t *testing.T) {
	lib := gosource.PackageSpec{Path: "example.com/lib", Sources: []gosource.Source{s249Source("lib.go", `package lib

const Name = "lib"
`)}}
	xtest := gosource.PackageSpec{Path: "example.com/lib_test", Sources: []gosource.Source{s249Source("lib_test.go", `package lib_test

import (
	"fmt"
	"testing"
)

func TestAlpha(t *testing.T) {
	fmt.Println("alpha from imported test")
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_ "example.com/lib"
	_xtest "example.com/lib_test"
)

var tests = []testing.InternalTest{
	{"TestAlpha", _xtest.TestAlpha},
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

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{lib, xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	want := s249GoSourceOutcome{stdout: "alpha from imported test\nPASS\n"}
	if got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// A native testing.M retains mapped-package callbacks after registration.
// Their package variables are shared Go storage even though they are not
// lexical closure captures: typed zero/non-zero values and indexed aggregate
// reads must remain available, and mutations from parallel testing frames must
// be visible to the test main after M.Run. The package channel supplies the
// same synchronization Go source requested, including under the host race
// detector.
func TestGoSourceTestingCallbacksShareMappedPackageGlobals(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/globals_test", Sources: []gosource.Source{s249Source("globals_test.go", `package globals_test

import (
	"fmt"
	"go/token"
	"testing"
)

var zero token.Pos
var nonzero token.Pos = 1
var panicArray = [...]string{"array-zero", "array-one"}
var panicSlice = []string{"slice-zero", "slice-one"}
var guard = make(chan struct{}, 1)
var callbackCount int

func init() { guard <- struct{}{} }

func increment() {
	<-guard
	callbackCount++
	guard <- struct{}{}
}

func CallbackCount() int {
	<-guard
	n := callbackCount
	guard <- struct{}{}
	return n
}

func TestPackageGlobals(t *testing.T) {
	local := "lexical-local"
	t.Run("typed-and-indexed", func(t *testing.T) {
		got := panicArray[zero] + "|" + panicSlice[nonzero] + "|" + local
		if got != "array-zero|slice-one|lexical-local" {
			panic(panicArray[zero] + ":" + panicSlice[nonzero])
		}
		increment()
	})
	for i := 0; i < 4; i++ {
		t.Run(fmt.Sprintf("parallel-%d", i), func(t *testing.T) {
			t.Parallel()
			increment()
		})
	}
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"
	_xtest "example.com/globals_test"
)

var tests = []testing.InternalTest{{"TestPackageGlobals", _xtest.TestPackageGlobals}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	code := m.Run()
	if got := _xtest.CallbackCount(); got != 5 {
		panic(fmt.Sprintf("callback count after M.Run = %d, want 5", got))
	}
	os.Exit(code)
}
`)

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if want := (s249GoSourceOutcome{stdout: "PASS\n"}); got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// A testing callback receives values decoded from the dependency bridge through
// callback-frame storage, not through the original frame's locals. When a
// value receiver is copied from a range variable over a named-scalar slice, the
// receiver copy must keep scalar metadata as a scalar cell; otherwise the
// method body sees its receiver as an object and rejects arithmetic/conversion
// reads.
func TestGoSourceS319TestingCallbackNamedScalarValueReceiver(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/parampropbits_test", Sources: []gosource.Source{s249Source("parampropbits_test.go", `package parampropbits_test

import (
	"encoding/json"
	"fmt"
	"testing"
)

type propBits uint32

func (i propBits) String() string { return fmt.Sprintf("<%d>", uint64(i)) }

func propBitsToString[T interface{ String() string }](sl []T) string {
	var out string
	for i, f := range sl {
		out += fmt.Sprintf("%d: %s\n", i, f.String())
	}
	return out
}

func TestFuncProperties(t *testing.T) {
	var decoded struct {
		Flags []propBits
	}
	if err := json.Unmarshal([]byte("{\"Flags\":[42]}"), &decoded); err != nil {
		t.Fatal(err)
	}
	if got := propBitsToString(decoded.Flags); got != "0: <42>\n" {
		t.Fatalf("propBits string = %q", got)
	}
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"
	_xtest "example.com/parampropbits_test"
)

var tests = []testing.InternalTest{{"TestFuncProperties", _xtest.TestFuncProperties}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if want := (s249GoSourceOutcome{stdout: "PASS\n"}); got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// A mapped test package executes its body in the interpreter even though the
// generated test main reaches it through testing's native descriptor wrapper.
// Interface results must survive both the native call and the interpreted
// helper's return boundary.
func TestGoSourceS249PackageTestMainKeepsInterfaceReturn(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/lib_test", Sources: []gosource.Source{s249Source("lib_test.go", `package lib_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	. "go/types"
	"testing"
)


func consume(_ *Package, err error) {
	if _, ok := err.(Error); !ok {
		panic("not a types.Error")
	}
}

func TestInterfaceReturn(t *testing.T) {
	t.Run("nested", func(t *testing.T) {
		fset := token.NewFileSet()
		file, _ := parser.ParseFile(fset, "x.go", "package x; var _ = missing", 0)
		consume(new(Config).Check("x", fset, []*ast.File{file}, nil))
	})
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"
	_xtest "example.com/lib_test"
)

var tests = []testing.InternalTest{{"TestInterfaceReturn", _xtest.TestInterfaceReturn}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if want := (s249GoSourceOutcome{stdout: "PASS\n"}); got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// The upstream internal/types/errors package test shape: inside a t.Run
// callback, an interpreted helper re-assigns the dot-imported
// (*Config).Check error result into its existing `err error` and returns it.
// The caller compares that error with nil, asserts it to the dot-imported
// go/types.Error struct, and reads an unexported field through reflect.
func TestGoSourceS249PackageTestMainDotImportedMethodError(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/lib_test", Sources: []gosource.Source{s249Source("lib_test.go", `package lib_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	. "go/types"
	"reflect"
	"strings"
	"testing"
)

func walkCodes(t *testing.T, f func(string, int)) {
	t.Helper()
	f("MissingName", 1)
}

func readCode(err Error) int {
	v := reflect.ValueOf(err)
	return int(v.FieldByName("go116code").Int())
}

func checkExample(t *testing.T, example string) error {
	t.Helper()
	fset := token.NewFileSet()
	if !strings.HasPrefix(example, "package") {
		example = "package p\n\n" + example
	}
	file, err := parser.ParseFile(fset, "example.go", example, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	conf := Config{
		FakeImportC: true,
		Importer:    importer.Default(),
	}
	_, err = conf.Check("example", fset, []*ast.File{file}, nil)
	return err
}

func TestErrorCodeExamples(t *testing.T) {
	walkCodes(t, func(name string, value int) {
		t.Run(name, func(t *testing.T) {
			examples := []string{"", "var _ = missing"}
			for i := 1; i < len(examples); i++ {
				example := strings.TrimSpace(examples[i])
				err := checkExample(t, example)
				if err == nil {
					t.Fatalf("no error in example #%d", i)
				}
				typerr, ok := err.(Error)
				if !ok {
					t.Fatalf("not a types.Error: %v", err)
				}
				if got := readCode(typerr); got <= 0 {
					t.Errorf("%s: example #%d returned code %d (%s), want > 0", name, i, got, err)
				}
			}
		})
	})
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"
	_xtest "example.com/lib_test"
)

var tests = []testing.InternalTest{{"TestErrorCodeExamples", _xtest.TestErrorCodeExamples}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if want := (s249GoSourceOutcome{stdout: "PASS\n"}); got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// The upstream internal/types/errors package test's actual failing shape:
// the external test package ranges over a dependency-owned interface slice
// (`file.Decls`, a native []ast.Decl) and shadows the loop variable through a
// comma-ok type assertion on it (`decl, ok := decl.(*ast.GenDecl)`), then
// again over the element's own interface slice (`spec.(*ast.ValueSpec)`),
// before its callback reaches t.Run and asserts the dot-imported Error. Every
// element of an interface-typed native sequence must bind as an interface
// value, not as a bare handle that merely spells the interface type.
func TestGoSourceS249PackageTestMainRangesNativeInterfaceElements(t *testing.T) {
	lib := gosource.PackageSpec{Path: "example.com/lib", Sources: []gosource.Source{s249Source("lib.go", `package lib

type Code int

const (
	// _ is unused.
	_ Code = iota

	// Alpha is the first code.
	//
	// Example:
	//  var _ = missing
	Alpha
)
`)}}
	xtest := gosource.PackageSpec{Path: "example.com/lib_test", Sources: []gosource.Source{s249Source("lib_test.go", `package lib_test

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	. "go/types"
)

const codesSource = "package lib\n\ntype Code int\n\nconst (\n\t// _ is unused.\n\t_ Code = iota\n\n\t// Alpha is the first code.\n\t//\n\t// Example:\n\t//  var _ = missing\n\tAlpha\n)\n"

func TestCodeExamples(t *testing.T) {
	seen := 0
	walkCodes(t, func(name string, value int, spec *ast.ValueSpec) {
		t.Run(name, func(t *testing.T) {
			examples := strings.Split(spec.Doc.Text(), "Example:")
			for i := 1; i < len(examples); i++ {
				err := checkExample(t, strings.TrimSpace(examples[i]))
				if err == nil {
					t.Fatalf("no error in example #%d", i)
				}
				typerr, ok := err.(Error)
				if !ok {
					t.Fatalf("not a types.Error: %v", err)
				}
				if typerr.Msg == "" || value != 1 {
					t.Errorf("%s: example #%d gave %q for code %d", name, i, typerr.Msg, value)
				}
				seen++
			}
		})
	})
	if seen != 1 {
		t.Fatalf("checked %d examples, want 1", seen)
	}
}

func walkCodes(t *testing.T, f func(string, int, *ast.ValueSpec)) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "codes.go", codesSource, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	conf := Config{}
	info := &Info{
		Types: make(map[ast.Expr]TypeAndValue),
		Defs:  make(map[*ast.Ident]Object),
		Uses:  make(map[*ast.Ident]Object),
	}
	_, err = conf.Check("lib", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		decl, ok := decl.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			continue
		}
		for _, spec := range decl.Specs {
			spec, ok := spec.(*ast.ValueSpec)
			if !ok || len(spec.Names) == 0 {
				continue
			}
			obj := info.ObjectOf(spec.Names[0])
			if named, ok := obj.Type().(*Named); ok && named.Obj().Name() == "Code" {
				codename := spec.Names[0].Name
				value := int(constant.Val(obj.(*Const).Val()).(int64))
				f(codename, value, spec)
			}
		}
	}
}

func checkExample(t *testing.T, example string) error {
	t.Helper()
	fset := token.NewFileSet()
	if !strings.HasPrefix(example, "package") {
		example = "package p\n\n" + example
	}
	file, err := parser.ParseFile(fset, "example.go", example, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	conf := Config{FakeImportC: true}
	_, err = conf.Check("example", fset, []*ast.File{file}, nil)
	return err
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_ "example.com/lib"
	_xtest "example.com/lib_test"
)

var tests = []testing.InternalTest{{"TestCodeExamples", _xtest.TestCodeExamples}}
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

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{lib, xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	if want := (s249GoSourceOutcome{stdout: "PASS\n"}); got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

func TestGoSourceS249PackageTestMainRequiresAuthenticatedFact(t *testing.T) {
	driver := s249Source("_testmain.go", `package main

import "testing/internal/testdeps"

func main() { _ = testdeps.TestDeps{} }
`)
	_, err := gosource.Load([]gosource.Source{driver}, gosource.Options{
		RunMain: true, ImportPath: "example.com/lib.test",
	})
	if err == nil || !strings.Contains(err.Error(), "testing/internal/testdeps") {
		t.Fatalf("Load err = %v, want unauthenticated internal-package refusal", err)
	}
}
