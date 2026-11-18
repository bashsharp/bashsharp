package shellrt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
)

// ChannelWord is display-only identity. No runtime operation resolves authority
// from this text; copied or interpolated bytes remain ordinary strings.
func ChannelWord(scope *ChannelScope, channel any) string {
	state, err := scope.state(reflect.ValueOf(channel))
	MustChannelOperation(err)
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.text == "" {
		var token [24]byte
		if _, err := rand.Read(token[:]); err != nil {
			panic(ChannelAbort{Err: err})
		}
		state.text = "chan@bashpp:" + hex.EncodeToString(token[:])
	}
	return state.text
}
func (p *Program) WithArguments(frame *ResultFrame) *Program {
	call := *p
	call.Arguments = frame
	return &call
}

func CarryChannel[T, C any](p *Program, target *T, channel C) error {
	if err := p.Readonly.CheckAssign(target); err != nil {
		return err
	}
	frame, err := NewResultFrame(&ResultOwner{Channels: p.Channels, Session: p.Session}, 1)
	if err != nil {
		return err
	}
	var zero T
	if err := SetResultCapability(frame, 0, zero, channel); err != nil {
		return err
	}
	if err := TransferResults(frame, p.ResultSidecars, []any{target}, ValueSite{}); err != nil {
		return err
	}
	return p.Bindings.NativeWrittenAt(target)
}

func RequireChannelBinding(sidecars *ResultSidecars, target any, name string) error {
	if !HasCapability(sidecars, target) {
		return fmt.Errorf("bash++: %s is not a channel in this task group", name)
	}
	return nil
}

// SetArgumentFromBinding copies a retained capability into the declared native
// parameter carrier. The source storage identity is supplied explicitly.
func SetArgumentFromBinding[T any](frame *ResultFrame, index int, value T, sidecars *ResultSidecars, source any) error {
	if err := SetResult(frame, index, value); err != nil {
		return err
	}
	capability, err := sidecars.capability(source)
	if err != nil {
		return err
	}
	if err := capability.live(nil); err != nil {
		return err
	}
	copy := *capability
	copy.declared = reflect.TypeFor[T]()
	frame.slots[index].capability = &copy
	return nil
}
