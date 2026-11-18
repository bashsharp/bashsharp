package gosource

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// spellingShadowed decides, for every typed-constant reference lowered from a
// Go source package, whether the leading identifier of a type spelling is
// shadowed by a local declaration at that position. It used to call
// types.Scope.Innermost(pos) — an O(scope-depth × siblings-per-level) descent
// of the file scope — once per reference. On constant-saturated generated code
// such as cmd/compile/internal/ssa (a file holds ~1000 top-level function
// scopes and tens of thousands of typed Op-constant references) that is
// O(references × top-level-decls): the interpreted ssa package start burned
// >10 min at ~100% CPU before the first test ever ran.
//
// The fix skips the descent whenever the spelling head is not declared in any
// scope inside the file — the overwhelmingly common case — because then the
// tree walk contributes nothing and a file-scope lookup gives the identical
// object. These tests pin the equivalence (the fast path must agree with the
// Innermost path in every case) and guard the startup cost from regressing.

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
	"time"
)

// checkFileForShadow type-checks a single self-contained source file and
// returns the pieces spellingShadowed needs: the file AST, the package, and a
// populated types.Info whose Scopes mirror what the real lowering pipeline
// feeds the converter.
func checkFileForShadow(t *testing.T, src string) (*ast.File, *types.Package, *types.Info) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "shadow.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := newTypeInfo()
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("type check: %v", err)
	}
	return file, pkg, info
}

// innermostShadowed is the pre-fix reference implementation: it always descends
// with types.Scope.Innermost. The optimized spellingShadowed must agree with it
// for every (spelling, pos, want) the lowering can ask about.
func innermostShadowed(info *types.Info, file *ast.File, spelling string, pos token.Pos, want types.Object) bool {
	head := spelling
	end := strings.IndexFunc(head, func(r rune) bool {
		return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	if end >= 0 {
		head = head[:end]
	}
	scope := info.Scopes[file]
	if scope == nil {
		return false
	}
	inner := scope.Innermost(pos)
	if inner == nil {
		return false
	}
	_, obj := inner.LookupParent(head, pos)
	if obj == nil {
		return false
	}
	if want == nil {
		_, isType := obj.(*types.TypeName)
		return !isType
	}
	return obj != want
}

// stmtAfter returns a position guaranteed to be inside the body of the named
// top-level function and after any local declarations there.
func lastStmtPos(t *testing.T, file *ast.File, fn string) token.Pos {
	t.Helper()
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn || fd.Body == nil || len(fd.Body.List) == 0 {
			continue
		}
		return fd.Body.List[len(fd.Body.List)-1].Pos()
	}
	t.Fatalf("function %s with a body not found", fn)
	return token.NoPos
}

func TestSpellingShadowFastPathEquivalence(t *testing.T) {
	// Op is a package-level type never used as a local name -> the fast path
	// applies to it. Kind is a package-level type that func withLocal shadows
	// with a local variable -> the descent path must still apply to "Kind".
	const src = `package p

type Op int
type Kind int

const Zero Op = 0

func withLocal() Op {
	Kind := 5
	_ = Kind
	return Zero
}

func plain() Op {
	return Zero
}
`
	file, pkg, info := checkFileForShadow(t, src)
	opObj := pkg.Scope().Lookup("Op")
	kindObj := pkg.Scope().Lookup("Kind")
	if opObj == nil || kindObj == nil {
		t.Fatal("Op/Kind not found in package scope")
	}

	// A real lowering mints a non-empty hygienic prefix; an empty one would make
	// strings.HasPrefix(head, prefix) match every spelling and short-circuit.
	c := &converter{info: info, currentFile: file, prefix: "__bpp0_"}
	names := c.fileInnerNames()

	// "Op" is declared only at package scope, so it must be absent (fast path);
	// "Kind" is declared as a local, so it must be present (descent path).
	if names["Op"] {
		t.Errorf("fileInnerNames unexpectedly contains Op; the fast path would be skipped for every Op reference")
	}
	if !names["Kind"] {
		t.Errorf("fileInnerNames must contain the shadowing local Kind so its references keep the descent path")
	}

	posInLocal := lastStmtPos(t, file, "withLocal") // after `Kind := 5`
	posInPlain := lastStmtPos(t, file, "plain")

	cases := []struct {
		name     string
		spelling string
		pos      token.Pos
		want     types.Object
	}{
		{"op-in-shadowing-func", "Op", posInLocal, opObj},
		{"op-in-plain-func", "Op", posInPlain, opObj},
		{"kind-shadowed", "Kind", posInLocal, kindObj},
		{"kind-not-shadowed", "Kind", posInPlain, kindObj},
		{"builtin-int-plain", "int", posInPlain, types.Universe.Lookup("int")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.spellingShadowed(tc.spelling, tc.pos, tc.want)
			want := innermostShadowed(info, file, tc.spelling, tc.pos, tc.want)
			if got != want {
				t.Fatalf("spellingShadowed(%q)=%v, Innermost reference=%v", tc.spelling, got, want)
			}
		})
	}

	// The Kind cases must actually exercise both truths, or the equivalence
	// above is vacuous: shadowed inside withLocal, not shadowed inside plain.
	if !c.spellingShadowed("Kind", posInLocal, kindObj) {
		t.Error("expected Kind to be shadowed inside withLocal")
	}
	if c.spellingShadowed("Kind", posInPlain, kindObj) {
		t.Error("expected Kind not to be shadowed inside plain")
	}
}

// opConstFile mimics ssa's rewrite*.go / opGen.go: a named scalar Op with many
// typed iota constants referenced heavily inside n top-level functions whose
// bodies are switch statements (so both the file-scope child count and the
// per-function scope depth grow with n).
func opConstFile(n int) string {
	const consts = 50
	var b strings.Builder
	b.WriteString("package main\n\nimport \"fmt\"\n\ntype Op int\n\nconst (\n\tOp0 Op = iota\n")
	for i := 1; i < consts; i++ {
		b.WriteString("\tOp")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	b.WriteString(")\n\n")
	for f := 0; f < n; f++ {
		fs := itoa(f)
		b.WriteString("func rule" + fs + "(x Op) Op {\n\tswitch x {\n")
		for c := 0; c < 8; c++ {
			a, bb, cc := (f+c)%consts, (f+c+1)%consts, (f+c+2)%consts
			b.WriteString("\tcase Op" + itoa(a) + ":\n\t\treturn Op" + itoa(bb) + " + Op" + itoa(cc) + "\n")
		}
		b.WriteString("\t}\n\treturn x\n}\n\n")
	}
	b.WriteString("func main() { fmt.Println(rule0(Op0)) }\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestSpellingShadowHotTypeUsesFastPath is the deterministic guard on the fix's
// mechanism: a package-level type name that is never declared as a local (the
// heavily-referenced Op of the ssa shape) must be absent from fileInnerNames,
// so every one of its typed-constant references takes the file-scope fast path
// instead of a per-reference types.Scope.Innermost descent. A genuine local
// must still be present so its references keep the descent. This holds
// regardless of machine speed, unlike the wall-clock guard below.
func TestSpellingShadowHotTypeUsesFastPath(t *testing.T) {
	file, _, info := checkFileForShadow(t, opConstFile(200))
	c := &converter{info: info, currentFile: file, prefix: "__bpp0_"}
	names := c.fileInnerNames()
	if names["Op"] {
		t.Fatalf("Op is a package-level type used only as a type; it must not be in fileInnerNames, or every Op reference pays for the Innermost descent")
	}
	// The parameter `x Op` is a local named x, not Op; confirm the set is not
	// simply empty (which would also pass the check above by accident).
	if !names["x"] {
		t.Fatalf("expected the rule parameter x to be collected as an inner name; got %v", names)
	}
}

// TestSpellingShadowParseScaling is a coarser wall-clock guard against the
// startup path regressing to super-linear. Measured on this fix, an 8× larger
// package (8× the top-level functions and typed-constant references) lowers in
// ~8.8× the time (linear); the pre-fix O(references × decls) descent took
// ~15.5× at the same sizes. The 12× ceiling clears the linear cost with margin
// while still failing on the quadratic. Best-of-three absorbs scheduler/GC
// noise; skipped under -short because it is timing-sensitive.
func TestSpellingShadowParseScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipped under -short")
	}
	parseTime := func(n int) time.Duration {
		src := opConstFile(n)
		best := time.Duration(1<<62 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if _, err := Parse(strings.NewReader(src), "scaling.go", Options{RunMain: true}); err != nil {
				t.Fatalf("n=%d parse: %v", n, err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	small := parseTime(800)
	large := parseTime(6400) // 8× the functions
	ratio := float64(large) / float64(small)
	t.Logf("Parse(800)=%v Parse(6400)=%v ratio=%.2f", small, large, ratio)
	if ratio > 12 {
		t.Fatalf("Parse scaled %.1f× for 8× input (%v -> %v); the spellingShadowed startup path looks quadratic again",
			ratio, small, large)
	}
}
