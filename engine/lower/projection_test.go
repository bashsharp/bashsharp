// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package lower

import (
	"go/parser"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These are helper-level checks against recorded interpreter output. They do
// not compile or run a whole profile case and make no profile-parity claim.

func literalOf(t *testing.T, kind, text string) *syntax.BashPPBasicLit {
	t.Helper()
	return &syntax.BashPPBasicLit{Kind: kind, Value: &syntax.Lit{Value: text}}
}

// tests/lowering/profile-additional/literal-float.bpp records stdout "3/2 3"
// for `a := 1.5; b := 0x1.8p+1; println(a, b)`. The exact rational spelling is
// the interpreter's, and is preserved here without any float64 round trip.
func TestProjectionRetainsExactFloatSpelling(t *testing.T) {
	for text, want := range map[string]string{
		"1.5":      "3/2",
		"0x1.8p+1": "3",
		"1.1":      "11/10",
		"2.5e0":    "5/2",
	} {
		pr, err := projectionFromLiteral(literalOf(t, "FLOAT", text))
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if !pr.hasText || pr.text != want {
			t.Fatalf("%s: got %q want %q", text, pr.text, want)
		}
	}
}

// literal-integer.bpp records "5 15 31 1000"; literal-rune.bpp records "233 10".
func TestProjectionRetainsIntegerAndRuneSpelling(t *testing.T) {
	ints := []struct{ text, want string }{
		{"0b101", "5"}, {"0o17", "15"}, {"0x1f", "31"}, {"1_000", "1000"},
	}
	for _, c := range ints {
		pr, err := projectionFromLiteral(literalOf(t, "INT", c.text))
		if err != nil {
			t.Fatalf("%s: %v", c.text, err)
		}
		if pr.text != c.want {
			t.Fatalf("%s: got %q want %q", c.text, pr.text, c.want)
		}
	}
	runes := []struct{ text, want string }{{`'é'`, "233"}, {`'\n'`, "10"}}
	for _, c := range runes {
		pr, err := projectionFromLiteral(literalOf(t, "CHAR", c.text))
		if err != nil {
			t.Fatalf("%s: %v", c.text, err)
		}
		if pr.text != c.want {
			t.Fatalf("%s: got %q want %q", c.text, pr.text, c.want)
		}
	}
}

// A string constant projects as its contents, not as a quoted or JSON form.
func TestProjectionStringConstantIsPlain(t *testing.T) {
	pr, err := projectionFromLiteral(literalOf(t, "STRING", `"incremental"`))
	if err != nil {
		t.Fatal(err)
	}
	if pr.text != "incremental" {
		t.Fatalf("got %q", pr.text)
	}
	// Content that looks like JSON is still an ordinary string.
	pr, err = projectionFromLiteral(literalOf(t, "STRING", `"{\"a\":1}"`))
	if err != nil {
		t.Fatal(err)
	}
	if pr.text != `{"a":1}` || pr.kind != projectScalar {
		t.Fatalf("got %q kind %v", pr.text, pr.kind)
	}
}

// The float64 entry point is a refusal, not a convenience. 1.5 happens to be
// exact as a float64, so a lenient implementation would look correct there and
// diverge on 1.1; both must fail.
func TestProjectionFromFloat64AlwaysFailsClosed(t *testing.T) {
	for _, f := range []float64{1.5, 1.1, 0} {
		if _, err := projectionFromFloat64(f); err == nil {
			t.Fatalf("%v: expected refusal", f)
		} else if !strings.Contains(err.Error(), "source literal") {
			t.Fatalf("%v: refusal lacks the actionable hint: %v", f, err)
		}
	}
}

func TestProjectionRejectsBadLiterals(t *testing.T) {
	if _, err := projectionFromLiteral(nil); err == nil {
		t.Fatal("nil literal accepted")
	}
	if _, err := projectionFromLiteral(literalOf(t, "IMAG", "1i")); err == nil {
		t.Fatal("unsupported kind accepted")
	}
	if _, err := projectionFromLiteral(literalOf(t, "INT", "0x")); err == nil {
		t.Fatal("invalid literal accepted")
	}
}

func TestProjectValueChoosesKindFromProvenanceNotShape(t *testing.T) {
	var p projector
	p.projectionPush()
	p.projectionBind("h", objectProjection())
	p.projectionBind("s", scalarProjection())
	// Same emitted expression, different recorded provenance.
	object, err := p.projectValue("h", "h")
	if err != nil {
		t.Fatal(err)
	}
	scalar, err := p.projectValue("s", "s")
	if err != nil {
		t.Fatal(err)
	}
	if object != "shellrt.Project(h, shellrt.KindObject)" {
		t.Fatalf("object projection: %s", object)
	}
	if scalar != "shellrt.Project(s, shellrt.KindScalar)" {
		t.Fatalf("scalar projection: %s", scalar)
	}
	for _, src := range []string{object, scalar} {
		if _, err := parser.ParseExpr(src); err != nil {
			t.Fatalf("emitted %s is not a Go expression: %v", src, err)
		}
	}
}

// A retained constant emits a plain Go literal with no runtime call, which is
// how the exact spelling reaches the generated program.
func TestProjectValueEmitsConstantsDirectly(t *testing.T) {
	var p projector
	p.projectionBind("a", mustLiteral(t, "FLOAT", "1.5"))
	got, err := p.projectValue("a", "a")
	if err != nil {
		t.Fatal(err)
	}
	if got != `"3/2"` {
		t.Fatalf("got %s want %q", got, "3/2")
	}
	if strings.Contains(got, "shellrt") {
		t.Fatalf("constant projection emitted a runtime call: %s", got)
	}
	if _, err := parser.ParseExpr(got); err != nil {
		t.Fatalf("emitted %s is not a Go expression: %v", got, err)
	}
	p.projectionBind("b", projectionFromBool(true))
	if got, _ := p.projectValue("b", "b"); got != `"true"` {
		t.Fatalf("bool constant: %s", got)
	}
}

func mustLiteral(t *testing.T, kind, text string) projection {
	t.Helper()
	pr, err := projectionFromLiteral(literalOf(t, kind, text))
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func TestProjectValueRejectsUnknownName(t *testing.T) {
	var p projector
	p.projectionPush()
	_, err := p.projectValue("missing", "missing")
	if err == nil {
		t.Fatal("unknown name accepted")
	}
	d, ok := projectionDiagnostic(&syntax.Lit{}, err).(Diagnostic)
	if !ok || d.Code != CodeUndefined {
		t.Fatalf("got %#v want a %s diagnostic", err, CodeUndefined)
	}
	var p2 projector
	p2.projectionBind("h", objectProjection())
	if _, err := p2.projectValue("h", ""); err == nil {
		t.Fatal("empty expression accepted")
	}
}

// Emitter hook proof: the projector is driven in lockstep with the emitter's
// existing push/pop/bind, which is the seam the callable owner wires up. Inner
// bindings shadow and then disappear exactly as the emitter's own scopes do.
func TestProjectorTracksEmitterScopes(t *testing.T) {
	e := &emitter{scopes: []map[string]bool{{}}, funcs: map[string]bool{}, globals: map[string]bool{}}
	var p projector
	p.projectionPush()

	e.bind("h")
	p.projectionBind("h", objectProjection())

	e.push()
	p.projectionPush()
	e.bind("h") // shadows the outer rich root with a selected scalar
	p.projectionBind("h", scalarProjection())
	e.bind("n")
	p.projectionBind("n", mustLiteral(t, "INT", "0x1f"))

	if got, _ := p.projectValue("h", "h"); got != "shellrt.Project(h, shellrt.KindScalar)" {
		t.Fatalf("inner shadow: %s", got)
	}
	if got, _ := p.projectValue("n", "n"); got != `"31"` {
		t.Fatalf("inner constant: %s", got)
	}

	e.pop()
	p.projectionPop()

	if !e.known("h") {
		t.Fatal("emitter lost the outer binding")
	}
	if got, _ := p.projectValue("h", "h"); got != "shellrt.Project(h, shellrt.KindObject)" {
		t.Fatalf("outer rich root not restored: %s", got)
	}
	if _, err := p.projectValue("n", "n"); err == nil {
		t.Fatal("inner binding outlived its scope")
	}
	if e.known("n") {
		t.Fatal("emitter scope disagrees; the seam would drift")
	}
}

// Popping more than was pushed must not panic mid-compile.
func TestProjectorPopIsSafe(t *testing.T) {
	var p projector
	p.projectionPop()
	p.projectionPush()
	p.projectionPop()
	p.projectionPop()
	p.projectionBind("_", objectProjection())
	p.projectionBind("", objectProjection())
	if _, ok := p.projectionLookup("_"); ok {
		t.Fatal("blank identifier was bound")
	}
}

// --- Measured against the engine ------------------------------------------
//
// Expectations below were observed by running the source through interp.Runner
// with interp.Lang(syntax.LangBashPP). The observed stdout is quoted inline.

// Observed: `x := 1.5; echo "1:$x"; x=2.5; echo "2:$x"` → "1:3/2\n2:2.5\n".
// The exact rational belongs to the binding, not to the name: after a shell
// assignment the variable holds the raw assigned text. A retained literal that
// outlived its binding would reprint 3/2 forever, which is why the emitter
// needs an assignment hook and not just a bind hook.
func TestProjectionAssignmentReplacesRetainedLiteral(t *testing.T) {
	var p projector
	p.projectionBind("x", mustLiteral(t, "FLOAT", "1.5"))
	if got, err := p.projectValue("x", "x"); err != nil || got != `"3/2"` {
		t.Fatalf("before assignment: got %s, %v; want \"3/2\"", got, err)
	}
	p.projectionAssign("x", "2.5")
	got, err := p.projectValue("x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if got != `"2.5"` {
		t.Fatalf("after assignment: got %s want %q", got, "2.5")
	}
	// The assigned text is stored verbatim, not re-derived as a rational.
	if strings.Contains(got, "/") {
		t.Fatalf("assignment re-derived a rational: %s", got)
	}
}

// Observed the same shape for integers and strings: `x := 5; x=7` → "1:5\n2:7\n",
// and `x := "a"; x=b` → "1:a\n2:b\n".
func TestProjectionAssignmentForIntAndString(t *testing.T) {
	var p projector
	p.projectionBind("i", mustLiteral(t, "INT", "5"))
	p.projectionAssign("i", "7")
	if got, _ := p.projectValue("i", "i"); got != `"7"` {
		t.Fatalf("int reassignment: %s", got)
	}
	p.projectionBind("s", mustLiteral(t, "STRING", `"a"`))
	p.projectionAssign("s", "b")
	if got, _ := p.projectValue("s", "s"); got != `"b"` {
		t.Fatalf("string reassignment: %s", got)
	}
}

// An assignment whose text is not statically known drops the retained literal.
// A float binding then fails closed rather than reprinting a stale 3/2.
func TestProjectionInvalidateFailsClosedForFloat(t *testing.T) {
	var p projector
	p.projectionBind("x", mustLiteral(t, "FLOAT", "1.5"))
	p.projectionInvalidate("x")
	got, err := p.projectValue("x", "x")
	if err == nil {
		t.Fatalf("invalidated float still projected %s", got)
	}
	if !strings.Contains(err.Error(), "source literal") {
		t.Fatalf("refusal lacks the actionable hint: %v", err)
	}
	// A non-float binding falls back to runtime projection rather than failing.
	p.projectionBind("n", mustLiteral(t, "INT", "5"))
	p.projectionInvalidate("n")
	if got, err := p.projectValue("n", "n"); err != nil || got != "shellrt.Project(n, shellrt.KindScalar)" {
		t.Fatalf("invalidated int: got %s, %v", got, err)
	}
}

// Assignment updates the binding where it lives rather than creating a shadow
// that would vanish on scope exit.
func TestProjectionAssignmentUpdatesOuterBinding(t *testing.T) {
	var p projector
	p.projectionPush()
	p.projectionBind("x", mustLiteral(t, "INT", "5"))
	p.projectionPush()
	p.projectionAssign("x", "9")
	p.projectionPop()
	if got, _ := p.projectValue("x", "x"); got != `"9"` {
		t.Fatalf("assignment did not reach the outer binding: %s", got)
	}
}

// No typed-float path in the engine renders at all today: `var a float64 = 1.1`
// reports `"var": executable file not found`, a float64 struct field, slice
// element and map value each report BASHPP-ECOLLECTION-ELEMENT, and `a := 1.5;
// b := a + a` reports BASHPP-EEXPR-OPERAND on Unknown and Unknown. So a float
// projection without retained provenance has nothing faithful to emit and must
// fail closed rather than invent a decimal.
func TestProjectionFloatWithoutProvenanceFailsClosed(t *testing.T) {
	var p projector
	p.projectionBind("f", projection{kind: projectFloat})
	if got, err := p.projectValue("f", "f"); err == nil {
		t.Fatalf("float without provenance projected %s", got)
	}
	// It is a positioned compiler diagnostic, not a runtime marker.
	_, err := p.projectValue("f", "f")
	d, ok := projectionDiagnostic(&syntax.Lit{}, err).(Diagnostic)
	if !ok || d.Code != CodeExpr {
		t.Fatalf("got %#v want a %s diagnostic", err, CodeExpr)
	}
	if strings.Contains(d.Msg, "<") {
		t.Fatalf("diagnostic looks like a marker string: %s", d.Msg)
	}
}

// Pointer and interface roots get their own kinds, and rich nil behaviour is
// untouched. Observed: pointer roots interpolate empty (`p=[]` for &S{},
// &namedInt, new(S) and nil *S), a nil interface interpolates empty, and a nil
// map or slice still renders "null".
func TestProjectValueEmitsPointerAndInterfaceKinds(t *testing.T) {
	var p projector
	p.projectionBind("ptr", pointerProjection())
	p.projectionBind("iface", interfaceProjection())
	p.projectionBind("rich", objectProjection())
	want := map[string]string{
		"ptr":   "shellrt.Project(ptr, shellrt.KindPointer)",
		"iface": "shellrt.Project(iface, shellrt.KindInterface)",
		"rich":  "shellrt.Project(rich, shellrt.KindObject)",
	}
	for name, expect := range want {
		got, err := p.projectValue(name, name)
		if err != nil {
			t.Fatal(err)
		}
		if got != expect {
			t.Fatalf("%s: got %s want %s", name, got, expect)
		}
		if _, err := parser.ParseExpr(got); err != nil {
			t.Fatalf("emitted %s is not a Go expression: %v", got, err)
		}
	}
}

// The untyped float literal keeps its exact-rational provenance and is marked
// as a float binding, so invalidation can find it later.
func TestProjectionFloatLiteralCarriesFloatKind(t *testing.T) {
	pr := mustLiteral(t, "FLOAT", "1.1")
	if pr.kind != projectFloat {
		t.Fatalf("float literal kind = %v", pr.kind)
	}
	if pr.text != "11/10" {
		t.Fatalf("got %q want 11/10", pr.text)
	}
	if got := mustLiteral(t, "INT", "5"); got.kind != projectScalar {
		t.Fatalf("int literal kind = %v", got.kind)
	}
}

// Observed: `var a float64; var b float32; echo "a=[$a] b=[$b]"` → "a=[0] b=[0]",
// and `var a float64; echo "1=[$a]"; a=2.5; echo "2=[$a]"` → "1=[0]\n2=[2.5]".
// The declared zero is the one typed-float form the engine renders, so it is
// retained as text and never reaches the runtime scalar path.
func TestProjectionFloatZeroValueIsRetained(t *testing.T) {
	var p projector
	p.projectionBind("a", zeroFloatProjection())
	got, err := p.projectValue("a", "a")
	if err != nil {
		t.Fatalf("float zero value: %v", err)
	}
	if got != `"0"` {
		t.Fatalf("got %s want %q", got, "0")
	}
	if strings.Contains(got, "shellrt") {
		t.Fatalf("float zero reached the runtime: %s", got)
	}
	// Assignment still replaces it, matching 1=[0] then 2=[2.5].
	p.projectionAssign("a", "2.5")
	if got, _ := p.projectValue("a", "a"); got != `"2.5"` {
		t.Fatalf("after assignment: %s", got)
	}
	// Invalidation still fails closed rather than reprinting the stale zero.
	p.projectionInvalidate("a")
	if got, err := p.projectValue("a", "a"); err == nil {
		t.Fatalf("invalidated float zero projected %s", got)
	}
}
