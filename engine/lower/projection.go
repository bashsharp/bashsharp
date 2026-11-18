// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package lower

import (
	"fmt"
	"go/constant"
	"go/token"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// This file is the emitter's value-projection helper: it decides what shell
// text a typed binding produces when it is used as a word. It holds no global
// state, resolves nothing by inspecting a value's runtime shape, and never
// executes source at compile time. It also never renders a value to text and
// reparses that text to pick a language.

// projectionKind records how a binding's root must be rendered. It is a
// compiler fact carried from the binding site, not something recovered later
// from the value. That distinction is the whole point: an imported package
// call returning a string still has an object root and therefore projects
// JSON-quoted, while a field selected out of a rich value is a plain scalar
// even though its parent is JSON.
//
// The kinds and their renderings are measured against the engine; see
// docs/lowering-projection.md for the table and its evidence.
type projectionKind int

const (
	// projectScalar renders plain interpreter scalar text. Used for native
	// scalar bindings, including named types such as `type Count int`, and
	// for a scalar reached by selector or index.
	projectScalar projectionKind = iota
	// projectObject renders the shell's object coercion, which is JSON. Used
	// for rich native roots and for every imported-call result. A nil map or
	// nil slice root renders "null".
	projectObject
	// projectPointer renders a pointer root, which the interpreter projects
	// as the empty string whether or not the pointer is nil.
	projectPointer
	// projectInterface renders an interface root: empty when nil, otherwise
	// the plain text of the named value it holds.
	projectInterface
	// projectFloat marks a floating binding. It renders only from retained
	// literal provenance; with no retained text it fails closed, because the
	// engine itself has no other working typed-float path.
	projectFloat
)

// projection is one binding's retained provenance.
type projection struct {
	constant          bool
	emptyWhen         string
	capabilityElement string
	kind              projectionKind
	nativeAggregate   bool
	sourceType        string
	present           string
	receiver          bool
	runtimeFloat      bool
	foreignBytes      bool
	element           *projection
	// text is the exact shell spelling when the binding is a constant whose
	// source literal was retained. hasText distinguishes a known empty string
	// from an unknown value.
	text    string
	hasText bool
}

// scalarProjection marks a native scalar root: a plain value, or a scalar
// reached through a selector or index out of a rich value. Its projection is
// the interpreter's plain text, never the enclosing root's JSON.
func scalarProjection() projection { return projection{kind: projectScalar} }

// objectProjection marks a rich root. The caller passes this for native
// struct, array, slice and map roots — including named ones, which the
// interpreter also renders as JSON — and for the result of every
// imported-package call regardless of that result's Go type.
func objectProjection() projection { return projection{kind: projectObject} }

// pointerProjection marks a pointer root. Measured against the engine, every
// pointer root interpolates empty: &S{}, &namedInt, new(S) and a nil *S alike.
// This is deliberately not nil-handling, and it does not disturb the rich nil
// behaviour — a nil map or slice root is still an object root rendering "null".
func pointerProjection() projection { return projection{kind: projectPointer} }

// interfaceProjection marks an interface root, which is nil-capable: a nil
// interface interpolates empty, and a non-nil one renders the plain text of the
// named value it holds.
func interfaceProjection() projection { return projection{kind: projectInterface} }

// projectionFromLiteral retains a constant literal's exact spelling, using the
// same mechanism the interpreter uses on this path — go/constant over the
// source text, with no float64 round trip anywhere. That is what keeps an
// untyped 1.5 projecting as the exact rational 3/2 and 0x1.8p+1 as 3.
func projectionFromLiteral(lit *syntax.BashPPBasicLit) (projection, error) {
	if lit == nil || lit.Value == nil {
		return projection{}, projectionError{CodeExpr, "missing constant literal"}
	}
	kind, ok := map[string]token.Token{
		"INT":    token.INT,
		"FLOAT":  token.FLOAT,
		"CHAR":   token.CHAR,
		"STRING": token.STRING,
	}[lit.Kind]
	if !ok {
		return projection{}, projectionError{CodeExpr, fmt.Sprintf("unsupported constant kind %q", lit.Kind)}
	}
	v := constant.MakeFromLiteral(lit.Value.Value, kind, 0)
	if v.Kind() == constant.Unknown {
		return projection{}, projectionError{CodeExpr, fmt.Sprintf("invalid literal %s", lit.Value.Value)}
	}
	pr := projection{kind: projectScalar, text: constantShellText(v), hasText: true}
	if kind == token.FLOAT {
		pr.kind = projectFloat
	}
	return pr, nil
}

// zeroFloatProjection retains the declared zero value of a floating binding.
// Measured, `var a float64` and `var b float32` both interpolate 0, and that
// is the one typed-float form the engine renders. Retaining it as text keeps
// the float off the runtime scalar path entirely, so no float64 is ever
// rendered without provenance.
func zeroFloatProjection() projection {
	return projection{kind: projectFloat, text: "0", hasText: true}
}

// projectionFromBool retains the two constant identifiers the scalar reader
// treats as literals.
func projectionFromBool(b bool) projection {
	return projection{kind: projectScalar, text: strconv.FormatBool(b), hasText: true}
}

// projectionFromFloat64 always fails. It exists so the refusal is a named,
// tested boundary rather than an omission somebody later "fixes" by reaching
// for constant.MakeFloat64. A float64 cannot recover the exact rational the
// interpreter prints: 1.5 is 3/2, but 1.1 is not 11/10, and pretending
// otherwise would silently diverge on the second value while looking correct
// on the first. Retain the literal and use projectionFromLiteral instead.
func projectionFromFloat64(float64) (projection, error) {
	return projection{}, projectionError{
		CodeExpr,
		"floating scalar has no retained constant provenance; project it from its source literal, not a float64",
	}
}

// constantShellText mirrors the interpreter's scalar rendering: strings render
// as their contents, booleans as true/false, and every numeric constant as its
// exact spelling rather than a rounded decimal.
func constantShellText(v constant.Value) string {
	switch v.Kind() {
	case constant.String:
		return constant.StringVal(v)
	case constant.Bool:
		return strconv.FormatBool(constant.BoolVal(v))
	default:
		return v.ExactString()
	}
}

// projector is the emitter's projection scope stack. Its zero value is ready
// to use, so wiring it into the emitter costs one struct field and no change
// to the emitter's constructor.
type projector struct {
	scopes []map[string]projection
}

// projectionPush enters a lexical scope. It mirrors the emitter's own push.
func (p *projector) projectionPush() {
	p.scopes = append(p.scopes, map[string]projection{})
}

// projectionPop leaves a lexical scope. Popping the root scope is a no-op so a
// mismatched pair cannot panic mid-compile.
func (p *projector) projectionPop() {
	if len(p.scopes) > 0 {
		p.scopes = p.scopes[:len(p.scopes)-1]
	}
}

// projectionBind records a binding's provenance in the innermost scope.
func (p *projector) projectionBind(name string, pr projection) {
	if name == "" || name == "_" {
		return
	}
	if len(p.scopes) == 0 {
		p.projectionPush()
	}
	p.scopes[len(p.scopes)-1][name] = pr
}

// projectionLookup resolves a name innermost-first.
func (p *projector) projectionLookup(name string) (projection, bool) {
	for i := len(p.scopes) - 1; i >= 0; i-- {
		if pr, ok := p.scopes[i][name]; ok {
			return pr, true
		}
	}
	return projection{}, false
}

// projectValue returns Go source of type string that renders the binding name
// as shell text. expression is the Go expression the emitter already produced
// for that binding.
//
// A binding with retained constant provenance projects to a plain Go string
// literal and emits no runtime call at all, which is how the exact rational
// spelling survives into the generated program. Everything else defers to the
// runtime with the kind chosen here, so the runtime never has to guess.
//
// An unknown name is a positioned error rather than a shape guess.
func (p *projector) projectValue(name, expression string) (string, error) {
	pr, ok := p.projectionLookup(name)
	if !ok {
		return "", projectionError{CodeUndefined, fmt.Sprintf("undefined projection for %s", name)}
	}
	if pr.hasText {
		return strconv.Quote(pr.text), nil
	}
	if pr.kind == projectFloat {
		// The engine has no working typed-float path to match: `var a
		// float64 = 1.1`, float struct fields, float collection elements and
		// float arithmetic all error there today. The only float rendering
		// that exists is an untyped literal's exact rational, which is the
		// retained-text branch above. Without that text there is nothing
		// faithful to emit, so fail closed rather than invent a decimal.
		return "", projectionError{CodeExpr, fmt.Sprintf(
			"floating binding %s has no retained constant provenance; project it from its source literal", name)}
	}
	if expression == "" {
		return "", projectionError{CodeExpr, fmt.Sprintf("missing expression for %s", name)}
	}
	kind := "shellrt.KindScalar"
	switch pr.kind {
	case projectObject:
		kind = "shellrt.KindObject"
	case projectPointer:
		kind = "shellrt.KindPointer"
	case projectInterface:
		kind = "shellrt.KindInterface"
	}
	return fmt.Sprintf("shellrt.Project(%s, %s)", expression, kind), nil
}

// projectionAssign records a shell assignment whose text is statically known.
// This is required for correctness, not an optimisation: measured against the
// engine, `x := 1.5` interpolates the exact rational 3/2, but after `x=2.5` it
// interpolates 2.5 — the raw assigned text, not 5/2. A retained literal that
// outlived its binding would reprint 3/2 forever.
//
// The assigned text is stored verbatim because that is what shell assignment
// writes into the variable; it is not re-derived through go/constant.
func (p *projector) projectionAssign(name, text string) {
	if name == "" || name == "_" {
		return
	}
	pr, ok := p.projectionLookup(name)
	if !ok {
		pr = projection{kind: projectScalar}
	}
	pr.text, pr.hasText = text, true
	p.rebind(name, pr)
}

// projectionInvalidate drops a binding's retained literal while keeping its
// kind, for an assignment whose text is not statically known. A float binding
// then fails closed on its next projection instead of reprinting a stale exact
// rational.
func (p *projector) projectionInvalidate(name string) {
	pr, ok := p.projectionLookup(name)
	if !ok {
		return
	}
	pr.text, pr.hasText = "", false
	p.rebind(name, pr)
}

// rebind updates a name where it is actually bound, so assigning to an outer
// binding from an inner scope does not silently create a shadow.
func (p *projector) rebind(name string, pr projection) {
	for i := len(p.scopes) - 1; i >= 0; i-- {
		if _, ok := p.scopes[i][name]; ok {
			p.scopes[i][name] = pr
			return
		}
	}
	p.projectionBind(name, pr)
}

// projectionError carries a diagnostic code out of the helper without needing
// a syntax node; the emitter attaches the position it already has.
type projectionError struct{ code, msg string }

func (e projectionError) Error() string { return e.msg }

// projectionDiagnostic converts a helper error into the package's positioned
// diagnostic. Errors from other sources keep CodeExpr.
func projectionDiagnostic(n syntax.Node, err error) error {
	if err == nil {
		return nil
	}
	code := CodeExpr
	if pe, ok := err.(projectionError); ok {
		code = pe.code
	}
	d := Diagnostic{Code: code, Msg: err.Error()}
	if n != nil {
		d.Pos = n.Pos()
	}
	return d
}
