package shellrt

// ShortFailureMark snapshots the typed short-declaration failure sequence.
// Sequential calls share it; task children own a fresh sequence.
func (p *Program) ShortFailureMark() uint64 {
	p.seq.mu.Lock()
	defer p.seq.mu.Unlock()
	return p.seq.shortFailure
}
func (p *Program) ShortFailure() { p.seq.mu.Lock(); p.seq.shortFailure++; p.seq.mu.Unlock() }

// SettleShortFailures restores the callable's status after its body and defers
// have continued. It does not claim that declared result bindings are present.
func (p *Program) SettleShortFailures(mark uint64) {
	if p.ShortFailureMark() != mark {
		p.SetStatus(2)
	}
}
