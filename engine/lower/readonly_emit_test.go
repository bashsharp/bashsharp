package lower

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

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// readonlyContextNames threads the state as an ordinary expression. Nothing
// here is a package-level variable, a thread local or a goroutine-id lookup,
// and there is no failure sink: guards unwind with their typed error.
var readonlyContextNames = ReadonlyContext{State: "__test_state"}

func newReadonlyEmitter() *emitter {
	return &emitter{
		prefix:          "__test_",
		scopes:          []map[string]bool{{}},
		funcs:           map[string]bool{},
		imports:         map[string]string{},
		dotNames:        map[string]bool{},
		typeNames:       map[string]bool{},
		globals:         map[string]bool{},
		functionGlobals: map[string]bool{},
	}
}

func readonlySource(node syntax.Node) string {
	var out strings.Builder
	_ = syntax.NewPrinter().Print(&out, node)
	return out.String()
}

// readonlyEmit is the harness dispatcher. It is deliberately minimal: it wires
// the guard helpers to the nodes this slice owns and delegates everything else
// to the ordinary emitter. It is not the compiler's statement dispatcher and
// certifies nothing about it.
type readonlyEmit struct {
	e     *emitter
	kinds []string
	used  int
	decls strings.Builder
	t     *testing.T
}

func (h *readonlyEmit) nextKind() string {
	if h.used >= len(h.kinds) {
		h.t.Fatalf("fixture supplies %d mutation kinds, dispatcher needs more", len(h.kinds))
	}
	kind := h.kinds[h.used]
	h.used++
	return kind
}

func (h *readonlyEmit) stmts(stmts []*syntax.Stmt) (string, error) {
	var out strings.Builder
	for _, stmt := range stmts {
		var text string
		var err error
		switch node := stmt.Cmd.(type) {
		case *syntax.BashPPImport:
			err = h.e.importDecl(node)
		case *syntax.BashPPFuncDecl:
			err = h.function(node)
		case *syntax.DeclClause:
			text, err = h.e.readonlyDeclare(node, readonlyContextNames)
		case *syntax.BashPPAssign:
			text, err = h.assign(node)
		case *syntax.Subshell:
			// A subshell is lexical scaffolding here: this fixture's guard fires
			// before any mutation, so the block form is observationally identical.
			// Real subshell lowering belongs to the compiler owner.
			var inner string
			inner, err = h.stmts(node.Stmts)
			text = "{\n" + inner + "}"
		case *syntax.BashPPShortDecl:
			text, err = h.shortDecl(node)
		case *syntax.BashPPCall:
			if len(node.Fun) == 1 && h.e.funcs[node.Fun[0].Value] && len(node.Args) == 0 {
				// The state is threaded as an ordinary parameter, exactly as the
				// contract requires; nothing is reachable through a global.
				text = h.e.goName(node.Fun[0].Value) + "(" + readonlyContextNames.State + ")"
				break
			}
			if readonlyGuardedBuiltin(node) {
				var setup, call string
				setup, call, err = h.builtin(node)
				text = setup + call
				break
			}
			text, err = h.e.command(stmt.Cmd)
		default:
			text, err = h.e.command(stmt.Cmd)
		}
		if err != nil {
			return "", err
		}
		if text != "" {
			out.WriteString(text + "\n")
		}
	}
	return out.String(), nil
}

// function keeps the parameterless declarations these fixtures use. Full
// callable lowering is the compiler owner's; the harness only needs a body
// scope so `make` and the guards can be exercised where the parser accepts them.
func (h *readonlyEmit) function(node *syntax.BashPPFuncDecl) error {
	if node.Name == nil || node.Body == nil || len(node.Params) > 0 || len(node.Results) > 0 {
		return errors.New("harness supports parameterless function declarations only")
	}
	h.e.funcs[node.Name.Value] = true
	h.e.push()
	body, err := h.stmts(node.Body.Stmts)
	h.e.pop()
	if err != nil {
		return err
	}
	h.decls.WriteString("func " + h.e.goName(node.Name.Value) + "(" + readonlyContextNames.State +
		" *__test_rt.ReadonlyState) {\n" + body + "}\n")
	return nil
}

func (h *readonlyEmit) assign(node *syntax.BashPPAssign) (string, error) {
	if node.TargetExpr != nil {
		value, err := h.e.expr(node.ValueExpr)
		if err != nil {
			return "", err
		}
		return h.e.readonlyMutation(node.TargetExpr, readonlyContextNames, ReadonlyMutation{Kind: h.nextKind(), Value: value})
	}
	// Whole-binding rebinding still arrives as words. The compiler owner must
	// supply the emitted value; the harness re-prints the positioned word, which
	// is already valid Go for these fixtures.
	name := readonlySource(node.Target)
	return h.e.readonlyRebind(node, readonlyContextNames, name, readonlySource(node.Value))
}

func (h *readonlyEmit) shortDecl(node *syntax.BashPPShortDecl) (string, error) {
	if node.Call != nil && (len(node.Call.Fun) > 1 || readonlyGuardedBuiltin(node.Call)) {
		var rhs, setup string
		var err error
		if readonlyGuardedBuiltin(node.Call) {
			setup, rhs, err = h.builtin(node.Call)
		} else {
			// Imported constructors are the compiler's projection work; the
			// harness emits the ordinary native call so the fixture can run.
			rhs, err = h.e.call(node.Call)
		}
		if err != nil {
			return "", err
		}
		declared := names(node.Lhs)
		operator := " := "
		if strings.Trim(strings.Join(declared, ""), "_") == "" {
			operator = " = "
		}
		for _, name := range declared {
			h.e.bind(name)
		}
		return setup + strings.Join(declared, ", ") + operator + rhs + h.e.unused(declared), nil
	}
	return h.e.command(node)
}

func readonlyGuardedBuiltin(call *syntax.BashPPCall) bool {
	if call == nil || len(call.Fun) != 1 {
		return false
	}
	switch call.Fun[0].Value {
	case "append", "copy", "delete", "clear":
		return true
	}
	return false
}

func (h *readonlyEmit) builtin(call *syntax.BashPPCall) (string, string, error) {
	if len(call.Args) == 0 {
		return "", "", errors.New("collection builtin without a target")
	}
	target, err := h.e.argument(call.Args[0])
	if err != nil {
		return "", "", err
	}
	var rest []string
	for _, arg := range call.Args[1:] {
		value, err := h.e.argument(arg)
		if err != nil {
			return "", "", err
		}
		rest = append(rest, value)
	}
	return h.e.readonlyBuiltin(call, readonlyContextNames, ReadonlyBuiltin{
		Name: call.Fun[0].Value, Root: readonlySource(call.Args[0]), Target: target, Args: rest,
		Spread: call.Ellipsis.IsValid(),
	})
}

// readonlyProgram assembles the ordinary Go artifact around the emitted body.
// The helpers themselves never build a program wrapper; __test_boundary is this
// test's stand-in for the runtime Program helper's Run, which catches the same
// typed unwind, reports it once and exits with the error's own status.
func readonlyProgram(e *emitter, declarations, body string) string {
	return `package main

import __test_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"
import __test_fmt "fmt"
import __test_os "os"
` + e.importLines() + `
func __test_boundary(body func()) (status int) {
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		err, ok := value.(error)
		if !ok {
			panic(value)
		}
		__test_fmt.Fprintln(__test_rt.Stderr, err)
		status = 1
		if exit, ok := err.(interface{ ExitStatus() int }); ok {
			status = exit.ExitStatus()
		}
	}()
	body()
	return 0
}

` + declarations + `
func main() {
	__test_state := &__test_rt.ReadonlyState{}
	_ = __test_state
	__test_os.Exit(__test_boundary(func() {
` + body + `}))
}
`
}

type readonlyRun struct {
	stdout, stderr string
	status         int
}

func readonlyBuildAndRun(t *testing.T, ctx context.Context, program string) readonlyRun {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	_, testFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(testFile)))
	module := "module readonlyartifact\ngo " + strings.TrimPrefix(runtime.Version(), "go") +
		"\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "program")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "generated.go")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build:%v\n%s\n%s", err, out, program)
	}
	// The compiled artifact must be an ordinary Go binary: no source, no shell
	// and no Go tool are reachable while it runs.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary)
	command.Dir = t.TempDir()
	command.Env = []string{"PATH="}
	var out, errs bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errs
	err := command.Run()
	status := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("artifact:%v\n%s", err, errs.String())
		}
		status = exit.ExitCode()
	}
	return readonlyRun{stdout: out.String(), stderr: errs.String(), status: status}
}

func readonlyInterpret(t *testing.T, ctx context.Context, file *syntax.File) readonlyRun {
	t.Helper()
	var out, errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errs),
		interp.Dir(t.TempDir()), interp.Env(expand.ListEnviron("PATH=")))
	if err != nil {
		t.Fatal(err)
	}
	status := 0
	if err := runner.Run(ctx, file); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatalf("interpreter:%v\n%s", err, errs.String())
		}
		status = int(exit)
	}
	return readonlyRun{stdout: out.String(), stderr: errs.String(), status: status}
}

type readonlyFixture struct {
	name, src string
	kinds     []string
}

func readonlyCompare(t *testing.T, fixture readonlyFixture) readonlyRun {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(fixture.src), "readonly.bpp")
	if err != nil {
		t.Fatal(err)
	}
	e := newReadonlyEmitter()
	harness := &readonlyEmit{e: e, kinds: fixture.kinds, t: t}
	body, err := harness.stmts(file.Stmts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	compiled := readonlyBuildAndRun(t, ctx, readonlyProgram(e, harness.decls.String(), body))
	interpreted := readonlyInterpret(t, ctx, file)
	if compiled != interpreted {
		t.Fatalf("compiled=%+v interpreted=%+v", compiled, interpreted)
	}
	return compiled
}

// TestReadonlyEmitterNegativeArtifactParity covers the seven public readonly
// negatives. Each one must compile as ordinary Go and fail at runtime with the
// interpreter's exact diagnostic and exit status, not at compile time.
func TestReadonlyEmitterNegativeArtifactParity(t *testing.T) {
	fixtures := []readonlyFixture{
		{"root", `cfg := map[string]int{"port": 80}
readonly cfg
cfg = map[string]int{"port": 443}
`, nil},
		{"map", `cfg := map[string]map[string]int{"nested": {"port": 80}}
readonly cfg
cfg["nested"]["port"] = 443
`, []string{"map"}},
		{"slice", `cfg := map[string][]int{"ports": {80, 443}}
readonly cfg
cfg["ports"][0] = 8080
`, []string{"slice"}},
		{"struct", `type Config struct { Name string }
cfg := Config{Name: "prod"}
readonly cfg
cfg.Name = "dev"
`, []string{"field"}},
		{"alias", `cfg := map[string][]int{"ports": {80, 443}}
alias := cfg
readonly cfg
alias["ports"][0] = 8080
`, []string{"slice"}},
		{"subshell", `cfg := map[string][]int{"ports": {80, 443}}
readonly cfg
(
 cfg["ports"][0] = 8080
)
`, []string{"slice"}},
		{"imported", `import "net/url"
endpoint, _ := url.Parse("https://example.test/original")
readonly endpoint
endpoint.Host = "changed.test"
`, []string{"field"}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			if run := readonlyCompare(t, fixture); run.status != 2 {
				t.Fatalf("status=%d stderr=%q", run.status, run.stderr)
			}
		})
	}
}

// TestReadonlyEmitterGuardPrecedesPathEvaluation pins the interpreter's timing.
// The guard resolves the root binding, so a readonly mutation reports the
// readonly diagnostic even when evaluating the path would first panic on a
// bounds violation or a nil map — and the diagnostic text distinguishes a
// nested field from an indexed path exactly as the interpreter does.
func TestReadonlyEmitterGuardPrecedesPathEvaluation(t *testing.T) {
	fixtures := []readonlyFixture{
		{"out of range slice path", `type Config struct { Ports []int }
cfg := Config{Ports: []int{80}}
readonly cfg
cfg.Ports[5] = 1
`, []string{"slice"}},
		{"nil map", `var m map[string]int
readonly m
m["a"] = 1
`, []string{"map"}},
		{"array element", `var a [3]int
readonly a
a[1] = 5
`, []string{"slice"}},
		{"struct map field", `type Config struct { Labels map[string]string }
cfg := Config{Labels: map[string]string{"a": "b"}}
readonly cfg
cfg.Labels["a"] = "c"
`, []string{"map"}},
		{"nested field names last selector", `type Meta struct { Name string }
type Config struct { Meta Meta }
cfg := Config{Meta: Meta{Name: "prod"}}
readonly cfg
cfg.Meta.Name = "dev"
`, []string{"field"}},
		{"slice alias element", `s := []int{1, 2}
readonly s
alias := s
alias[0] = 9
`, []string{"slice"}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			if run := readonlyCompare(t, fixture); run.status != 2 {
				t.Fatalf("status=%d stderr=%q", run.status, run.stderr)
			}
		})
	}
}

// TestReadonlyEmitterBuiltinArtifactParity certifies the collection builtins
// against the interpreter's exact stdout, stderr and status, wording included.
func TestReadonlyEmitterBuiltinArtifactParity(t *testing.T) {
	fixtures := []readonlyFixture{
		{"clear", "s := []int{1}\nreadonly s\nclear(s)\n", nil},
		{"delete", "m := map[string]int{\"a\": 1}\nreadonly m\ndelete(m, \"a\")\n", nil},
		{"delete through alias", "m := map[string]int{\"a\": 1}\nreadonly m\nn := m\ndelete(n, \"a\")\n", nil},
		{"copy", "s := []int{1, 2}\no := []int{3, 4}\nreadonly s\ncopy(s, o)\n", nil},
		{"append in place", `func main() {
 s := make([]int, 0, 1)
 readonly s
 t := append(s, 1)
}
main()
`, nil},
		{"append spread in place", `func main() {
 s := make([]int, 0, 2)
 more := []int{1}
 readonly s
 t := append(s, more...)
}
main()
`, nil},
		// A growing append allocates, so neither implementation reports a
		// mutation of the readonly backing array.
		{"append grows", "s := []int{1}\nreadonly s\nt := append(s, 2)\nprintln(t[1])\n", nil},
		{"append spread grows", `func main() {
 s := []int{1}
 more := []int{2}
 readonly s
 t := append(s, more...)
 println(t[1])
}
main()
`, nil},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			readonlyCompare(t, fixture)
		})
	}
}

// TestReadonlyEmitterGuardsWithoutHoisting pins the emitted shape: the guard
// resolves the root binding and its own value, needs no part of the path, and
// leaves the update as one ordinary Go statement whose operands are evaluated
// once, in Go's order.
func TestReadonlyEmitterGuardsWithoutHoisting(t *testing.T) {
	const src = `cfg := map[string][]int{"ports": {80, 443}}
readonly cfg
cfg["ports"][0] = 8080
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "readonly.bpp")
	if err != nil {
		t.Fatal(err)
	}
	e := newReadonlyEmitter()
	harness := &readonlyEmit{e: e, kinds: []string{"slice"}, t: t}
	body, err := harness.stmts(file.Stmts)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`__test_rt.MustReadonly(__test_state.Mark("cfg", &cfg))`,
		"__test_rt.MustReadonly(__test_state.CheckMutation(\"cfg\", &cfg, cfg, \"[\\\"ports\\\"][0]\", \"slice\"))\ncfg[\"ports\"][0] = 8080",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "readonlyIndex") || strings.Contains(body, "readonlyContainer") {
		t.Fatalf("mutation hoisted an operand:\n%s", body)
	}
	if !e.bridge {
		t.Fatal("guard emission must record the runtime import")
	}
}

// TestReadonlyBuiltinCapturesOperandsBeforeGuard pins the builtin order: the
// interpreter evaluates a builtin's arguments before it checks the target, so
// every effectful operand is captured first. Constant operands stay in the call,
// because hoisting one would change the type it takes.
func TestReadonlyBuiltinCapturesOperandsBeforeGuard(t *testing.T) {
	e := newReadonlyEmitter()
	e.bind("m")
	e.bind("key")
	e.funcs["key"] = true
	setup, call, err := e.readonlyBuiltin(nil, readonlyContextNames, ReadonlyBuiltin{
		Name: "delete", Root: "m", Target: "m", Args: []string{"key()"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "__test_readonlyTarget := m\n__test_readonlyArg0 := key()\n" +
		`__test_rt.MustReadonly(__test_state.CheckBuiltin(&m, __test_readonlyTarget, "delete"))` + "\n"
	if setup != want {
		t.Fatalf("setup=%q want=%q", setup, want)
	}
	if call != "delete(__test_readonlyTarget, __test_readonlyArg0)" {
		t.Fatalf("call=%q", call)
	}
	// An untyped constant must not be hoisted without a type, and must be
	// hoisted with the type the compiler supplies.
	setup, call, err = e.readonlyBuiltin(nil, readonlyContextNames, ReadonlyBuiltin{
		Name: "delete", Root: "m", Target: "m", Args: []string{"1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(setup, "readonlyArg0") || call != "delete(__test_readonlyTarget, 1)" {
		t.Fatalf("constant hoisted: setup=%q call=%q", setup, call)
	}
	setup, call, err = e.readonlyBuiltin(nil, readonlyContextNames, ReadonlyBuiltin{
		Name: "delete", Root: "m", Target: "m", Args: []string{"1"}, ArgTypes: []string{"float64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(setup, "var __test_readonlyArg0 float64 = 1\n") ||
		strings.Index(setup, "readonlyArg0 float64") > strings.Index(setup, "CheckBuiltin") {
		t.Fatalf("typed capture must precede the guard: %q", setup)
	}
	if call != "delete(__test_readonlyTarget, __test_readonlyArg0)" {
		t.Fatalf("call=%q", call)
	}
}

// TestReadonlyEmitterSpreadAppendGuard pins the runtime-length form: the guard
// only fires when the appended elements fit in the existing capacity.
func TestReadonlyEmitterSpreadAppendGuard(t *testing.T) {
	e := newReadonlyEmitter()
	e.bind("s")
	e.bind("more")
	setup, call, err := e.readonlyBuiltin(nil, readonlyContextNames, ReadonlyBuiltin{
		Name: "append", Root: "s", Target: "s", Args: []string{"more"}, Spread: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"__test_readonlyTarget := s\n",
		"if len(more) > 0 && len(__test_readonlyTarget)+len(more) <= cap(__test_readonlyTarget) {",
		`__test_rt.MustReadonly(__test_state.CheckBuiltin(&s, __test_readonlyTarget, "append"))`,
	} {
		if !strings.Contains(setup, want) {
			t.Fatalf("missing %q in:\n%s", want, setup)
		}
	}
	if call != "append(__test_readonlyTarget, more...)" {
		t.Fatalf("call=%q", call)
	}
	if _, _, err := e.readonlyBuiltin(nil, readonlyContextNames, ReadonlyBuiltin{
		Name: "copy", Root: "s", Target: "s", Args: []string{"more"}, Spread: true,
	}); err == nil {
		t.Fatal("spread copy accepted")
	}
}

func TestReadonlyEmitterRequiresExplicitState(t *testing.T) {
	e := newReadonlyEmitter()
	e.bind("cfg")
	empty := ReadonlyContext{}
	if _, err := e.readonlyRebind(nil, empty, "cfg", "1"); err == nil {
		t.Fatal("rebinding accepted without state")
	}
	if _, _, err := e.readonlyBuiltin(nil, empty, ReadonlyBuiltin{Name: "clear", Root: "cfg", Target: "cfg"}); err == nil {
		t.Fatal("builtin accepted without state")
	}
	if e.bridge {
		t.Fatal("a refused guard must not record the runtime import")
	}
}

func TestReadonlyEmitterRejectsIllFormedSites(t *testing.T) {
	e := newReadonlyEmitter()
	e.bind("cfg")
	ident := &syntax.BashPPIdent{Name: &syntax.Lit{Value: "cfg"}}
	field := &syntax.BashPPSelectorExpr{X: ident, Sel: &syntax.Lit{Value: "Name"}}
	index := &syntax.BashPPIndexExpr{X: ident, Index: &syntax.BashPPBasicLit{Value: &syntax.Lit{Value: "0"}}}
	cases := []struct {
		name   string
		target syntax.BashPPExpr
		m      ReadonlyMutation
	}{
		{"unknown kind", field, ReadonlyMutation{Kind: "object", Value: "1"}},
		{"missing value", field, ReadonlyMutation{Kind: "field"}},
		{"whole binding", ident, ReadonlyMutation{Kind: "field", Value: "1"}},
		{"field kind for index path", index, ReadonlyMutation{Kind: "field", Value: "1"}},
		{"index kind for field path", field, ReadonlyMutation{Kind: "slice", Value: "1"}},
	}
	for _, tc := range cases {
		if _, err := e.readonlyMutation(tc.target, readonlyContextNames, tc.m); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
	// An array element is an indexed path the interpreter calls a slice path;
	// routing it must work, not be refused for lacking a final field.
	if _, err := e.readonlyMutation(index, readonlyContextNames, ReadonlyMutation{Kind: "slice", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	unknown := &syntax.BashPPSelectorExpr{X: &syntax.BashPPIdent{Name: &syntax.Lit{Value: "missing"}}, Sel: &syntax.Lit{Value: "Name"}}
	if _, err := e.readonlyMutation(unknown, readonlyContextNames, ReadonlyMutation{Kind: "field", Value: "1"}); err == nil {
		t.Fatal("undefined root accepted")
	}
	for _, b := range []ReadonlyBuiltin{
		{Name: "delete", Root: "cfg", Target: "cfg"},
		{Name: "sort", Root: "cfg", Target: "cfg"},
		{Name: "clear", Root: "cfg", Target: "cfg", Args: []string{"1"}},
		{Name: "clear", Root: "missing", Target: "missing"},
		{Name: "copy", Root: "cfg", Target: "cfg", Args: []string{"1"}, ArgTypes: []string{"int", "int"}},
	} {
		if _, _, err := e.readonlyBuiltin(nil, readonlyContextNames, b); err == nil {
			t.Fatalf("%+v accepted", b)
		}
	}
	declare := &syntax.DeclClause{Variant: &syntax.Lit{Value: "export"}, Args: []*syntax.Assign{{Name: &syntax.Lit{Value: "cfg"}}}}
	if _, err := e.readonlyDeclare(declare, readonlyContextNames); err == nil {
		t.Fatal("export marked as readonly")
	}
}

func TestReadonlyEmitterOperandPurity(t *testing.T) {
	e := newReadonlyEmitter()
	e.imports["math"] = "math"
	e.bind("x")
	pure := []string{"1", `"a"`, "x", "-1", "(1 + 2)", "math.MaxInt64", "1 << 40"}
	impure := []string{"f()", "m[k]", "*p", "x.Field", "<-ch", "[]int{1}", "x[1:]", "!("}
	for _, text := range pure {
		if !e.readonlyPure(text) {
			t.Fatalf("%q must stay in the call", text)
		}
	}
	for _, text := range impure {
		if e.readonlyPure(text) {
			t.Fatalf("%q must be captured before the guard", text)
		}
	}
}
