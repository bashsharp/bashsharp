package shellrt

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var ErrChannelScopeClosed = errors.New("bash++: channel ownership scope is closed")
var ErrForeignChannel = errors.New("bash++: channel belongs to a different ownership scope")

// ChannelScope carries ownership explicitly alongside native channel values.
// It is shared by an owner and its typed tasks, never encoded in shell strings.
// The zero value is ready for use. Close revokes access and wakes blocked work.
type ChannelScope struct {
	mu       sync.Mutex
	channels map[uintptr]*channelState
	done     chan struct{}
	closed   bool
}
type channelState struct {
	text        string
	value       reflect.Value // retain native identity for the scope's lifetime
	mu          sync.Mutex
	changed     *sync.Cond
	closing     bool
	closed      chan struct{}
	activeSends int
}

func (s *ChannelScope) initialize() {
	if s.channels == nil {
		s.channels = map[uintptr]*channelState{}
		s.done = make(chan struct{})
	}
}
func (s *ChannelScope) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialize()
	return s.done
}
func (s *ChannelScope) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialize()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}
func (s *ChannelScope) state(channel reflect.Value) (*channelState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialize()
	if s.closed {
		return nil, ErrChannelScopeClosed
	}
	if channel.IsNil() {
		return nil, nil
	}
	state := s.channels[channel.Pointer()]
	if state == nil {
		return nil, ErrForeignChannel
	}
	return state, nil
}
func MakeChannel[T any](scope *ChannelScope, capacity int) (chan T, error) {
	if capacity < 0 || capacity > 65536 {
		return nil, fmt.Errorf("bash++: channel capacity must be between 0 and 65536")
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.initialize()
	if scope.closed {
		return nil, ErrChannelScopeClosed
	}
	channel := make(chan T, capacity)
	state := &channelState{value: reflect.ValueOf(channel), closed: make(chan struct{})}
	state.changed = sync.NewCond(&state.mu)
	scope.channels[reflect.ValueOf(channel).Pointer()] = state
	return channel, nil
}
func (state *channelState) startSend() (func(), <-chan struct{}, error) {
	if state == nil {
		return func() {}, nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing {
		return nil, nil, errors.New("bash++: send on closed channel")
	}
	state.activeSends++
	return func() {
		state.mu.Lock()
		state.activeSends--
		if state.activeSends == 0 {
			state.changed.Broadcast()
		}
		state.mu.Unlock()
	}, state.closed, nil
}
func CloseChannel[T any](scope *ChannelScope, channel chan<- T) error {
	state, err := scope.state(reflect.ValueOf(channel))
	if err != nil {
		return err
	}
	if state == nil {
		return errors.New("bash++: close of nil channel")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing {
		return errors.New("bash++: close of closed channel")
	}
	state.closing = true
	close(state.closed)
	// Wake and unregister every prepared send before closing the native channel.
	// This avoids Go's concurrent-close/send race while retaining native values.
	for state.activeSends > 0 {
		state.changed.Wait()
	}
	state.value.Close()
	return nil
}
func channelCanceled(ctx context.Context, session *Session, scope *ChannelScope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session != nil {
		if err := session.group.ctx.Err(); err != nil {
			return err
		}
	}
	select {
	case <-scope.Done():
		return ErrChannelScopeClosed
	default:
		return nil
	}
}
func channelOwnerDone(session *Session) <-chan struct{} {
	if session == nil {
		return nil
	}
	return session.group.ctx.Done()
}
func Send[T any](ctx context.Context, session *Session, scope *ChannelScope, channel chan<- T, value T) error {
	if err := channelCanceled(ctx, session, scope); err != nil {
		return err
	}
	state, err := scope.state(reflect.ValueOf(channel))
	if err != nil {
		return err
	}
	finish, closed, err := state.startSend()
	if err != nil {
		return err
	}
	defer finish()
	// A ready outcome belongs to the current launch. Arm only if the first
	// selection finds no ready operation; otherwise a following task can start
	// before this task reports an immediate channel/body failure.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-channelOwnerDone(session):
		return session.group.ctx.Err()
	case <-scope.Done():
		return ErrChannelScopeClosed
	case <-closed:
		return errors.New("bash++: send on closed channel")
	case channel <- value:
		return nil
	default:
	}
	session.Arm()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-channelOwnerDone(session):
		return session.group.ctx.Err()
	case <-scope.Done():
		return ErrChannelScopeClosed
	case <-closed:
		return errors.New("bash++: send on closed channel")
	case channel <- value:
		return nil
	}
}
func Receive[T any](ctx context.Context, session *Session, scope *ChannelScope, channel <-chan T) (value T, ok bool, err error) {
	if err = channelCanceled(ctx, session, scope); err != nil {
		return
	}
	if _, err = scope.state(reflect.ValueOf(channel)); err != nil {
		return
	}
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-channelOwnerDone(session):
		err = session.group.ctx.Err()
	case <-scope.Done():
		err = ErrChannelScopeClosed
	case value, ok = <-channel:
	default:
		// Cancellation and channel closure remain observable across the arm:
		// their notifications persist until the blocking selection consumes one.
		goto blocking
	}
	return

blocking:
	session.Arm()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-channelOwnerDone(session):
		err = session.group.ctx.Err()
	case <-scope.Done():
		err = ErrChannelScopeClosed
	case value, ok = <-channel:
	}
	return
}

// ChannelCase captures operands before selection. Constructors take directional
// native channels, so generated Go retains normal send/receive type checking.
type ChannelCase struct {
	scope     *ChannelScope
	channel   reflect.Value
	value     reflect.Value
	direction reflect.SelectDir
}

func SendCase[T any](scope *ChannelScope, channel chan<- T, value T) ChannelCase {
	return ChannelCase{scope, reflect.ValueOf(channel), reflect.ValueOf(&value).Elem(), reflect.SelectSend}
}
func ReceiveCase[T any](scope *ChannelScope, channel <-chan T) ChannelCase {
	return ChannelCase{scope: scope, channel: reflect.ValueOf(channel), direction: reflect.SelectRecv}
}
func DefaultCase() ChannelCase { return ChannelCase{direction: reflect.SelectDefault} }

type ChannelSelection struct {
	Index int
	value reflect.Value
	OK    bool
}

func SelectedReceive[T any](selection ChannelSelection, _ <-chan T) (value T, ok bool) {
	if selection.value.IsValid() {
		reflect.ValueOf(&value).Elem().Set(selection.value)
	}
	return value, selection.OK
}
func SelectChannels(ctx context.Context, session *Session, scope *ChannelScope, cases []ChannelCase) (selection ChannelSelection, err error) {
	if err = channelCanceled(ctx, session, scope); err != nil {
		return
	}
	var native []reflect.SelectCase
	hasDefault := false
	for _, entry := range cases {
		if entry.direction == reflect.SelectDefault {
			hasDefault = true
			native = append(native, reflect.SelectCase{Dir: reflect.SelectDefault})
			continue
		}
		if entry.scope != scope {
			return selection, ErrForeignChannel
		}
		if _, err = scope.state(entry.channel); err != nil {
			return
		}
		native = append(native, reflect.SelectCase{Dir: entry.direction, Chan: entry.channel, Send: entry.value})
	}
	addCancel := func(done <-chan struct{}) {
		native = append(native, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(done)})
	}
	addCancel(ctx.Done())
	addCancel(channelOwnerDone(session))
	addCancel(scope.Done())
	// Register only after all operands have been captured, and unregister every
	// selected or unselected send on all exits. Close wakes blocked select sends.
	closedSends := map[int]bool{}
	for caseIndex, entry := range cases {
		if entry.direction == reflect.SelectSend {
			state, stateErr := scope.state(entry.channel)
			if stateErr != nil {
				return selection, stateErr
			}
			finish, closed, sendErr := state.startSend()
			if sendErr != nil {
				// A closed send is a ready failing case, not a preflight
				// failure that suppresses other ready select cases.
				native[caseIndex] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(state.closed)}
				closedSends[caseIndex] = true
				continue
			}
			defer finish()
			addCancel(closed)
		}
	}
	// An authored default makes the selection nonblocking. Otherwise append a
	// temporary default to probe all ready communication/cancellation cases in
	// one native selection, retaining Go's choice among ready cases.
	probe := native
	if !hasDefault {
		probe = append(probe, reflect.SelectCase{Dir: reflect.SelectDefault})
	}
	index, value, ok := reflect.Select(probe)
	if !hasDefault && index == len(native) {
		session.Arm()
		index, value, ok = reflect.Select(native)
	}
	if index < len(cases) {
		if closedSends[index] {
			return selection, errors.New("bash++: send on closed channel")
		}
		return ChannelSelection{index, value, ok}, nil
	}
	if err = channelCanceled(ctx, session, scope); err != nil {
		return
	}
	return selection, errors.New("bash++: send on closed channel")
}

// ChannelAbort unwinds a native callable without changing its public result
// signature. Every task entry must use ChannelTask to translate it back into
// an error before Session's generic panic boundary ranks failures.
type ChannelAbort struct{ Err error }

// ChannelError gives a failed language channel operation its source status
// without replacing the cause or changing its diagnostic text.
type ChannelError struct{ Err error }

func (e *ChannelError) Error() string { return e.Err.Error() }
func (e *ChannelError) Unwrap() error { return e.Err }
func (*ChannelError) ExitStatus() int { return 2 }

func channelOperationError(err error) error {
	if err == nil || failureRank(err) == 1 {
		return err
	}
	if _, ok := err.(*ChannelError); ok {
		return err
	}
	return &ChannelError{Err: err}
}

func MustChannelOperation(err error) {
	if err != nil {
		panic(ChannelAbort{channelOperationError(err)})
	}
}
func MustChannel[T any](channel chan T, err error) chan T { MustChannelOperation(err); return channel }
func MustReceive[T any](value T, ok bool, err error) (T, bool) {
	MustChannelOperation(err)
	return value, ok
}
func MustSelect(selection ChannelSelection, err error) ChannelSelection {
	MustChannelOperation(err)
	return selection
}
func ChannelTask(fn TaskFunc) TaskFunc {
	return func(ctx context.Context, session *Session) (err error) {
		defer func() {
			if value := recover(); value != nil {
				if abort, ok := value.(ChannelAbort); ok {
					err = abort.Err
				} else {
					panic(value)
				}
			}
		}()
		return fn(ctx, session)
	}
}

// TaskContext is an alias, not a process-global context holder.
type TaskContext = context.Context

func MustReceiveValue[T any](value T, _ bool, err error) T { MustChannelOperation(err); return value }

// ChannelCapability carries typed provenance across an internal value cell.
// String conversion is intentionally lossy: no string, including this display
// text, can be resolved into channel authority. Scope.Close revokes every cell.
type ChannelCapability struct {
	owner *ChannelScope
	value any
}

func (*ChannelCapability) String() string { return "<channel>" }
func ChannelReference[T any](scope *ChannelScope, channel chan T) (*ChannelCapability, error) {
	if _, err := scope.state(reflect.ValueOf(channel)); err != nil {
		return nil, err
	}
	return &ChannelCapability{scope, channel}, nil
}
func ResolveChannel[T any](scope *ChannelScope, reference any) (chan T, error) {
	capability, ok := reference.(*ChannelCapability)
	if !ok || capability == nil || capability.owner != scope {
		return nil, ErrForeignChannel
	}
	channel, ok := capability.value.(chan T)
	if !ok {
		return nil, errors.New("bash++: channel element type mismatch")
	}
	if _, err := scope.state(reflect.ValueOf(channel)); err != nil {
		return nil, err
	}
	return channel, nil
}

// CheckChannelBoundary rejects live channel authority before an external
// command/environment or shell-copy boundary. The compiler/runtime bridge must
// call it on typed values BEFORE stringification or other provenance erasure.
func CheckChannelBoundary(value any) error {
	visited := map[struct {
		typ     reflect.Type
		pointer uintptr
	}]bool{}
	var inspect func(reflect.Value) bool
	inspect = func(v reflect.Value) bool {
		if !v.IsValid() {
			return false
		}
		if v.Type() == reflect.TypeFor[*ChannelCapability]() {
			return !v.IsNil()
		}
		switch v.Kind() {
		case reflect.Chan:
			return true
		case reflect.Interface:
			if !v.IsNil() {
				return inspect(v.Elem())
			}
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if v.IsNil() {
				return false
			}
			key := struct {
				typ     reflect.Type
				pointer uintptr
			}{v.Type(), v.Pointer()}
			if visited[key] {
				return false
			}
			visited[key] = true
			switch v.Kind() {
			case reflect.Pointer:
				return inspect(v.Elem())
			case reflect.Map:
				iter := v.MapRange()
				for iter.Next() {
					if inspect(iter.Key()) || inspect(iter.Value()) {
						return true
					}
				}
			case reflect.Slice:
				for i := 0; i < v.Len(); i++ {
					if inspect(v.Index(i)) {
						return true
					}
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if inspect(v.Field(i)) {
					return true
				}
			}
		case reflect.Array:
			for i := 0; i < v.Len(); i++ {
				if inspect(v.Index(i)) {
					return true
				}
			}
		}
		return false
	}
	if inspect(reflect.ValueOf(value)) {
		return errors.New("bash++: channel cannot cross an external or shell-copy boundary")
	}
	return nil
}
