// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package shellrt

import (
	"reflect"
	"sort"
)

// A typed constant reaches the shell boundary as metadata over a value, never
// as storage the program can be made to write through. Go has no addressable
// constant, and demoting one to a `var` so the ordinary lexical registration
// path could take its address would make the refusal a convention rather than
// a property: any missed check would then silently succeed. So a constant is
// registered as a *shadow* slot — runtime-owned storage holding a copy —
// while the generated program keeps its real `const` declaration and reads it
// natively. The shadow exists for two jobs only: projecting the constant's
// shell text at a region boundary, and carrying the fact that the identity
// behind that text is constant.
//
// Registration is keyed by the compiler's resolved identity, not by spelling,
// so a constant shadowed by an inner declaration of the same name, or two
// same-spelled constants in sibling scopes, stay distinct bindings.

// ConstBinding is one typed constant's shell-boundary registration. Text is
// the exact shell spelling the declaration projects; SourceType is the source
// type spelling, which the boundary reports rather than Go's rendering of T.
type ConstBinding struct {
	ID, Name, SourceType, Text string
}

// RegisterConstant installs the shadow binding for one typed constant. value
// is passed by value: nothing here takes the address of the caller's constant,
// because a Go constant has none.
//
// Re-registering the same ID is idempotent while the registered value and
// source type agree, so a constant that is in scope across several shell
// regions is declared once rather than redeclared per region. A disagreeing
// re-registration is a compiler fault and is reported rather than absorbed.
func RegisterConstant[T any](b *LexicalBindings, id, name, sourceType string, value T, kind Kind) error {
	if b == nil {
		return &LexicalWriteError{name, "constant registration requires lexical bindings"}
	}
	info := LexicalInfo{SourceType: sourceType, Constant: true, Readonly: true}
	b.store.mu.Lock()
	if existing := b.store.slots[id]; existing != nil {
		err := func() error {
			if !existing.raw.info.Constant {
				return &LexicalWriteError{name, "cannot replace a variable binding with a constant"}
			}
			if existing.value.Type() != reflect.TypeFor[T]() || existing.kind != kind {
				return &LexicalWriteError{name, "inconsistent constant type or projection"}
			}
			if existing.raw.info != info || !reflect.DeepEqual(existing.value.Interface(), value) {
				return &LexicalWriteError{name, "constant re-registered with a different value"}
			}
			return nil
		}()
		b.store.mu.Unlock()
		if err != nil {
			return err
		}
		// Idempotent in the store is not idempotent in the view. A captured
		// view carries its own name table, so a second registration reaching
		// an already-known slot must still publish the name — otherwise the
		// constant exists but is invisible to visible(), and the boundary
		// stops projecting it after the first call.
		b.mu.Lock()
		b.names[name] = id
		b.mu.Unlock()
		return nil
	}
	storage := reflect.New(reflect.TypeFor[T]()).Elem()
	storage.Set(reflect.ValueOf(value))
	present := true
	// The shadow is deliberately not tracked in the address registry: a
	// constant has no source-visible address, so no pointer can alias it and
	// no native write can reach it.
	b.store.slots[id] = &lexicalSlot{value: storage, present: &present, kind: kind, name: name, raw: &lexicalRaw{info: info}}
	b.store.mu.Unlock()
	b.mu.Lock()
	b.names[name] = id
	b.mu.Unlock()
	return nil
}

// ConstantInfo reports the declaration metadata registered for a resolved
// identity. The shell boundary and the declaration policy both need to ask
// "is this identity constant" without re-deriving it from a name.
func (b *LexicalBindings) ConstantInfo(id string) (LexicalInfo, bool) {
	slot := b.slot(id)
	if slot == nil || !slot.raw.info.Constant {
		return LexicalInfo{}, false
	}
	return slot.raw.info, true
}

// Constants lists every constant visible in this view, in name order, with the
// shell text each one projects. It is the metadata a backend declaration
// policy is installed from, so that the engine — not a reimplementation here —
// owns the refusal wording and the per-statement control transfer.
//
// A constant whose value cannot be rendered as shell text is reported rather
// than skipped: dropping it would leave the backend policing a name it does
// not know is constant.
func (b *LexicalBindings) Constants() ([]ConstBinding, error) {
	slots := b.visible()
	names := make([]string, 0, len(slots))
	for name, slot := range slots {
		if slot.raw.info.Constant {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	b.mu.RLock()
	ids := make(map[string]string, len(names))
	for _, name := range names {
		ids[name] = b.names[name]
	}
	b.mu.RUnlock()
	bindings := make([]ConstBinding, 0, len(names))
	for _, name := range names {
		slot := slots[name]
		text, err := slot.shellText()
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, ConstBinding{ID: ids[name], Name: name, SourceType: slot.raw.info.SourceType, Text: text})
	}
	return bindings, nil
}
