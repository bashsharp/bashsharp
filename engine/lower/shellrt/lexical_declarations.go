package shellrt

import (
	"context"
	"sync/atomic"
)

// LexicalDeclaration is the immutable metadata of a visible native binding.
// No native payload or callable/channel authority crosses this boundary.
type LexicalDeclaration struct {
	ID, Name, Type string
	Constant       bool
}
type DeclarationPolicy struct {
	Declarations []LexicalDeclaration
	refused      atomic.Bool
}
type declarationPolicyKey struct{}

// RefuseAssignment records a diagnostic already emitted by the shell backend.
func (p *DeclarationPolicy) RefuseAssignment()       { p.refused.Store(true) }
func (p *DeclarationPolicy) AssignmentRefused() bool { return p != nil && p.refused.Load() }
func DeclarationsFromContext(ctx context.Context) *DeclarationPolicy {
	p, _ := ctx.Value(declarationPolicyKey{}).(*DeclarationPolicy)
	return p
}
func (b *LexicalBindings) CurrentInfo(name string) (LexicalInfo, bool) {
	slot := b.visible()[name]
	if slot == nil || !*slot.present {
		return LexicalInfo{}, false
	}
	return slot.raw.info, true
}
func WithLexicalDeclarations(ctx context.Context, b *LexicalBindings) (context.Context, *DeclarationPolicy) {
	p := &DeclarationPolicy{}
	visible := b.visible()
	b.mu.RLock()
	for name, id := range b.names {
		if slot := visible[name]; slot != nil && *slot.present {
			p.Declarations = append(p.Declarations, LexicalDeclaration{ID: id, Name: name, Type: slot.raw.info.SourceType, Constant: slot.raw.info.Constant})
		}
	}
	b.mu.RUnlock()
	return context.WithValue(ctx, declarationPolicyKey{}, p), p
}
