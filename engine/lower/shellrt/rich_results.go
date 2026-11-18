package shellrt

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Rich results are the results of one invocation whose declared source carrier
// cannot, on its own, hold everything the callee produced. The motivating
// source declares `type Ch int` and returns a live channel in a `Ch` result:
// the declared Go storage is an `int` and stays an `int`, because the public
// signature is source-defined and nothing here rewrites it. The same shape
// covers an ordinary `string` carrier that holds channel authority, so nothing
// in this file is specialised to a type named Ch.
//
// The rules this file is built to:
//
//   - No ambient state. The caller allocates a [ResultFrame] for exactly one
//     invocation and passes it to the callee, so nested, recursive and
//     concurrent invocations each have their own. There is no program-wide,
//     goroutine-local or Program-held "last result" slot, and no arity cap:
//     the frame is as wide as the source signature says.
//   - Authority is identity, never a value. A capability is bound to a
//     binding's storage address and validated against the [ChannelScope] that
//     owns it. A numerically equal `Ch`, or a string produced by
//     interpolation, has different storage and therefore no authority. Copying
//     one binding's authority onto another is an explicit operation.
//   - No partial commit. A transfer validates every target's static type and
//     every capability's owner before writing anything, so a failed transfer
//     leaves both the native values and the prior sidecars alone.
//   - Presence is not status. A slot records what the callee produced
//     independently of the program's exit status; a non-zero status neither
//     erases a result nor invents one.
//
// Diagnostics reuse what already exists. A channel operation that fails
// because its scope is closed or foreign returns the runtime's established
// [ErrChannelScopeClosed] / [ErrForeignChannel], so the generated code's
// [MustChannelOperation] reports it exactly as any other channel operation.
// Misuse of the helper itself — a slot that was never produced, a target that
// is not a binding — is an ordinary internal error, not a new source-language
// diagnostic code.
//
// The bounded scope of this helper: the only capability it carries is a
// channel owned by a [ChannelScope]. Any other payload is refused where it is
// offered rather than silently dropped or fabricated into the carrier.

// ErrResultCapabilityEscape is the explicit refusal at a public Go signature.
// A result that exists only as capability metadata has no faithful ordinary
// value, so it is reported rather than returned as a zero carrier.
var ErrResultCapabilityEscape = errors.New("bash++: result capability has no ordinary Go representation")

// errNoResultCapability is helper misuse: a binding with no authority bound to
// it was used as though it had some.
var errNoResultCapability = errors.New("bash++: no channel capability is bound to this binding")

// errNoResultScope marks an owner that never had an ownership scope to lose.
var errNoResultScope = errors.New("bash++: result capability has no ownership scope")

// ResultOwner is the revocable authority a capability belongs to: the
// invocation's channel ownership scope and its session, and nothing else. It
// deliberately carries no [Frame]: permission to call an agentic function and
// authority over a channel are separate, so using metadata never imports the
// permission its creation site held.
type ResultOwner struct {
	Channels *ChannelScope
	Session  *Session
}

// authority reports whether the owner still holds its scope, and why not.
func (o *ResultOwner) authority(ctx context.Context) error {
	if o == nil || o.Channels == nil {
		return errNoResultScope
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if o.Session != nil && o.Session.group != nil {
		if err := o.Session.group.ctx.Err(); err != nil {
			return err
		}
	}
	select {
	case <-o.Channels.Done():
		return ErrChannelScopeClosed
	default:
		return nil
	}
}

// resultCapability retains a native authority by identity. It reuses
// [ChannelCapability], the runtime's existing scope-owned channel reference
// whose string form is deliberately lossy, so a result sidecar and a value
// cell speak about authority in one vocabulary. The declared carrier type is
// kept beside it so a rebind cannot move a capability onto an unrelated cell.
type resultCapability struct {
	owner    *ResultOwner
	channel  *ChannelCapability
	declared reflect.Type
}

// native is the retained channel, still owned by the scope that made it.
func (c *resultCapability) native() reflect.Value { return reflect.ValueOf(c.channel.value) }

// live reports whether the capability is still usable through its owner: the
// owner must hold its scope, the reference must belong to that scope, and the
// channel must still be registered in it. It is the type-agnostic form of the
// check [ResolveChannel] performs for a known element type.
func (c *resultCapability) live(ctx context.Context) error {
	if err := c.owner.authority(ctx); err != nil {
		return err
	}
	if c.channel == nil || c.channel.owner != c.owner.Channels {
		return ErrForeignChannel
	}
	_, err := c.owner.Channels.state(c.native())
	return err
}

// resultSlot is one declared result: its source-declared carrier type, the
// ordinary native value that carrier receives, and the authority the carrier
// cannot represent, if any.
type resultSlot struct {
	present    bool
	declared   reflect.Type
	value      any
	capability *resultCapability
}

// ResultFrame is one invocation's result descriptor. The caller allocates it
// for exactly that call and passes it to the callee, which fills it at its
// returns; the caller then consumes it. It is not safe for concurrent use by
// several goroutines and does not need to be, because it belongs to one
// invocation.
type ResultFrame struct {
	owner *ResultOwner
	slots []resultSlot
}

// NewResultFrame allocates the descriptor for one invocation of a callable
// with the given result arity. The arity is the source signature's; this
// runtime imposes no limit of its own.
func NewResultFrame(owner *ResultOwner, arity int) (*ResultFrame, error) {
	if arity < 0 {
		return nil, fmt.Errorf("bash++: result arity %d is negative", arity)
	}
	return &ResultFrame{owner: owner, slots: make([]resultSlot, arity)}, nil
}

// Arity reports the number of declared results the frame describes.
func (f *ResultFrame) Arity() int {
	if f == nil {
		return 0
	}
	return len(f.slots)
}

// Owner reports the authority the frame's capabilities belong to.
func (f *ResultFrame) Owner() *ResultOwner {
	if f == nil {
		return nil
	}
	return f.owner
}

// Present reports whether a result was produced. It is independent of the
// program's exit status: a callable that fails after producing a result still
// produced it, and one that succeeds without producing a slot has not.
func (f *ResultFrame) Present(index int) bool {
	if f == nil || index < 0 || index >= len(f.slots) {
		return false
	}
	return f.slots[index].present
}

func (f *ResultFrame) slot(index int) error {
	if f == nil {
		return errors.New("bash++: result frame is missing")
	}
	if index < 0 || index >= len(f.slots) {
		return fmt.Errorf("bash++: result slot %d is outside the frame's %d results", index, len(f.slots))
	}
	return nil
}

// SetResult records an ordinary native result. T is the declared source
// carrier type, retained even when the payload is a nil interface, so a later
// transfer checks the declared type rather than a dynamic one.
func SetResult[T any](f *ResultFrame, index int, value T) error {
	if err := f.slot(index); err != nil {
		return err
	}
	f.slots[index] = resultSlot{present: true, declared: reflect.TypeFor[T](), value: value}
	return nil
}

// SetResultCapability records a result whose declared carrier T cannot hold
// the authority the callee produced. The carrier receives `carried` — the
// callee's own result cell value, normally the source zero of T — and the
// channel is retained beside it.
//
// The capability must be a non-nil channel registered in the owner's scope.
// A scope failure is reported with the runtime's established channel errors;
// a payload that is not a channel at all is helper misuse.
func SetResultCapability[T any, C any](f *ResultFrame, index int, carried T, capability C) error {
	if err := f.slot(index); err != nil {
		return err
	}
	value := reflect.ValueOf(capability)
	if !value.IsValid() || value.Kind() != reflect.Chan {
		return fmt.Errorf("bash++: %s is not a channel capability", valueTypeName(reflect.TypeFor[C]()))
	}
	if value.IsNil() {
		return fmt.Errorf("bash++: nil %s carries no capability", valueTypeName(reflect.TypeFor[C]()))
	}
	if err := f.owner.authority(nil); err != nil {
		return err
	}
	if _, err := f.owner.Channels.state(value); err != nil {
		return err
	}
	f.slots[index] = resultSlot{
		present:  true,
		declared: reflect.TypeFor[T](),
		value:    carried,
		capability: &resultCapability{
			owner:    f.owner,
			channel:  &ChannelCapability{owner: f.owner.Channels, value: capability},
			declared: reflect.TypeFor[T](),
		},
	}
	return nil
}

// MustResult adapts a result-recording call to a generated statement. A
// descriptor failure is a checked engine abort, not a source panic; a channel
// failure keeps its own diagnostic through [MustChannelOperation].
func MustResult(err error) {
	if err != nil {
		panic(ValueAbort{Err: err})
	}
}

// NativeResult is the public native wrapper boundary. An ordinary Go consumer
// of a compiled callable receives ordinary Go values, so a result that exists
// only as capability metadata must not escape as a fabricated zero: it is
// reported with [ErrResultCapabilityEscape] instead.
func NativeResult[T any](f *ResultFrame, index int, site ValueSite) (T, error) {
	var zero T
	if err := f.slot(index); err != nil {
		return zero, err
	}
	slot := f.slots[index]
	if !slot.present {
		return zero, fmt.Errorf("bash++: result %d was never produced", index)
	}
	if slot.capability != nil {
		where := ""
		if site.File != "" {
			where = fmt.Sprintf("%s: line %d: ", site.File, site.Line)
		}
		return zero, fmt.Errorf("%s%w: result %d carries %s", where, ErrResultCapabilityEscape, index, valueTypeName(slot.capability.native().Type()))
	}
	if !slot.declared.AssignableTo(reflect.TypeFor[T]()) {
		return zero, fmt.Errorf("bash++: cannot return %s as %s", valueTypeName(slot.declared), valueTypeName(reflect.TypeFor[T]()))
	}
	if slot.value == nil {
		return zero, nil
	}
	return slot.value.(T), nil
}

// ResultSidecars maps a binding's storage identity to the authority bound to
// it. An ordinary sequential region shares one table by identity, exactly like
// the channel scope and the readonly state; a child that copies its storage
// gets its own through [ForkSidecars]. It is safe for concurrent use.
type ResultSidecars struct {
	mu       sync.RWMutex
	bound    map[resultKey]*resultCapability
	retained []any
}

type resultKey struct {
	typ     reflect.Type
	address uintptr
}

// NewResultSidecars returns an empty table. The zero value is also usable.
func NewResultSidecars() *ResultSidecars { return &ResultSidecars{} }

// resultTargetKey derives the identity of a binding: its storage location and
// its pointer type. Never its value, so two distinct carriers holding equal
// integers — or equal strings — are never confused for one another.
func resultTargetKey(target any) (resultKey, error) {
	pointer := reflect.ValueOf(target)
	if !pointer.IsValid() || pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return resultKey{}, errors.New("bash++: capability target is not a binding")
	}
	return resultKey{typ: pointer.Type(), address: pointer.Pointer()}, nil
}

func (s *ResultSidecars) bind(key resultKey, target any, capability *resultCapability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bound == nil {
		s.bound = map[resultKey]*resultCapability{}
	}
	if capability == nil {
		delete(s.bound, key)
		return
	}
	s.bound[key] = capability
	// Retention keeps the identity key stable while the binding is reachable,
	// mirroring ReadonlyState.
	s.retained = append(s.retained, target)
}

func (s *ResultSidecars) lookup(key resultKey) *resultCapability {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bound[key]
}

func (s *ResultSidecars) capability(target any) (*resultCapability, error) {
	key, err := resultTargetKey(target)
	if err != nil {
		return nil, err
	}
	capability := s.lookup(key)
	if capability == nil {
		return nil, errNoResultCapability
	}
	return capability, nil
}

// HasCapability reports whether authority is currently bound to target. A
// value copy has its own storage identity and therefore none: copying an
// int-declared or string-declared carrier copies an integer or a string, which
// is the whole reason authority lives here and not in that value.
func HasCapability(s *ResultSidecars, target any) bool {
	key, err := resultTargetKey(target)
	if err != nil {
		return false
	}
	return s.lookup(key) != nil
}

// BindCapability moves the authority bound to `from` onto `to`. It is how a
// real source assignment between two native bindings carries metadata: the
// compiler emits it for the assignment itself, and never for a value that only
// looks equal. The declared carrier types must match.
func BindCapability(s *ResultSidecars, from, to any) error {
	source, err := resultTargetKey(from)
	if err != nil {
		return err
	}
	destination, err := resultTargetKey(to)
	if err != nil {
		return err
	}
	capability := s.lookup(source)
	if capability == nil {
		return errNoResultCapability
	}
	if source.typ != destination.typ {
		return fmt.Errorf("bash++: cannot bind a %s capability to %s", valueTypeName(source.typ.Elem()), valueTypeName(destination.typ.Elem()))
	}
	if err := capability.live(nil); err != nil {
		return err
	}
	s.bind(destination, to, capability)
	return nil
}

// ReleaseCapability unbinds target. It does not close the channel: the owning
// scope remains the only thing that revokes authority.
func ReleaseCapability(s *ResultSidecars, target any) {
	if s == nil {
		return
	}
	key, err := resultTargetKey(target)
	if err != nil {
		return
	}
	s.bind(key, nil, nil)
}

// RevokeOwner drops every capability held under owner's scope. It is the end
// of an entry invocation's authority: afterwards a binding that still holds
// its declared carrier value cannot reach the channel through this table.
//
// Ownership is compared by the channel scope, which is the authority itself,
// not by the address of a particular ResultOwner value: a caller that
// describes the same scope in a fresh struct describes the same owner.
func RevokeOwner(s *ResultSidecars, owner *ResultOwner) {
	if s == nil || owner == nil || owner.Channels == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, capability := range s.bound {
		if capability.owner != nil && capability.owner.Channels == owner.Channels {
			delete(s.bound, key)
		}
	}
}

// ForkSidecars builds the table a child region uses, through the SAME address
// map the child's storage was cloned with. It creates no second value graph:
// each binding that crossed the boundary is looked up in the snapshot and its
// child address is bound to the same retained [ChannelCapability].
//
// What changes is who is asked. The child's capabilities are checked against
// `owner`, which holds the child's own scope, so the established rule decides:
// a task that shares its owner's scope keeps working channels, while a
// subshell with a fresh scope refuses the inherited handle with
// [ErrForeignChannel] — the same answer [Snapshot] gives for a channel in a
// copied value graph. Parent authority is never handed to a fresh scope.
//
// The snapshot must have been cloned already, so its address map is populated.
func ForkSidecars(s *ResultSidecars, snapshot *Snapshot, owner *ResultOwner) (*ResultSidecars, error) {
	child := NewResultSidecars()
	if s == nil {
		return child, nil
	}
	if snapshot == nil || !snapshot.started {
		return nil, &SnapshotError{"sidecar fork requires a cloned snapshot"}
	}
	if owner == nil || owner.Channels == nil {
		return nil, &SnapshotError{"sidecar fork requires the child's ownership scope"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, capability := range s.bound {
		destination, ok := snapshot.addresses[snapshotKey{typ: key.typ.Elem(), address: key.address}]
		if !ok {
			// The binding did not cross this boundary; the child has no
			// storage to bind authority to, and none is invented for it.
			continue
		}
		if !destination.CanAddr() {
			return nil, &SnapshotError{"child binding for a capability is not addressable"}
		}
		address := destination.Addr()
		child.bind(resultKey{typ: address.Type(), address: address.Pointer()}, address.Interface(), &resultCapability{
			owner:    owner,
			channel:  capability.channel,
			declared: capability.declared,
		})
	}
	return child, nil
}

// TransferResults commits one invocation's results to their destinations.
//
// Validation happens in full before any mutation: every slot's presence, every
// capability's owner and scope, then the whole value assignment through
// [AssignTuple], which is itself all-or-nothing. Only once all of that has
// succeeded are the sidecars rebound. A failure therefore leaves the prior
// native values and the prior capability bindings exactly as they were.
//
// A nil target denotes the blank identifier: its value is discarded, and so is
// its authority, because that is what the source asked for.
func TransferResults(f *ResultFrame, sidecars *ResultSidecars, targets []any, site ValueSite) error {
	if f == nil {
		return errors.New("bash++: result frame is missing")
	}
	if len(targets) != len(f.slots) {
		return &TupleError{Message: fmt.Sprintf("tuple arity mismatch: %d results, %d targets", len(f.slots), len(targets)), Site: site}
	}
	type pending struct {
		key        resultKey
		target     any
		capability *resultCapability
	}
	var staged []pending
	for i, slot := range f.slots {
		if !slot.present {
			return fmt.Errorf("bash++: result %d was never produced", i)
		}
		if targets[i] == nil {
			continue
		}
		key, err := resultTargetKey(targets[i])
		if err != nil {
			return err
		}
		if slot.capability != nil {
			if sidecars == nil {
				return fmt.Errorf("bash++: result %d carries a capability but no sidecar table was supplied", i)
			}
			if key.typ.Elem() != slot.capability.declared {
				return &TupleError{Message: fmt.Sprintf("cannot assign %s to %s", valueTypeName(slot.capability.declared), valueTypeName(key.typ.Elem())), Site: site}
			}
			if err := slot.capability.live(nil); err != nil {
				return err
			}
		}
		staged = append(staged, pending{key: key, target: targets[i], capability: slot.capability})
	}
	values := make([]TupleValue, len(f.slots))
	for i, slot := range f.slots {
		values[i] = TupleValue{value: slot.value, typ: slot.declared}
	}
	// AssignTuple is the existing atomic value contract: it validates every
	// target's static type and commits nothing on failure.
	if err := AssignTuple(targets, values, site); err != nil {
		return err
	}
	for _, entry := range staged {
		sidecars.bind(entry.key, entry.target, entry.capability)
	}
	return nil
}

// resolveCapability returns the native channel bound to target for a known
// element type. It goes through [ResolveChannel], the runtime's existing
// owner check, so a foreign or closed scope answers exactly as it does for any
// other channel operation.
func resolveCapability[T any](s *ResultSidecars, target any) (chan T, *ResultOwner, error) {
	capability, err := s.capability(target)
	if err != nil {
		return nil, nil, err
	}
	if err := capability.owner.authority(nil); err != nil {
		return nil, nil, err
	}
	channel, err := ResolveChannel[T](capability.owner.Channels, capability.channel)
	if err != nil {
		return nil, nil, err
	}
	return channel, capability.owner, nil
}

// ReceiveCapability receives from the channel bound to a binding, preserving
// the element type the compiler already knows, so the result stays an ordinary
// typed Go value. The carrier — an `int`-declared `Ch`, or a `string` — is
// never parsed and never reinterpreted as a handle.
func ReceiveCapability[T any](ctx context.Context, s *ResultSidecars, target any) (T, bool, error) {
	var zero T
	channel, owner, err := resolveCapability[T](s, target)
	if err != nil {
		return zero, false, err
	}
	return Receive(ctx, owner.Session, owner.Channels, channel)
}

// SendCapability sends through the channel bound to a binding. It is the other
// half of a carrier that holds authority — `assigned <- assigned` on a string
// binding — and it reaches the same [Send], with the same cancellation,
// closed-channel and scope diagnostics.
func SendCapability[T any](ctx context.Context, s *ResultSidecars, target any, value T) error {
	channel, owner, err := resolveCapability[T](s, target)
	if err != nil {
		return err
	}
	return Send(ctx, owner.Session, owner.Channels, channel, value)
}
