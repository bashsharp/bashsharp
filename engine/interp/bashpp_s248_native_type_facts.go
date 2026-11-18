package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8
//
// Session-scoped native type facts.
//
// Two native type operations answer facts that are fixed for the lifetime of
// one dependency session:
//
//   - "type" resolves a bridge type identity in the worker's registry. A
//     session's imports, local types, embeds, companions and instantiations
//     are immutable (begin refuses any change), and the worker's registry
//     only grows, so an identity that resolved once keeps resolving to the
//     same type. A program that validates `new(big.Int)` or a `*big.Int`
//     element type in a loop paid one round trip per evaluation.
//   - "assignable" of a native handle answers reflect's AssignableTo for the
//     handle's authenticated dynamic type (the worker-issued type token the
//     Sprint 243 interface-admission cache already keys on) against the
//     destination identity. Both halves are fixed, so the answer is too.
//
// Only successful answers are remembered, per session, exactly as the
// interface-admission cache does: an error or a refusal stays a live
// round trip with its live diagnostic, a new session starts empty, and the
// registry-change refusal in begin still runs before every cache hit.

import (
	"fmt"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPNativeTypeFactKey struct {
	op       string
	selector string
	source   goSourceNativeAdmissionKey
}

// bashPPNativeTypeFactKeyFor reports the cache key of a type operation, or
// false when the operation's answer is not a session-fixed fact.
func (s *bashPPNativeSession) nativeTypeFactKey(op, selector string, args []bashPPBridgeValue) (bashPPNativeTypeFactKey, bool) {
	if s == nil || selector == "" {
		return bashPPNativeTypeFactKey{}, false
	}
	switch op {
	case "type":
		if len(args) != 0 {
			return bashPPNativeTypeFactKey{}, false
		}
		return bashPPNativeTypeFactKey{op: op, selector: selector}, true
	case "assignable":
		if len(args) != 1 {
			return bashPPNativeTypeFactKey{}, false
		}
		source, ok := s.nativeAdmissionKey(args[0], selector)
		if !ok {
			return bashPPNativeTypeFactKey{}, false
		}
		return bashPPNativeTypeFactKey{op: op, selector: selector, source: source}, true
	}
	return bashPPNativeTypeFactKey{}, false
}

// cachedNativeTypeFact answers a remembered fact after the same admission
// checks a live request makes before it writes: cancellation, the immutable
// registry, and a session that is still running.
func (r *Runner) cachedNativeTypeFact(req bashPPEvalRequest, key bashPPNativeTypeFactKey) (bashPPBridgeValue, bool, error) {
	s := req.Bridge
	if s == nil || s.conn == nil {
		return bashPPBridgeValue{}, false, nil
	}
	s.mu.Lock()
	value, ok := s.typeFacts[key]
	s.mu.Unlock()
	if !ok {
		return bashPPBridgeValue{}, false, nil
	}
	if err := r.ectx.Err(); err != nil {
		return bashPPBridgeValue{}, false, nil
	}
	if err := s.begin(r.ectx, req); err != nil {
		return bashPPBridgeValue{}, false, err
	}
	select {
	case <-s.done:
		// A finished session answers through the live path, which owns the
		// program-exit diagnostics.
		return bashPPBridgeValue{}, false, nil
	default:
	}
	return value, true, nil
}

func (s *bashPPNativeSession) rememberNativeTypeFact(key bashPPNativeTypeFactKey, value bashPPBridgeValue) {
	if !bashPPNativeTypeFactCacheable(key, value) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.typeFacts == nil {
		s.typeFacts = make(map[bashPPNativeTypeFactKey]bashPPBridgeValue)
	}
	s.typeFacts[key] = value
}

// A fact is remembered only as a plain value: never a handle, callback or
// origin that names session storage, and for assignability only the
// admitted (true) answer.
func bashPPNativeTypeFactCacheable(key bashPPNativeTypeFactKey, value bashPPBridgeValue) bool {
	if value.Handle != 0 || value.Origin != 0 || value.Callbacks || value.Function ||
		len(value.Elements) != 0 || len(value.Fields) != 0 || len(value.Entries) != 0 {
		return false
	}
	switch key.op {
	case "type":
		return value.Kind == "string"
	case "assignable":
		return value.Kind == "bool" && value.Text == "true"
	}
	return false
}

// bashPPNativeScalarEqual decides a native comparison locally when both
// operands are plain integer or boolean values of one identical predeclared
// type. The worker would rebuild each with reflect.New(type).Elem() from its
// decimal text (an "int" or "uint" spelling only chooses the parse sign; the
// type decides the value) and compare with reflect.Value.Equal, which for
// these kinds is value equality. Anything else — floats, strings, named or
// interface types, handles, a text that is not the canonical in-range
// spelling the worker would accept — is not handled and takes the dependency
// round trip, so every diagnostic stays the worker's.
func bashPPNativeScalarEqual(lv, rv bashPPBridgeValue) (equal, handled bool) {
	plain := func(v bashPPBridgeValue) bool {
		return v.Interface == "" && v.Handle == 0 && v.Origin == 0 && v.Session == "" &&
			!v.Callbacks && !v.Function && v.NativeTypeID == 0 &&
			len(v.Elements) == 0 && len(v.Fields) == 0 && len(v.Entries) == 0
	}
	if !plain(lv) || !plain(rv) || lv.Type != rv.Type {
		return false, false
	}
	if lv.Type == "bool" {
		if lv.Kind != "bool" || rv.Kind != "bool" {
			return false, false
		}
		l, lok := bashPPCanonicalBool(lv.Text)
		r, rok := bashPPCanonicalBool(rv.Text)
		if !lok || !rok {
			return false, false
		}
		return l == r, true
	}
	bits, signed, ok := bashPPPredeclaredIntType(lv.Type)
	if !ok {
		return false, false
	}
	l, lok := bashPPCanonicalInteger(lv, bits, signed)
	r, rok := bashPPCanonicalInteger(rv, bits, signed)
	if !lok || !rok {
		return false, false
	}
	return l == r, true
}

// bashPPIntegerValue is an integer that fits its declared type: an int64 for
// a signed type, a uint64 for an unsigned one.
type bashPPIntegerValue struct {
	signed int64
	plain  uint64
}

func bashPPCanonicalInteger(v bashPPBridgeValue, bits int, signed bool) (bashPPIntegerValue, bool) {
	var n bashPPIntegerValue
	switch v.Kind {
	case "int":
		i, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil || strconv.FormatInt(i, 10) != v.Text {
			return n, false
		}
		if signed {
			if bits < 64 && (i < -(1<<(bits-1)) || i > 1<<(bits-1)-1) {
				return n, false
			}
			n.signed = i
			return n, true
		}
		if i < 0 || bits < 64 && uint64(i) > 1<<bits-1 {
			return n, false
		}
		n.plain = uint64(i)
		return n, true
	case "uint":
		u, err := strconv.ParseUint(v.Text, 10, 64)
		if err != nil || strconv.FormatUint(u, 10) != v.Text {
			return n, false
		}
		if signed {
			if u > 1<<(bits-1)-1 {
				return n, false
			}
			n.signed = int64(u)
			return n, true
		}
		if bits < 64 && u > 1<<bits-1 {
			return n, false
		}
		n.plain = u
		return n, true
	}
	return n, false
}

func bashPPCanonicalBool(text string) (bool, bool) {
	switch text {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// bashPPPredeclaredIntType names the width and signedness of a predeclared
// integer type as the worker spells it (reflect's String of the type).
func bashPPPredeclaredIntType(name string) (bits int, signed, ok bool) {
	switch name {
	case "int":
		return strconv.IntSize, true, true
	case "int8":
		return 8, true, true
	case "int16":
		return 16, true, true
	case "int32":
		return 32, true, true
	case "int64":
		return 64, true, true
	case "uint", "uintptr":
		return strconv.IntSize, false, true
	case "uint8":
		return 8, false, true
	case "uint16":
		return 16, false, true
	case "uint32":
		return 32, false, true
	case "uint64":
		return 64, false, true
	}
	return 0, false, false
}

// bashPPNativeTypeFactRequest is bashPPNativeTypeRequest's fact-aware form.
func (r *Runner) bashPPNativeTypeFactRequest(req bashPPEvalRequest, op string, typ syntax.BashPPTypeExpr, args []bashPPBridgeValue) (bashPPBridgeValue, error) {
	selector := r.bashPPBridgeTypeIdentity(typ)
	key, factual := req.Bridge.nativeTypeFactKey(op, selector, args)
	if factual {
		value, ok, err := r.cachedNativeTypeFact(req, key)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		if ok {
			return value, nil
		}
	}
	values, err := r.bashPPNativeRequest(r.ectx, req, bashPPBridgeRequest{Op: op, Selector: selector, Args: args})
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	if len(values) != 1 {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: native type operation returned %d values", len(values))
	}
	if factual {
		req.Bridge.rememberNativeTypeFact(key, values[0])
	}
	return values[0], nil
}
