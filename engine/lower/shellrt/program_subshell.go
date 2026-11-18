package shellrt

// Subshell creates a distinct sequential lifetime and shell snapshot. Native
// binding graphs are registered by the generated caller before its body runs.
// A fresh channel scope deliberately gives inherited handles no authority.
func (p *Program) Subshell() (*Program, error) {
	session, err := p.Session.newChild(p.Context, p.Session.Snapshot())
	if err != nil {
		return nil, err
	}
	child := p.Child(p.Context, session)
	child.Channels = &ChannelScope{}
	child.Readonly = &ReadonlyState{}
	child.owner = true
	return child, nil
}
