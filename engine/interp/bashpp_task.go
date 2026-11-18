// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"fmt"
	"reflect"
)

// Task snapshots and imported native handles.
//
// A Bash++ task is a goroutine over a cloned [Runner], so every mutable value
// reachable from the shell environment is deep copied before the task starts;
// see [bashPPObjectCloner]. An imported native value is the one thing which
// must NOT be copied that way. It is a *reference* to an object owned by the
// dependency session process — `sync.WaitGroup`, `atomic.Int64`, `*os.File`,
// a channel — and the original Go program's semantics depend on the goroutine
// and its parent naming the same object. Deep copying a native handle would
// silently split a `sync.WaitGroup` in two: `wg.Done()` inside the task would
// release a copy nobody waits on, and the parent's `wg.Wait()` would block
// forever. Serializing one is not possible either; the interpreter never holds
// the object, only a session-scoped ticket for it.
//
// So the rule for this layer is narrow and identity preserving:
//
//   - the descriptor struct is copied, so a task never shares interpreter heap
//     with its parent and `go test -race` stays clean;
//   - Session and Handle are carried across verbatim, so both sides keep
//     addressing the one native object;
//   - a handle which cannot still name a live object fails closed at the
//     snapshot, rather than being forwarded to a session which would reject or,
//     worse, misresolve it.
//
// Goroutine and function bodies themselves remain interpreted. Nothing here
// forwards original Go source; only the ticket travels.

var (
	// errBashPPUnboundNativeHandle is a handle with no minting session. It can
	// never name a native object, so a task must not be handed one.
	errBashPPUnboundNativeHandle = errors.New("native handle is not bound to a dependency session")
	// errBashPPStaleNativeHandle is a handle whose session is gone, e.g. after
	// Reset or in an independent public Subshell.
	errBashPPStaleNativeHandle = errors.New("native handle belongs to a closed dependency session")
)

// bashPPNativeHandleScope is what a snapshot knows about the dependency
// session the task will run against. A nil scope means "unknown", which only
// skips the proactive staleness check; identity is preserved either way, and
// the session still validates ownership on every request.
type bashPPNativeHandleScope struct {
	// known reports that the snapshot could observe the runner's session slot
	// at all. Without it a nil session is not evidence of staleness.
	known bool
	// session is the dependency session the task inherits, nil once closed.
	session *bashPPNativeSession
}

func bashPPNativeScopeOf(r *Runner) bashPPNativeHandleScope {
	if r == nil {
		return bashPPNativeHandleScope{}
	}
	return bashPPNativeHandleScope{known: true, session: r.bashPPTools.bridge}
}

// checkHandle refuses a handle a task could not legitimately use. It is
// deliberately conservative: it only rejects what is provably unusable, so a
// snapshot taken before the session has started is still allowed through.
func (scope bashPPNativeHandleScope) checkHandle(value bashPPBridgeValue) error {
	if value.residentSync != nil {
		return nil
	}
	if value.Kind == "handle" {
		if value.Session == "" {
			return errBashPPUnboundNativeHandle
		}
		// The task inherits the parent's session pointer verbatim (see
		// [Runner.subshell], which copies bashPPTools by value). A nil slot
		// therefore means the minting session was closed, so no live session
		// can still resolve this handle. Comparing minted ids instead would
		// have to read the session's id under its start lock, which a
		// concurrent first request holds across a dependency build.
		if scope.known && scope.session == nil {
			return errBashPPStaleNativeHandle
		}
	}
	for _, elem := range value.Elements {
		if err := scope.checkHandle(elem); err != nil {
			return err
		}
	}
	for _, field := range value.Fields {
		if err := scope.checkHandle(field); err != nil {
			return err
		}
	}
	for _, entry := range value.Entries {
		if err := scope.checkHandle(entry.Key); err != nil {
			return err
		}
		if err := scope.checkHandle(entry.Value); err != nil {
			return err
		}
	}
	return nil
}

// bashPPCopyBridgeValue deep copies a descriptor's own storage while leaving
// Session and Handle alone. Only the interpreter-side struct is duplicated;
// the native object it names is not, and must not be.
func bashPPCopyBridgeValue(value bashPPBridgeValue) bashPPBridgeValue {
	out := value
	if value.Elements != nil {
		out.Elements = make([]bashPPBridgeValue, len(value.Elements))
		for i, elem := range value.Elements {
			out.Elements[i] = bashPPCopyBridgeValue(elem)
		}
	}
	if value.Fields != nil {
		out.Fields = make(map[string]bashPPBridgeValue, len(value.Fields))
		for name, field := range value.Fields {
			out.Fields[name] = bashPPCopyBridgeValue(field)
		}
	}
	if value.Entries != nil {
		out.Entries = make([]bashPPBridgeEntry, len(value.Entries))
		for i, entry := range value.Entries {
			out.Entries[i] = bashPPBridgeEntry{
				Key:   bashPPCopyBridgeValue(entry.Key),
				Value: bashPPCopyBridgeValue(entry.Value),
			}
		}
	}
	return out
}

// cloneNativeHandle is [bashPPObjectCloner]'s rule for an imported native
// value. Aliasing is preserved: two shell names for one native object stay two
// names for that same object inside the task.
func (c *bashPPObjectCloner) cloneNativeHandle(value *bashPPBridgeValue) (any, error) {
	if value == nil {
		return (*bashPPBridgeValue)(nil), nil
	}
	key := bashPPObjectCloneKey{kind: 3, ptr: bashPPPointerWord(value)}
	if done, ok := c.done[key]; ok {
		return done, nil
	}
	if err := c.native.checkHandle(*value); err != nil {
		return nil, fmt.Errorf("%w: %v", err, bashPPNativeHandleText(*value))
	}
	copy := bashPPCopyBridgeValue(*value)
	out := &copy
	c.done[key] = out
	return out, nil
}

// bashPPNativeHandleText names a handle in an error without leaking any of the
// dependency's contents; a session id is an authority token, so only the type
// and the session-local handle number are reported.
func bashPPNativeHandleText(value bashPPBridgeValue) string {
	typ := value.Type
	if typ == "" {
		typ = value.Kind
	}
	if value.Handle == 0 {
		return typ
	}
	return fmt.Sprintf("%s#%d", typ, value.Handle)
}

// bashPPPointerWord is the identity word of one native descriptor, used to
// keep aliasing stable across a snapshot without importing unsafe.
func bashPPPointerWord(value *bashPPBridgeValue) uintptr {
	return reflect.ValueOf(value).Pointer()
}
