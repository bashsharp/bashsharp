package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8

import (
	"math"
	"reflect"
	"strconv"
	"testing"
)

// The local comparison answers exactly what the worker's decode +
// reflect.Value.Equal answers, and declines (handled=false) every operand the
// worker would reject or that is not a plain predeclared integer or bool.
func TestS248NativeScalarEqualMatchesWorker(t *testing.T) {
	v := func(kind, typ, text string) bashPPBridgeValue {
		return bashPPBridgeValue{Kind: kind, Type: typ, Text: text}
	}
	cases := []struct {
		name          string
		l, r          bashPPBridgeValue
		equal, handle bool
	}{
		{"uint result vs untyped one", v("uint", "uint", "1"), v("int", "uint", "1"), true, true},
		{"uint result vs untyped zero", v("uint", "uint", "0"), v("int", "uint", "1"), false, true},
		{"int sign zero", v("int", "int", "0"), v("int", "int", "0"), true, true},
		{"int negative", v("int", "int", "-1"), v("int", "int", "1"), false, true},
		{"int8 min", v("int", "int8", "-128"), v("int", "int8", "-128"), true, true},
		{"int8 max vs uint spelling", v("int", "int8", "127"), v("uint", "int8", "127"), true, true},
		{"uint64 max", v("uint", "uint64", "18446744073709551615"), v("uint", "uint64", "18446744073709551615"), true, true},
		{"int64 min", v("int", "int64", "-9223372036854775808"), v("int", "int64", "-9223372036854775807"), false, true},
		{"uintptr", v("uint", "uintptr", "4096"), v("int", "uintptr", "4096"), true, true},
		{"bool", v("bool", "bool", "true"), v("bool", "bool", "false"), false, true},
		{"bool equal", v("bool", "bool", "false"), v("bool", "bool", "false"), true, true},

		// Declined: the worker would reject, or the value is not plain.
		{"int8 overflow", v("int", "int8", "128"), v("int", "int8", "0"), false, false},
		{"uint8 overflow", v("int", "uint8", "256"), v("int", "uint8", "0"), false, false},
		{"negative unsigned", v("int", "uint", "-1"), v("uint", "uint", "1"), false, false},
		{"int64 from too-large uint", v("uint", "int64", "9223372036854775808"), v("int", "int64", "0"), false, false},
		{"leading zero", v("int", "int", "01"), v("int", "int", "1"), false, false},
		{"plus sign", v("int", "int", "+1"), v("int", "int", "1"), false, false},
		{"bool spelled 1", v("bool", "bool", "1"), v("bool", "bool", "true"), false, false},
		{"float", v("float", "float64", "1"), v("float", "float64", "1"), false, false},
		{"string", v("string", "string", "a"), v("string", "string", "a"), false, false},
		{"named type", v("int", "time.Duration", "1"), v("int", "time.Duration", "1"), false, false},
		{"type mismatch", v("int", "int", "1"), v("int", "int64", "1"), false, false},
		{"untyped", v("int", "", "1"), v("int", "", "1"), false, false},
		{"interface marker", bashPPBridgeValue{Kind: "int", Type: "int", Text: "1", Interface: "any"}, v("int", "int", "1"), false, false},
		{"handle", bashPPBridgeValue{Kind: "handle", Type: "int", Handle: 3}, v("int", "int", "1"), false, false},
		{"session value", bashPPBridgeValue{Kind: "int", Type: "int", Text: "1", Session: "s"}, v("int", "int", "1"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			equal, handled := bashPPNativeScalarEqual(c.l, c.r)
			if handled != c.handle || handled && equal != c.equal {
				t.Fatalf("equal=%v handled=%v; want equal=%v handled=%v", equal, handled, c.equal, c.handle)
			}
			if !handled {
				return
			}
			// Cross-check against the worker's own decode rule.
			typ := map[string]reflect.Type{
				"int": reflect.TypeFor[int](), "int8": reflect.TypeFor[int8](), "int64": reflect.TypeFor[int64](),
				"uint": reflect.TypeFor[uint](), "uint64": reflect.TypeFor[uint64](), "uintptr": reflect.TypeFor[uintptr](),
				"bool": reflect.TypeFor[bool](),
			}[c.l.Type]
			decode := func(b bashPPBridgeValue) reflect.Value {
				out := reflect.New(typ).Elem()
				switch b.Kind {
				case "bool":
					x, _ := strconv.ParseBool(b.Text)
					out.SetBool(x)
				case "int":
					n, _ := strconv.ParseInt(b.Text, 10, 64)
					if typ.Kind() >= reflect.Uint && typ.Kind() <= reflect.Uintptr {
						out.SetUint(uint64(n))
					} else {
						out.SetInt(n)
					}
				case "uint":
					n, _ := strconv.ParseUint(b.Text, 10, 64)
					if typ.Kind() >= reflect.Uint && typ.Kind() <= reflect.Uintptr {
						out.SetUint(n)
					} else if n <= math.MaxInt64 {
						out.SetInt(int64(n))
					}
				}
				return out
			}
			if want := decode(c.l).Equal(decode(c.r)); want != equal {
				t.Fatalf("reflect.Value.Equal answers %v, local answers %v", want, equal)
			}
		})
	}
}

// Only session-fixed successful answers are remembered, and never a value
// that names session storage.
func TestS248NativeTypeFactRules(t *testing.T) {
	s := &bashPPNativeSession{id: "s", handleTypes: map[uint64]uint64{9: 42}}
	if _, ok := s.nativeTypeFactKey("type", "math/big.Int", nil); !ok {
		t.Fatal("a type resolution is a session fact")
	}
	if _, ok := s.nativeTypeFactKey("type", "", nil); ok {
		t.Fatal("an empty identity is not a fact")
	}
	for _, op := range []string{"new", "construct", "equal", "channel-bind", "call"} {
		if _, ok := s.nativeTypeFactKey(op, "math/big.Int", nil); ok {
			t.Fatalf("%s allocates or observes values; never a fact", op)
		}
	}
	handle := bashPPBridgeValue{Kind: "handle", Handle: 9, Session: "s"}
	key, ok := s.nativeTypeFactKey("assignable", "*math/big.Int", []bashPPBridgeValue{handle})
	if !ok || key.source.source != 42 || key.source.destination != "*math/big.Int" {
		t.Fatalf("handle assignability keys on the authenticated type token: %+v %v", key, ok)
	}
	for name, arg := range map[string]bashPPBridgeValue{
		"scalar":          {Kind: "int", Type: "int", Text: "1"},
		"unknown handle":  {Kind: "handle", Handle: 10, Session: "s"},
		"foreign session": {Kind: "handle", Handle: 9, Session: "t"},
	} {
		if _, ok := s.nativeTypeFactKey("assignable", "*math/big.Int", []bashPPBridgeValue{arg}); ok {
			t.Fatalf("%s assignability is not a cached fact", name)
		}
	}

	typeKey := bashPPNativeTypeFactKey{op: "type", selector: "math/big.Int"}
	if !bashPPNativeTypeFactCacheable(typeKey, bashPPBridgeValue{Kind: "string", Type: "string", Text: "math/big.Int"}) {
		t.Fatal("a resolved type identity is cacheable")
	}
	if bashPPNativeTypeFactCacheable(typeKey, bashPPBridgeValue{Kind: "handle", Handle: 1}) {
		t.Fatal("a handle is never cacheable")
	}
	if !bashPPNativeTypeFactCacheable(key, bashPPBridgeValue{Kind: "bool", Type: "bool", Text: "true"}) {
		t.Fatal("an admitted assignability is cacheable")
	}
	if bashPPNativeTypeFactCacheable(key, bashPPBridgeValue{Kind: "bool", Type: "bool", Text: "false"}) {
		t.Fatal("a refused assignability stays a live diagnostic")
	}

	// Remembering is per session and refuses uncacheable answers.
	s.rememberNativeTypeFact(key, bashPPBridgeValue{Kind: "bool", Type: "bool", Text: "false"})
	if len(s.typeFacts) != 0 {
		t.Fatal("a refusal was remembered")
	}
	s.rememberNativeTypeFact(key, bashPPBridgeValue{Kind: "bool", Type: "bool", Text: "true", Interface: "*math/big.Int"})
	if got := s.typeFacts[key]; got.Text != "true" || got.Interface != "*math/big.Int" {
		t.Fatalf("remembered %+v", got)
	}
	if other := (&bashPPNativeSession{id: "u"}); len(other.typeFacts) != 0 {
		t.Fatal("a new session starts empty")
	}
}
