package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8

import (
	"go/constant"
	"go/token"
	"math"
	"math/rand"
	"testing"
)

// The int64 fast path of bashPPWrapInteger answers exactly what the math/big
// path answers — same numeric value and same constant kind — for every
// integer width and signedness, at the edges and at random.
func TestS248WrapIntegerFastPathIsExact(t *testing.T) {
	types := []string{"int", "int8", "int16", "int32", "int64", "rune", "uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr"}
	var inputs []int64
	for _, edge := range []int64{0, 1, -1, 2, -2, 127, 128, -128, -129, 255, 256, -256, 32767, 32768, -32768, -32769, 65535, 65536,
		math.MaxInt32, math.MaxInt32 + 1, math.MinInt32, math.MinInt32 - 1, math.MaxUint32, math.MaxUint32 + 1,
		math.MaxInt64, math.MinInt64, math.MaxInt64 - 1, math.MinInt64 + 1} {
		inputs = append(inputs, edge)
	}
	rng := rand.New(rand.NewSource(248))
	for range 2000 {
		inputs = append(inputs, int64(rng.Uint64()), rng.Int63n(1<<20)-1<<19)
	}
	for _, typ := range types {
		bits, signed := bashPPIntegerWidth(typ)
		for _, in := range inputs {
			value := constant.MakeInt64(in)
			fast, ok := bashPPWrapInt64(bits, signed, value)
			if !ok {
				t.Fatalf("%s %d: an int64 integer takes the fast path", typ, in)
			}
			exact := bashPPWrapIntegerExact(bits, signed, value)
			if fast.Kind() != exact.Kind() || !constant.Compare(fast, token.EQL, exact) {
				t.Fatalf("%s %d: fast %s (%v), exact %s (%v)", typ, in, fast.ExactString(), fast.Kind(), exact.ExactString(), exact.Kind())
			}
			if got := bashPPWrapInteger(typ, value); !constant.Compare(got, token.EQL, exact) {
				t.Fatalf("%s %d: wrap %s, exact %s", typ, in, got.ExactString(), exact.ExactString())
			}
		}
	}
	// Values outside int64 and non-integer kinds keep the exact path.
	huge := constant.Shift(constant.MakeInt64(1), token.SHL, 70)
	if _, ok := bashPPWrapInt64(64, false, huge); ok {
		t.Fatal("a value beyond int64 must not take the fast path")
	}
	if got := bashPPWrapInteger("uint64", constant.BinaryOp(huge, token.ADD, constant.MakeInt64(5))); !constant.Compare(got, token.EQL, constant.MakeInt64(5)) {
		t.Fatalf("2^70+5 wraps to 5 in uint64, got %s", got.ExactString())
	}
	integral := constant.MakeFloat64(300)
	if _, ok := bashPPWrapInt64(8, false, integral); ok {
		t.Fatal("a float-kind constant keeps the exact path")
	}
	if got := bashPPWrapInteger("uint8", integral); !constant.Compare(got, token.EQL, constant.MakeInt64(44)) {
		t.Fatalf("integral float 300 wraps to 44 in uint8 on the exact path, got %s", got.ExactString())
	}
}

func BenchmarkS248WrapInteger(b *testing.B) {
	v := constant.MakeInt64(1 << 40)
	for i := 0; i < b.N; i++ {
		bashPPWrapInteger("int32", v)
	}
}
