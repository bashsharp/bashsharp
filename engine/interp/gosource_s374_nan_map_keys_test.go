//go:build full

package interp_test

import "testing"

func TestS374NaNMapKeysDifferential(t *testing.T) {
	s374SyncDifferential(t, `package main
import ("fmt"; "math")
func main() {
 f32 := math.Float32frombits(0x7f800001)
 f64 := math.Float64frombits(0xfff0000000000001)
 m32 := map[float32]int{f32: 1, float32(1.5): 2}
 m64 := map[float64]int{f64: 3, math.Inf(1): 4}
 m32[f32] = 5
 m64[f64] = 6
 fmt.Println(len(m32), m32[f32], m32[1.5], len(m64), m64[f64], m64[math.Inf(1)])
 for k := range m32 { if math.IsNaN(float64(k)) { fmt.Printf("32 %08x\n", math.Float32bits(k)) } }
 for k := range m64 { if math.IsNaN(k) { fmt.Printf("64 %016x\n", math.Float64bits(k)) } }
}`)
}
