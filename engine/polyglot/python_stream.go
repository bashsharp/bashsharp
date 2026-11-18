package polyglot

// StreamSignature reports an analyzed export's streaming adapter. A missing
// signature is not guessed from an arbitrary callable or its result.
func (m *Module) StreamSignature(name string) (Signature, bool) {
	for _, export := range m.plan.Exports {
		if export.Name == name && export.Signature.Iterator != "" {
			return export.Signature, true
		}
	}
	return Signature{}, false
}
