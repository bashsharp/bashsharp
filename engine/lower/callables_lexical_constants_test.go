package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func constantAssignStmt(t *testing.T, source string) *syntax.Stmt {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "constants.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Stmts) != 1 {
		t.Fatalf("want one statement, got %d", len(file.Stmts))
	}
	return file.Stmts[0]
}

func constantEmitter() *emitter {
	e := &emitter{mixedShell: true}
	e.projections.projectionPush()
	return e
}

// The measured refusal for `const x int = 1; x=42` is the engine's
// `x: cannot assign to const`, at status 1, with nothing after it running.
// Compile used to reject the same program statically with LOWER-ETYPE
// "cannot assign to x", because the assignment was claimed as a native write
// against a Go constant. It must lower as a shell region instead, so the
// refusal stays the runtime one that was reproduced.
func TestNativeScalarShellAssignmentDeclinesConstantTargets(t *testing.T) {
	e := constantEmitter()
	stmt := constantAssignStmt(t, "x=42")
	e.projections.projectionBind("x", scalarProjection())
	if !e.nativeScalarShellAssignment(stmt) {
		t.Fatal("a plain integer assignment to a variable is a native write")
	}
	constant := scalarProjection()
	constant.constant = true
	e.projections.projectionBind("x", constant)
	if e.nativeScalarShellAssignment(stmt) {
		t.Fatal("an assignment to a constant was claimed as a native write")
	}
	if !e.dynamicShell(stmt) {
		t.Fatal("an assignment to a constant did not fall through to a shell region")
	}
}

// Constant-ness rides the emitter's own scoped projections, so shadowing and
// scope exit are already modelled: an inner `var` of the same spelling is
// native storage again, and the constant returns when that scope pops.
func TestLexicalConstantAssignedFollowsProjectionScopes(t *testing.T) {
	e := constantEmitter()
	stmt := constantAssignStmt(t, "x=42")
	constant := scalarProjection()
	constant.constant = true
	e.projections.projectionBind("x", constant)
	if !e.lexicalConstantAssigned("x") {
		t.Fatal("the constant was not visible in its own scope")
	}

	e.projections.projectionPush()
	e.projections.projectionBind("x", scalarProjection())
	if e.lexicalConstantAssigned("x") {
		t.Fatal("an inner variable was reported as the outer constant")
	}
	if !e.nativeScalarShellAssignment(stmt) {
		t.Fatal("an inner variable shadowing a constant lost its native write")
	}

	e.projections.projectionPop()
	if !e.lexicalConstantAssigned("x") {
		t.Fatal("the constant did not return when its shadow went out of scope")
	}
	if e.lexicalConstantAssigned("absent") {
		t.Fatal("an unbound name was reported as constant")
	}
}

// lexicalConstants is the hook core installs in lexicalStorage. It registers a
// shadow binding by resolved identity and never takes the constant's address.
func TestLexicalConstantsRegistersShadowsByResolvedIdentity(t *testing.T) {
	source := `package generated

import rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"

type Amount int

func call(program *rt.Program) {
	const x int = 1
	const total Amount = 3
	var y int = 2
	_, _, _ = x, total, y
}
`
	got := constantRegistrations(t, source)
	for _, want := range []string{
		`rtrt.RegisterConstant(program.Bindings,"local:`, // resolved identity, not the spelling
		`,"x","int",x,rtrt.KindScalar)`,
		`,"total","Amount",total,rtrt.KindScalar)`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "&x") || strings.Contains(got, "&total") {
		t.Fatalf("the constant's address was taken:\n%s", got)
	}
	if strings.Contains(got, `"y"`) {
		t.Fatalf("a variable was registered as a constant:\n%s", got)
	}
	if strings.Count(got, "RegisterConstant") != 2 {
		t.Fatalf("want two registrations, got:\n%s", got)
	}
}

// The entry takes no program parameter: it holds the root body as a literal
// passed to Program.Run. Skipping such a declaration dropped every constant
// declared at source top level.
func TestLexicalConstantsRegistersTheRootCallbackContext(t *testing.T) {
	source := `package generated

import rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"

func Execute() (int, error) {
	p, err := rt.NewProgram()
	if err != nil {
		return 0, err
	}
	err = p.Run(func(program *rt.Program) {
		const root int = 1
		_ = root
	})
	return 0, err
}

func detached() {
	const hidden int = 9
	_ = hidden
}
`
	got := constantRegistrations(t, source)
	if !strings.Contains(got, `,"root","int",root,rtrt.KindScalar)`) {
		t.Fatalf("the root callback's constant was not registered:\n%s", got)
	}
	// A body reached with no program at all still has no boundary.
	if strings.Contains(got, `"hidden"`) {
		t.Fatalf("a constant outside any program context was registered:\n%s", got)
	}
	if strings.Count(got, "RegisterConstant") != 1 {
		t.Fatalf("want one registration, got:\n%s", got)
	}
}

func TestLexicalConstantsKeepsShadowedIdentitiesDistinct(t *testing.T) {
	source := `package generated

import rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"

func call(program *rt.Program) {
	const x int = 1
	{
		const x int = 2
		_ = x
	}
	_ = x
}
`
	got := constantRegistrations(t, source)
	if strings.Count(got, "RegisterConstant") != 2 {
		t.Fatalf("want a registration per identity, got:\n%s", got)
	}
	keys := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		_, rest, found := strings.Cut(line, `Bindings,"`)
		if !found {
			continue
		}
		key, _, _ := strings.Cut(rest, `"`)
		if keys[key] {
			t.Fatalf("two shadowed constants shared identity %q:\n%s", key, got)
		}
		keys[key] = true
	}
	if len(keys) != 2 {
		t.Fatalf("want two distinct identities, got %v", keys)
	}
}

func constantRegistrations(t *testing.T, source string) string {
	t.Helper()
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "generated.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: bridgeImporter{fallback: newModuleImporter(""), path: DefaultRuntime, cache: map[string]*types.Package{}}}
	if _, err := conf.Check("generated", fs, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	e := &emitter{prefix: "rt", options: Options{Runtime: DefaultRuntime}}
	var text strings.Builder
	for _, edit := range e.lexicalConstants(file, fs, info) {
		if edit.start != edit.end {
			t.Fatalf("registration must be an insertion, got %d..%d", edit.start, edit.end)
		}
		text.WriteString(edit.text)
	}
	return text.String()
}
