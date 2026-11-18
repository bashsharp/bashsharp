package shellrt

import "reflect"

// CopyLexicalScalar implements a shell-shaped assignment from a resolved typed
// binding. It copies the source scalar's shell spelling before any later typed
// read converts it; the bare identifier is not shell literal text.
func CopyLexicalScalar[T, S any](p *Program, target *T, source *S) error {
	if err := p.Readonly.CheckAssign(target); err != nil {
		return err
	}
	to, from := p.Bindings.slotAt(target), p.Bindings.slotAt(source)
	if to == nil || from == nil {
		return &LexicalWriteError{Message: "scalar copy requires registered native bindings"}
	}
	if to.kind != KindScalar || from.kind != KindScalar {
		return &LexicalWriteError{to.name, "scalar copy requires scalar bindings"}
	}
	if !*from.present {
		return lexicalUndefined(from.name, ValueSite{Name: from.name})
	}
	text, err := from.shellText()
	if err != nil {
		return err
	}
	scalar := lexicalRawScalar(to.value.Type(), text)
	native, err := lexicalNativeScalar(to, scalar, ValueSite{Name: to.name})
	if err != nil {
		native = reflect.Zero(to.value.Type())
	}
	to.value.Set(native)
	to.raw.value = Var{Str: text}
	to.raw.scalar = scalar
	to.raw.present = true
	to.raw.nativeScalar = nil
	*to.present = true
	p.SetStatus(0)
	return nil
}
