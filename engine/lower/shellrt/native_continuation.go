package shellrt

import (
	"context"
	"errors"
	"sync"
)

// NativeContinuation retains explicitly declared native shell functions and
// their lexical captures across independently compiled entries. It does not
// retain an entry's root bindings, session, permission frame or channel scope.
// Hosts sharing a real backend across Runner.Reset opt into this same lifetime
// for native declarations. Entries using one continuation execute serially.
type NativeContinuation struct {
	registry nativeShellRegistry
	once     sync.Once
	gate     chan struct{}
}

func NewNativeContinuation() *NativeContinuation { return &NativeContinuation{} }
func WithNativeContinuation(continuation *NativeContinuation) SessionOption {
	return func(session *Session) error {
		if continuation == nil {
			return errors.New("shellrt: nil native continuation")
		}
		session.nativeContinuation = continuation
		return nil
	}
}
func (c *NativeContinuation) acquire(ctx context.Context) error {
	c.once.Do(func() { c.gate = make(chan struct{}, 1) })
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *NativeContinuation) release() { <-c.gate }
