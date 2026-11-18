package shellrt

// ShellBinding preserves raw shell bytes without forcing an invalid native
// value through a typed load. The compiler supplies its established projection
// lazily for a binding that has not received a raw shell write.
func (p *Program) ShellBinding(name string, native func() string) string {
	if slot := p.Bindings.visible()[name]; slot != nil && *slot.present && slot.raw != nil && slot.raw.present {
		return slot.raw.value.String()
	}
	return native()
}
