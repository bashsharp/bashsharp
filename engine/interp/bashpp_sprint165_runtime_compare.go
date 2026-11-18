// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// bashPPBridgeCompareOperands gives an untyped constant operand of a native
// comparison the other operand's type. `math.Pow(2, 3) != 8` compares a
// float64 with the untyped constant 8, which Go converts to float64 before
// comparing; the constant crossed the bridge as a bare int and the
// dependency compared an int with a float64. The checker has already
// established that the constant is representable in that type, so the
// conversion is Go's, not a permissive one; a typed operand is never
// rewritten.
func bashPPBridgeCompareOperands(lv, rv bashPPBridgeValue) (bashPPBridgeValue, bashPPBridgeValue) {
	untyped := func(v bashPPBridgeValue) bool {
		if v.Type != "" || v.Interface != "" || v.Session != "" {
			return false
		}
		switch v.Kind {
		case "int", "uint", "float", "complex", "string", "bool":
			return true
		}
		return false
	}
	typed := func(v bashPPBridgeValue) bool {
		return v.Type != "" && v.Interface == "" && v.Kind != "nil" && v.Kind != "handle"
	}
	switch {
	case untyped(lv) && typed(rv):
		lv.Type = rv.Type
	case untyped(rv) && typed(lv):
		rv.Type = lv.Type
	}
	return lv, rv
}
