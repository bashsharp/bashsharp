//go:build full

package interp_test

import (
	"testing"
)

// TestS374ByteArrayNativeMutationParity locks the wire-parity contract for
// byte arrays through native calls: reads and pointer writebacks must match
// the native oracle byte for byte, however the bridge spells the payload.
func TestS374ByteArrayNativeMutationParity(t *testing.T) {
	dep := `package dep
func Zero8(p *[8]byte) { for i := range p { p[i] = 0 } }
func Inc1024(p *[1024]byte) { for i := range p { p[i]++ } }
func Sum8(p [8]byte) int { n := 0; for _, b := range p { n += int(b) }; return n }
func Sum1024(p [1024]byte) int { n := 0; for _, b := range p { n += int(b) }; return n }`
	main := `package main
import (
	"fmt"

	"example.com/bytedep/dep"
)
func main() {
	var a [8]byte
	for i := range a {
		a[i] = byte(i + 1)
	}
	fmt.Println(dep.Sum8(a))
	dep.Zero8(&a)
	fmt.Println(dep.Sum8(a), a[3])
	var big [1024]byte
	for i := range big {
		big[i] = 1
	}
	fmt.Println(dep.Sum1024(big))
	dep.Inc1024(&big)
	s := 0
	for _, b := range big {
		s += int(b)
	}
	fmt.Println(s, big[0], big[1023])
}`
	differGoSourceDependencyModule(t, "example.com/bytedep", dep, main)
}

// TestS374MemStatsHostHeapParity proves the program's heap oracle observes
// the heap its objects live on: a fresh megabyte is visible, and dropping
// it below a megabyte after collection is visible too.
func TestS374MemStatsHostHeapParity(t *testing.T) {
	differGoSource(t, `package main
import (
	"fmt"
	"runtime"
)
func inuse() int64 {
	runtime.GC()
	var st runtime.MemStats
	runtime.ReadMemStats(&st)
	return int64(st.Alloc)
}
func main() {
	start := inuse()
	x := new([1 << 20]byte)
	fmt.Println(inuse()-start > 1<<19)
	runtime.KeepAlive(x)
	x = nil
	fmt.Println(inuse()-start < 1<<20)
	fmt.Println(x == nil)
}
`, nil, "")
}
