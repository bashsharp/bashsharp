//go:build full

package interp_test

import "testing"

func TestS374NaNBitsDifferential(t *testing.T) {
	s374SyncDifferential(t, `package main
import ("fmt"; "math"; "reflect")
type F32 struct { V float32 }
type F64 struct { V float64 }
func through32(v float32) float32 { return v }
func through64(v float64) float64 { return v }
func main() {
 for _, bits := range []uint32{0x7f800001,0x7fbfffff,0xff800001,0xffbfffff,0x7fffffff,0x7fc12345,0x7f800000,0xff800000} {
  v := math.Float32frombits(bits)
  a := v; s := F32{v}; sl := []float32{v}
  fmt.Printf("32 %08x %08x %08x %08x %08x %08x\n",bits,math.Float32bits(a),math.Float32bits(s.V),math.Float32bits(sl[0]),math.Float32bits(through32(v)),math.Float32bits(reflect.ValueOf(v).Interface().(float32)))
  fmt.Printf("wide %08x %016x\n", bits, math.Float64bits(float64(v)))
 }
 for _, bits := range []uint64{0x7ff0000000000001,0x7ff7ffffffffffff,0xfff0000000000001,0xfff7ffffffffffff,0x7fffffffffffffff,0x7ff8123456789abc,0x7ff0000000000000,0xfff0000000000000} {
  v := math.Float64frombits(bits)
  a := v; s := F64{v}; sl := []float64{v}
  fmt.Printf("64 %016x %016x %016x %016x %016x %016x\n",bits,math.Float64bits(a),math.Float64bits(s.V),math.Float64bits(sl[0]),math.Float64bits(through64(v)),math.Float64bits(reflect.ValueOf(v).Interface().(float64)))
  fmt.Printf("narrow %016x %08x\n", bits, math.Float32bits(float32(v)))
 }
 fmt.Printf("convert %016x %08x\n", math.Float64bits(float64(math.Float32frombits(0x7fc12345))), math.Float32bits(float32(math.Float64frombits(0x7ff82468a0000000))))
}`)
}
