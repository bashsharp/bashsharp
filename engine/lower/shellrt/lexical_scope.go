package shellrt

// LexicalScope copies name visibility while retaining native storage identity.
// A nil names map captures this view's current names; an empty map captures none.
func (p *Program) LexicalScope(names map[string]string) *Program {
	if names == nil {
		names = map[string]string{}
		p.Bindings.mu.RLock()
		for name, id := range p.Bindings.names {
			names[name] = id
		}
		p.Bindings.mu.RUnlock()
	}
	scope := *p
	scope.Bindings = p.Bindings.CaptureNames(names)
	return &scope
}
