package shellrt

import "slices"

// WithParams sets the session's initial positional parameters, what a shell
// region reads as $1, $2, ... and counts with $#.
//
// The arguments are taken as opaque strings and stored one per parameter: an
// empty argument stays an empty parameter, and nothing is split, joined,
// quoted or re-encoded on the way in. `WithParams()` with no arguments seeds
// an empty parameter list, which is what a program invoked with no operands
// wants.
//
// A later WithParams wins over an earlier one, so a generated entry can offer
// the process arguments as a default and still let its caller override them:
//
//	rt.NewProgram(append([]rt.SessionOption{rt.WithParams(os.Args[1:]...)}, opts...)...)
//
// The supplied slice is copied, so the caller keeps ownership of it and
// os.Args is never aliased or mutated.
func WithParams(params ...string) SessionOption {
	return func(s *Session) error {
		s.state.Params = slices.Clone(params)
		if s.state.Params == nil {
			s.state.Params = []string{}
		}
		return nil
	}
}

// Params reports the positional parameters as an independent copy, so the
// caller cannot write through into the session's projection.
func (s *Session) Params() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.Params)
}

// SetParams replaces the positional parameters. The write reaches the dynamic
// shell before the next region runs, the same way a variable write does.
func (s *Session) SetParams(params ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Params = slices.Clone(params)
	if s.state.Params == nil {
		s.state.Params = []string{}
	}
}
