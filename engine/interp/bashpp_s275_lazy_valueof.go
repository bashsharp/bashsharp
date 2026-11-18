package interp

// Sprint: #275; Story: #758; Story-ID: 2577207b59b5
//
// Lazily answered reflect.ValueOf inside a routed callback.
//
// A reflect.MakeFunc implementation typically ends with
//
//	return []reflect.Value{reflect.ValueOf(A{1, 2})}
//
// and runs, in the interpreter, once per call of the made function. The
// ValueOf was a helper round trip per call, only for its handle to cross
// straight back in the callback reply.
//
// ValueOf of a plain value — scalars, structs and arrays of them, with no
// handle, pointer, callback, map or slice anywhere — copies that value; the
// resulting reflect.Value is neither addressable nor settable, so two ValueOf
// calls of equal operands cannot be told apart. Once a live ValueOf of an
// operand shape has succeeded in a session (proving the helper resolves and
// decodes that shape), later ValueOf calls of the same shape inside a routed
// callback are answered with the recorded reply shape and a local-reflect
// descriptor carrying the operand as encoded at the call:
//
//   - any request that uses the value materializes it by replaying the
//     ValueOf exactly as issued (goSourceLocalReflectMaterialize), once;
//   - a callback reply that returns it unmaterialized sends the operand
//     in-band as a "valueof" value, and the helper applies reflect.ValueOf
//     to it at the same interface parameter type the live call decodes at.
//
// Outside a routed callback every ValueOf stays a live round trip.

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// goSourceLazyValueOfReplayKey marks the context of a lazy ValueOf's replay,
// so the replay itself reaches the helper.
type goSourceLazyValueOfReplayKey struct{}

// goSourceValueOfOperand reports the operand shape of a reflect.ValueOf
// request eligible for a lazy answer.
func (r *Runner) goSourceValueOfOperand(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) (string, bool) {
	if !r.bashPPGoSource || req.Bridge == nil || req.CallbackOwner == nil || req.CallbackOwner.bashPPTools.routedDepth == 0 {
		return "", false
	}
	if q.Op != "call" || q.Receiver != nil || q.Spread || len(q.Args) != 1 || q.Instance != "" || q.LogPrint != "" ||
		len(q.SliceBuffers) != 0 || len(q.Transfers) != 0 || q.coherence != nil || len(q.Values) != 0 {
		return "", false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || name != "ValueOf" || req.Imports[alias] != "reflect" {
		return "", false
	}
	if replaying, _ := ctx.Value(goSourceLazyValueOfReplayKey{}).(bool); replaying {
		return "", false
	}
	var shape strings.Builder
	if !bashPPPlainOperandShape(q.Args[0], &shape) {
		return "", false
	}
	return shape.String(), true
}

// bashPPPlainOperandShape writes the shape of a plain value and reports
// whether it is one.
func bashPPPlainOperandShape(v bashPPBridgeValue, shape *strings.Builder) bool {
	if v.Origin != 0 || v.Handle != 0 || v.Session != "" || v.Callbacks || v.Function || v.NativeTypeID != 0 || v.NativeType != "" ||
		v.Callable != "" || v.NilChannel || v.Bytes != nil || len(v.Entries) != 0 || len(v.CallArgs) != 0 ||
		len(v.ReaderBuffer) != 0 || v.ReaderLength != 0 || v.localReflect != nil || v.localCell != nil || v.sliceView != nil ||
		v.reflectFunc != nil || v.reflectFunction || v.callRefusal != "" || v.reflectCopy || v.deferredNativeComposite || v.copiedResults {
		return false
	}
	switch v.Kind {
	case "string", "bool", "int", "uint", "float", "complex":
		if len(v.Elements) != 0 || len(v.Fields) != 0 {
			return false
		}
	case "struct", "array":
	default:
		return false
	}
	shape.WriteString(v.Kind)
	shape.WriteString(strconv.Quote(v.Type))
	shape.WriteString(strconv.Quote(v.Interface))
	shape.WriteByte('[')
	for _, e := range v.Elements {
		if !bashPPPlainOperandShape(e, shape) {
			return false
		}
		shape.WriteByte(',')
	}
	shape.WriteByte(']')
	names := make([]string, 0, len(v.Fields))
	for name := range v.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	shape.WriteByte('{')
	for _, name := range names {
		shape.WriteString(strconv.Quote(name))
		if !bashPPPlainOperandShape(v.Fields[name], shape) {
			return false
		}
		shape.WriteByte(',')
	}
	shape.WriteByte('}')
	return true
}

// goSourceRememberValueOf records the reply of a live eligible ValueOf as the
// template for its operand shape.
func (r *Runner) goSourceRememberValueOf(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest, values []bashPPBridgeValue) {
	shape, ok := r.goSourceValueOfOperand(ctx, req, q)
	if !ok || len(values) != 1 {
		return
	}
	s, v := req.Bridge, values[0]
	if v.Kind != "handle" || v.Session != s.id || v.Handle == 0 || v.NativeTypeID == 0 || v.Callbacks || v.Function || v.Origin != 0 ||
		v.reflectFunction || v.reflectFunc != nil || v.callRefusal != "" || v.localReflect != nil || v.localCell != nil ||
		len(v.Elements) != 0 || len(v.Fields) != 0 || len(v.Entries) != 0 {
		return
	}
	v.Handle = 0
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.valueOfReplies == nil {
		s.valueOfReplies = make(map[string]bashPPBridgeValue)
	}
	if _, seen := s.valueOfReplies[shape]; !seen {
		s.valueOfReplies[shape] = v
	}
}

// goSourceLazyValueOf answers an eligible ValueOf whose operand shape already
// succeeded live, after the admission checks a live request makes before it
// writes: cancellation, the immutable registry, a session still running.
func (r *Runner) goSourceLazyValueOf(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) (bashPPBridgeValue, bool) {
	shape, ok := r.goSourceValueOfOperand(ctx, req, q)
	if !ok {
		return bashPPBridgeValue{}, false
	}
	s := req.Bridge
	s.mu.Lock()
	value, ok := s.valueOfReplies[shape]
	conn := s.conn
	s.mu.Unlock()
	if !ok || conn == nil || ctx.Err() != nil {
		return bashPPBridgeValue{}, false
	}
	if err := s.begin(ctx, req); err != nil {
		// The live path reports the refusal.
		return bashPPBridgeValue{}, false
	}
	select {
	case <-s.done:
		return bashPPBridgeValue{}, false
	default:
	}
	value.Handle = goSourceLocalReflectHandle()
	value.localReflect = &goSourceLocalReflect{
		valueOf: bashPPBridgeRequest{Op: q.Op, Selector: q.Selector, Args: slices.Clone(q.Args), SourceFile: q.SourceFile, SourceLine: q.SourceLine},
		operand: true,
	}
	bashPPMarkReflectCopy(&value, reflectedCopyDerived(req, q))
	s.rememberNativeHandleType(value)
	return value, true
}

// bashPPWireLazyValueOf rewrites the lazy ValueOf results a callback returns
// for the wire: a materialized one as its helper handle, any other as the
// in-band "valueof" of its operand.
func (s *bashPPNativeSession) bashPPWireLazyValueOf(values []bashPPBridgeValue) []bashPPBridgeValue {
	var out []bashPPBridgeValue
	for i, v := range values {
		if wire, changed := bashPPWireLazyValue(v); changed {
			if out == nil {
				out = slices.Clone(values)
			}
			out[i] = wire
		}
	}
	if out == nil {
		return values
	}
	return out
}

func bashPPWireLazyValue(v bashPPBridgeValue) (bashPPBridgeValue, bool) {
	if lr := v.localReflect; lr != nil {
		if !lr.operand {
			return v, false
		}
		lr.mu.Lock()
		handle := lr.handle
		lr.mu.Unlock()
		if handle != nil {
			return *handle, true
		}
		return bashPPBridgeValue{Kind: "valueof", Elements: []bashPPBridgeValue{lr.valueOf.Args[0]}}, true
	}
	changed := false
	var elements []bashPPBridgeValue
	for i, e := range v.Elements {
		if wire, ok := bashPPWireLazyValue(e); ok {
			if elements == nil {
				elements = slices.Clone(v.Elements)
			}
			elements[i], changed = wire, true
		}
	}
	var fields map[string]bashPPBridgeValue
	for name, f := range v.Fields {
		if wire, ok := bashPPWireLazyValue(f); ok {
			if fields == nil {
				fields = make(map[string]bashPPBridgeValue, len(v.Fields))
				for k, x := range v.Fields {
					fields[k] = x
				}
			}
			fields[name], changed = wire, true
		}
	}
	var entries []bashPPBridgeEntry
	for i, e := range v.Entries {
		key, keyChanged := bashPPWireLazyValue(e.Key)
		value, valueChanged := bashPPWireLazyValue(e.Value)
		if keyChanged || valueChanged {
			if entries == nil {
				entries = slices.Clone(v.Entries)
			}
			entries[i], changed = bashPPBridgeEntry{Key: key, Value: value}, true
		}
	}
	if !changed {
		return v, false
	}
	if elements != nil {
		v.Elements = elements
	}
	if fields != nil {
		v.Fields = fields
	}
	if entries != nil {
		v.Entries = entries
	}
	return v, true
}
