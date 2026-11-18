package interp

import (
	"context"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// BashPPDeclaration carries the identity and attributes of a native binding.
// Its value remains in the caller's shell environment; this is not a source
// declaration, a rich-value deserializer, or a second mutable value store.
type BashPPDeclaration struct {
	ID, Name, Type string
	Constant       bool
}
type declarationPolicy struct {
	declarations map[string]BashPPDeclaration
	refused      func()
}
type declarationPolicyKey struct{}

// WithDeclarations supplies an immutable lexical view for one Run. The callback
// reports a const assignment already diagnosed by the interpreter. Unset is
// deliberately not an assignment refusal. Without metadata, shell behavior is
// unchanged; non-Bash++ runners ignore this policy.
func WithDeclarations(ctx context.Context, declarations []BashPPDeclaration, refused func()) context.Context {
	p := &declarationPolicy{declarations: make(map[string]BashPPDeclaration), refused: refused}
	for _, d := range declarations {
		if d.ID != "" && syntax.ValidName(d.Name) {
			p.declarations[d.Name] = d
		}
	}
	return context.WithValue(ctx, declarationPolicyKey{}, p)
}
func (r *Runner) withDeclarations(ctx context.Context) func() {
	old, previous := r.bashPPHostedDecls, r.bashPPDeclRefused
	p, _ := ctx.Value(declarationPolicyKey{}).(*declarationPolicy)
	r.bashPPHostedDecls, r.bashPPDeclRefused = nil, false
	if p != nil && r.Dialect() == syntax.LangBashPP {
		r.bashPPHostedDecls = p.declarations
	}
	return func() {
		refused := r.bashPPDeclRefused
		r.bashPPHostedDecls, r.bashPPDeclRefused = old, previous
		if refused && p != nil && p.refused != nil {
			p.refused()
		}
	}
}
func (r *Runner) hostedDeclaration(name string) (BashPPDeclaration, bool) {
	// A source declaration made inside the dynamic region can shadow the
	// supplied native view. Existing interpreter cells retain their authority.
	if r.bashPPScope != nil && r.bashPPScope.lookup(name) != nil {
		return BashPPDeclaration{}, false
	}
	d, ok := r.bashPPHostedDecls[name]
	return d, ok
}
func (r *Runner) refuseConstantAssignment(name string) {
	r.errf("%s%s: cannot assign to const\n", r.bashErrPrefix(r.curStmtPos), name)
	r.exit.code = 1
}
func (r *Runner) refuseDeclarationUnset(name string, constant bool) {
	kind := "var"
	if constant {
		kind = "const"
	}
	r.errf("%sunset: %s: cannot unset a bash++ %s declaration\n", r.bashErrPrefix(r.curStmtPos), name, kind)
	r.exit.code = 1
}
