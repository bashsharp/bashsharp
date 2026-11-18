package shellrt

import "reflect"

// ShellExit stops generated statements after an explicit shell exit. It is a
// control transfer rather than a user panic or a second diagnostic.
type ShellExit struct{}

// DeclarationAbort ends the current source statement after the backend has
// reported a refused constant write. A file can continue with its next statement.
type DeclarationAbort struct{}

func (p *Program) RootStatement(body func()) {
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(DeclarationAbort); !ok {
				panic(value)
			}
		}
	}()
	body()
}

// ShellAbort carries a backend failure across native callable frames while
// retaining its identity for an injected entry's caller. It is not a source
// panic; generated source recover must preserve this control transfer.
type ShellAbort struct{ Err error }

func (p *Program) ShellRegion(source string) {
	exchange, err := p.shellExchangeBindings().BeginShell(p.Session)
	if err != nil {
		panic(ShellAbort{Err: err})
	}
	defer func() {
		pending := recover()
		endErr := exchange.EndShell(p.Session)
		if pending != nil {
			panic(pending)
		}
		if endErr != nil {
			panic(ShellAbort{Err: endErr})
		}
	}()

	if p.Frame.Agentic() {
		source = "agentic {\n" + source + "\n}"
	}
	ctx, policy := WithLexicalDeclarations(p.Context, p.Bindings)
	if err := p.Session.Shell(ctx, source); err != nil {
		panic(ShellAbort{Err: err})
	}
	p.SetStatus(p.Session.Status())
	if p.Session.Exited() {
		panic(ShellExit{})
	}
	if policy.AssignmentRefused() {
		panic(DeclarationAbort{})
	}
}
func (p *Program) ShellString(name string) string {
	value, _, err := p.Bindings.ShellValue(p.Session, name)
	if err != nil {
		panic(ShellAbort{Err: err})
	}
	return value
}

func (p *Program) ShellDefault(name, fallback string) string {
	value, present, err := p.Bindings.ShellValue(p.Session, name)
	if err != nil {
		panic(ShellAbort{Err: err})
	}
	if !present {
		return fallback
	}
	return value
}

// A native callable has no scalar serialization. Keep it out of unrelated
// shell exchanges; an explicit ShellValue observation still uses the lexical
// projection contract and reports unsupported callable rendering.
func (p *Program) shellExchangeBindings() *LexicalBindings {
	visible := p.Bindings.visible()
	names := map[string]string{}
	p.Bindings.mu.RLock()
	for name, id := range p.Bindings.names {
		if slot := visible[name]; slot != nil && slot.value.Kind() != reflect.Func && slot.value.Kind() != reflect.Chan {
			names[name] = id
		}
	}
	p.Bindings.mu.RUnlock()
	return p.Bindings.CaptureNames(names)
}
