package shellrt

// WithResults passes a caller-owned descriptor to exactly one source invocation.
// Its entry takes the descriptor and clears the body program's pointer before
// evaluating any nested call. Sequential sidecar storage remains shared.
func (p *Program) WithResults(frame *ResultFrame) *Program {
	call := *p
	call.Results = frame
	return &call
}
