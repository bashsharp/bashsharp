//go:build full

package interp_test

import (
	"strings"
	"testing"
)

func TestS270G4UnsafeOpaquePointerReductions(t *testing.T) {
	t.Run("opaque pointer words preserve identity without host addresses", func(t *testing.T) {
		src := `package main
import "unsafe"
func main() {
	a := new(int)
	b := new(int)
	c := a
	ua := uintptr(unsafe.Pointer(a))
	ub := uintptr(unsafe.Pointer(b))
	uc := uintptr(unsafe.Pointer(c))
	if ua == 0 { panic("zero word") }
	if ua != uc { panic("same target mismatch") }
	if ua == ub { panic("different targets match") }
	if ua >= 1<<32 { panic("host-shaped address escaped") }
}`
		out, stderr, err := runGoSource(t, "s270-g4-opaque-identity", src)
		if err != nil || out != "" || stderr != "" {
			t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
		}
	})

	t.Run("bug246 forged integer pointers compare by address", func(t *testing.T) {
		src := `package main
import "unsafe"
func main() {
	addr := uintptr(0x234)
	x1 := (*int)(unsafe.Pointer(addr))
	x2 := (*int)(unsafe.Pointer(uintptr(0x234)))
	if x1 != x2 { panic("mismatch") }
}`
		out, stderr, err := runGoSource(t, "s270-g4-bug246", src)
		if err != nil || out != "" || stderr != "" {
			t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
		}
	})

	t.Run("issue19078 pointer uintptr arithmetic stays opaque", func(t *testing.T) {
		src := `package main
import "unsafe"
var sink interface{}
func main() {
	p := new([48]byte)
	sink = &p
	u := uintptr(unsafe.Pointer(p))
	u = u >> 13 << 13
	u += 1<<13 - 1
	_ = identity(u)
}
func identity(x uintptr) uintptr { return x }`
		out, stderr, err := runGoSource(t, "s270-g4-issue19078", src)
		if err != nil || out != "" || stderr != "" {
			t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
		}
	})

	t.Run("issue35073 reflect address results may be discarded as opaque", func(t *testing.T) {
		src := `package main
import (
	"reflect"
	"unsafe"
)
func main() {
	n := 10
	m := make(map[string]string)
	_ = unsafe.Pointer(reflect.ValueOf(&n).Elem().UnsafeAddr())
	_ = unsafe.Pointer(reflect.ValueOf(&m).Elem().Pointer())
}`
		out, stderr, err := runGoSource(t, "s270-g4-issue35073", src)
		if err != nil || out != "" || stderr != "" {
			t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
		}
	})

	t.Run("issue8004 slice header reinterpret may be retained opaquely", func(t *testing.T) {
		src := `package main
import (
	"reflect"
	"runtime"
	"unsafe"
)
type T struct {
	H *reflect.SliceHeader
	P *[]int
}
func main() {
	var all []interface{}
	for i := 0; i < 3; i++ {
		p := new([]int)
		*p = append(*p, 1, 2, 3, 4)
		h := (*reflect.SliceHeader)(unsafe.Pointer(p))
		all = append(all, h, p)
	}
	var typed []T
	for i := 0; i < 3; i++ {
		p := new([]int)
		*p = append(*p, 1, 2, 3, 4)
		h := (*reflect.SliceHeader)(unsafe.Pointer(p))
		typed = append(typed, T{H: h}, T{P: p})
	}
	runtime.GC()
	for i := 0; i < 3; i++ {
		p := *all[2*i+1].(*[]int)
		if p[0] != 1 || p[3] != 4 { panic("bad interface slice") }
		q := *typed[2*i+1].P
		if q[0] != 1 || q[3] != 4 { panic("bad typed slice") }
	}
}`
		// The header pointer is only stored, never read: the conversion is
		// legal on every architecture, and only an access through it would
		// need the layout (see gosource_s270_g4_view_test.go).
		out, stderr, err := runGoSource(t, "s270-g4-issue8004", src)
		if err != nil || out != "" || stderr != "" {
			t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
		}
	})
}

func TestS270G4UnsafeRawLayoutExclusionEvidence(t *testing.T) {
	t.Run("opaque uintptr round trip does not regain dereference authority", func(t *testing.T) {
		src := `package main
import "unsafe"
func main() {
	p := new(int)
	addr := uintptr(unsafe.Pointer(p))
	q := (*int)(unsafe.Pointer(addr))
	_ = *q
}`
		_, stderr, err := runGoSource(t, "s270-g4-opaque-forged-deref", src)
		if err == nil || !strings.Contains(err.Error()+stderr, "BASHPP-EUNSAFE-FORGED") {
			t.Fatalf("err=%v stderr=%q; want forged opaque-address refusal", err, stderr)
		}
	})

	t.Run("issue15329 reflect uintptr keeps its pointer origin", func(t *testing.T) {
		src := `package main
import (
	"reflect"
	"unsafe"
)
func main() {
	x := 1
	p := unsafe.Pointer(reflect.ValueOf(&x).Pointer())
	_ = *(*int)(p)
}`
		// Sprint 374: a pointer round-tripped through reflect.Value.Pointer
		// keeps its origin, so this is the original storage, not a forged
		// address; the program runs as it does natively.
		_, stderr, err := runGoSource(t, "s270-g4-issue15329-raw", src)
		if err != nil {
			t.Fatalf("err=%v stderr=%q; want the reflected pointer to keep its origin", err, stderr)
		}
	})

	t.Run("slice3 raw slice header read remains layout reinterpretation", func(t *testing.T) {
		src := `package main
import "unsafe"
func main() {
	array := new([10]byte)
	x := array[1:3:4]
	raw := *(*[3]uintptr)(unsafe.Pointer(&x))
	_, _, _ = raw[0], raw[1], raw[2]
}`
		_, stderr, err := runGoSource(t, "s270-g4-slice3-raw", src)
		if err == nil || (!strings.Contains(err.Error()+stderr, "BASHPP-EUNSAFE-VIEW") && !strings.Contains(err.Error()+stderr, "BASHPP-EUNSAFE-LAYOUT")) {
			t.Fatalf("err=%v stderr=%q; want raw-layout view refusal", err, stderr)
		}
	})
}
