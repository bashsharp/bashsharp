package shellrt

import "sync"

type nativeShellDefinition struct {
	marked bool
	body   func(*Program)
}
type nativeShellRegistry struct {
	mu        sync.RWMutex
	functions map[string]nativeShellDefinition
}

func (r *nativeShellRegistry) clone() *nativeShellRegistry {
	child := &nativeShellRegistry{functions: map[string]nativeShellDefinition{}}
	if r == nil {
		return child
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, definition := range r.functions {
		child.functions[name] = definition
	}
	return child
}

// DefineNativeShell installs a source function only when its declaration runs.
// The callable contains native typed code; no source AST is retained or run.
func (p *Program) DefineNativeShell(name string, marked bool, body func(*Program)) {
	p.nativeShells.mu.Lock()
	defer p.nativeShells.mu.Unlock()
	if p.nativeShells.functions == nil {
		p.nativeShells.functions = map[string]nativeShellDefinition{}
	}
	p.nativeShells.functions[name] = nativeShellDefinition{marked, body}
	p.SetStatus(0)
}

// CallNativeShell enters the definition using the invocation's frame. Panics
// propagate on the same goroutine through ordinary native deferred functions.
// False means no declaration has run; the caller may use ordinary shell lookup.
func (p *Program) CallNativeShell(site Site, args ...string) bool {
	p.nativeShells.mu.RLock()
	definition, ok := p.nativeShells.functions[site.Name]
	p.nativeShells.mu.RUnlock()
	if !ok {
		return false
	}
	child, err := p.Enter(site, definition.marked)
	if err != nil {
		p.Fail(err)
		return true
	}
	previous := child.Session.Params()
	child.Session.SetParams(args...)
	defer child.Session.SetParams(previous...)
	definition.body(child)
	return true
}
