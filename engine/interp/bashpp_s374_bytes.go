package interp

import (
	"reflect"
	"runtime"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #374; Story: #1540; Story-ID: 61179c258609
//
// Compact byte arrays and the program's heap oracle.
//
// A transported [N]byte used to cross as one bridge value per element, so a
// 10 MB buffer became gigabytes of JSON, decoded garbage and pointee
// snapshots on the worker. Byte arrays now cross in compact form
// ({Kind:"bytes"} with the raw image); only the element type being uint8
// selects it, anything else falls back to the per-element path, and the
// worker decodes, snapshots and compares the raw image without rendering.
// Nested byte arrays inside structs keep the structural spelling on the
// worker side; both spellings decode on both sides.
//
// Scalar field reads on a runtime.MemStats handle observe the heap the
// interpreted program's objects live on. Transported copies are
// bridge-internal working storage (as kernel buffers are to a real
// process), so filling the program's heap oracle from the helper heap made
// explicit GC observations meaningless: an allocation the helper never saw
// could never appear, and a dropped buffer could never disappear.

// bashPPByteElementType answers whether typ's underlying element is a
// byte: only then may a sequence cross in compact form.
func bashPPByteElementType(r *Runner, typ syntax.BashPPTypeExpr) bool {
	switch bashPPTypeText(r.bashPPUnderlyingType(typ)) {
	case "uint8", "byte":
		return true
	}
	return false
}

// bashPPByteArrayBytes extracts the raw image of byte-array storage. Every
// storable byte spelling is accepted; anything else answers false so the
// caller falls back to the per-element path instead of corrupting.
func bashPPByteArrayBytes(value []any) ([]byte, bool) {
	raw := make([]byte, 0, len(value))
	for _, item := range value {
		switch item := item.(type) {
		case string:
			n, err := strconv.ParseUint(item, 10, 8)
			if err != nil {
				return nil, false
			}
			raw = append(raw, byte(n))
		case int:
			if item < 0 || item > 255 {
				return nil, false
			}
			raw = append(raw, byte(item))
		case int64:
			if item < 0 || item > 255 {
				return nil, false
			}
			raw = append(raw, byte(item))
		case uint64:
			if item > 255 {
				return nil, false
			}
			raw = append(raw, byte(item))
		case uint8:
			raw = append(raw, item)
		default:
			return nil, false
		}
	}
	return raw, true
}

// bashPPHostMemStatsMember answers a scalar field read on a MemStats
// handle from the host runtime. The program's heap oracle must observe the
// heap its objects live on: transported copies are bridge-internal working
// storage (as kernel buffers are to a real process), so filling the oracle
// from the helper heap made explicit GC observations meaningless. The match
// is on the dependency-attested wire spelling of the handle (typeID is the
// package path plus the name); a handle from another session, an unknown
// field, or a non-scalar field falls through to the ordinary worker path.
// Field writes are not intercepted: MemStats is a snapshot struct, and no
// in-scope program writes its fields.
func bashPPHostMemStatsMember(sessionID string, recv bashPPBridgeValue, field string) (bashPPBridgeValue, bool) {
	if recv.Kind != "handle" || recv.Type != "runtime.MemStats" {
		return bashPPBridgeValue{}, false
	}
	if recv.Session != "" && recv.Session != sessionID {
		return bashPPBridgeValue{}, false
	}
	var st runtime.MemStats
	runtime.ReadMemStats(&st)
	return bashPPMemStatsScalar(st, field)
}

// bashPPMemStatsScalar converts one field from a single heap snapshot.
func bashPPMemStatsScalar(st runtime.MemStats, field string) (bashPPBridgeValue, bool) {
	fv := reflect.ValueOf(st).FieldByName(field)
	if !fv.IsValid() {
		return bashPPBridgeValue{}, false
	}
	out := bashPPBridgeValue{Type: fv.Type().String()}
	switch fv.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		out.Kind = "uint"
		out.Text = strconv.FormatUint(fv.Uint(), 10)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out.Kind = "int"
		out.Text = strconv.FormatInt(fv.Int(), 10)
	case reflect.Float32, reflect.Float64:
		out.Kind = "float"
		out.Text = strconv.FormatFloat(fv.Float(), 'g', -1, 64)
	case reflect.Bool:
		out.Kind = "bool"
		out.Text = strconv.FormatBool(fv.Bool())
	case reflect.String:
		out.Kind = "string"
		out.Text = fv.String()
		out.Bytes = []byte(fv.String())
	default:
		return bashPPBridgeValue{}, false
	}
	return out, true
}
