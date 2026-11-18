//go:build full

package interp_test

// Sprint: #374; Story-ID: e80318ce1ce9

import (
	"strings"
	"testing"
)

const unsafeSliceViewPrelude = `package main
import ("fmt"; "unsafe")
type H struct { Data unsafe.Pointer; Len, Cap int }
type limit struct { min, max int64; umin, umax uint64 }
type ID int32
var _ = fmt.Sprint
`

// TestGoSourceUnsafeSliceViewAllocator is the shape of the generated
// cmd/compile/internal/ssa allocators: a pooled []limit is handed out as a
// narrower []ID through a slice header, written through the view, handed back
// through the reverse header, cleared and reused as both types.
func TestGoSourceUnsafeSliceViewAllocator(t *testing.T) {
	src := unsafeSliceViewPrelude + `
var pool [][]limit
func allocLimitSlice(n int) []limit {
	if len(pool) > 0 {
		s := pool[len(pool)-1]
		pool = pool[:len(pool)-1]
		return s[:n]
	}
	return make([]limit, 8)[:n]
}
func freeLimitSlice(s []limit) { clear(s); pool = append(pool, s) }
func allocIDSlice(n int) []ID {
	var base limit
	var derived ID
	if unsafe.Sizeof(base)%unsafe.Sizeof(derived) != 0 {
		panic("bad")
	}
	scale := unsafe.Sizeof(base) / unsafe.Sizeof(derived)
	b := allocLimitSlice(int((uintptr(n) + scale - 1) / scale))
	s := H{
		Data: unsafe.Pointer(&b[0]),
		Len:  n,
		Cap:  cap(b) * int(scale),
	}
	return *(*[]ID)(unsafe.Pointer(&s))
}
func freeIDSlice(s []ID) {
	var base limit
	var derived ID
	scale := unsafe.Sizeof(base) / unsafe.Sizeof(derived)
	b := H{
		Data: unsafe.Pointer(&s[0]),
		Len:  int((uintptr(len(s)) + scale - 1) / scale),
		Cap:  int((uintptr(cap(s)) + scale - 1) / scale),
	}
	freeLimitSlice(*(*[]limit)(unsafe.Pointer(&b)))
}
func main() {
	ids := allocIDSlice(10)
	fmt.Println(len(ids), cap(ids))
	for i := range ids {
		if ids[i] != 0 {
			panic("not zero")
		}
		ids[i] = ID(i + 1)
	}
	ids = append(ids, 99)
	sum := 0
	for _, v := range ids {
		sum += int(v)
	}
	fmt.Println(len(ids), cap(ids), sum)
	freeIDSlice(ids)
	l := allocLimitSlice(3)
	fmt.Println(len(l), cap(l), l[0].min, l[1].umax, l[2].max)
	l[0].min = 5
	freeLimitSlice(l)
	ids = allocIDSlice(3)
	fmt.Println(len(ids), cap(ids), ids[0], ids[1], ids[2])
	freeIDSlice(ids)
}`
	out, stderr, err := runGoSource(t, "unsafe-slice-view-allocator", src)
	want := "10 64\n11 64 154\n3 8 0 0 0\n3 64 0 0 0\n"
	if err != nil || out != want || stderr != "" {
		t.Fatalf("err=%v out=%q stderr=%q want %q", err, out, stderr, want)
	}
}

// TestGoSourceUnsafeSliceViewBytes checks the byte-offset semantics of a
// cross-width view against the output of the Go toolchain: little-endian
// element bytes carried across narrowing, widening, an offset start, a repeated
// read of one header, and pointer words read as another pointer type.
func TestGoSourceUnsafeSliceViewBytes(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"round trip", `b := make([]limit, 2)
b[0].min = -2; b[0].max = 0x0102030405060708; b[1].umax = 1<<63 + 5
h := H{unsafe.Pointer(&b[0]), 16, 16}
s := *(*[]ID)(unsafe.Pointer(&h))
fmt.Println(s)
s[0] = 7; s[15] = -1
s2 := *(*[]ID)(unsafe.Pointer(&h))
fmt.Println(&s2[0] == &s[0], s2[0])
h2 := H{unsafe.Pointer(&s[0]), 2, 2}
l := *(*[]limit)(unsafe.Pointer(&h2))
fmt.Println(l, l[1].umax)
h3 := H{unsafe.Pointer(&l[0]), 64, 64}
u := *(*[]uint8)(unsafe.Pointer(&h3))
fmt.Println(u[:12], u[60:])
h4 := H{unsafe.Pointer(&u[8]), 3, 3}
q := *(*[]uint64)(unsafe.Pointer(&h4))
fmt.Printf("%x\n", q)`,
			"[-2 -1 84281096 16909060 0 0 0 0 0 0 0 0 0 0 5 -2147483648]\ntrue 7\n" +
				"[{-4294967289 72623859790382856 0 0} {0 0 0 18446744069414584325}] 18446744069414584325\n" +
				"[7 0 0 0 255 255 255 255 8 7 6 5] [255 255 255 255]\n[102030405060708 0 0]\n"},
		{"pointer words", `type V struct{ a int }; type B struct{ a int }
v := &V{5}
b := make([]*V, 4); b[1] = v
h := H{unsafe.Pointer(&b[0]), 4, 4}
s := *(*[]*B)(unsafe.Pointer(&h))
fmt.Println(s[0] == nil, s[1] != nil, len(s))
x := &B{9}; s[2] = x
h2 := H{unsafe.Pointer(&s[0]), 4, 4}
vv := *(*[]*V)(unsafe.Pointer(&h2))
fmt.Println(vv[1] == v, vv[1].a, vv[2] != nil, vv[0] == nil)
clear(vv)
fmt.Println(vv[1] == nil)`, "true true 4\ntrue 5 true true\ntrue\n"},
		{"same element type aliases", `type E int32
b := []E{7, 11, 13}
h := H{unsafe.Pointer(&b[0]), 2, 3}
s := *(*[]E)(unsafe.Pointer(&h))
s[0] = 17
fmt.Println(len(s), cap(s), s[1], b[0])`, "2 3 11 17\n"},
		{"nil header", `h := H{nil, 0, 0}
s := *(*[]ID)(unsafe.Pointer(&h))
fmt.Println(s == nil, len(s), cap(s))`, "true 0 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "unsafe-slice-view-bytes", unsafeSliceViewPrelude+"func main() {\n"+tc.body+"\n}\n")
			if err != nil || out != tc.want || stderr != "" {
				t.Fatalf("err=%v out=%q stderr=%q want %q", err, out, stderr, tc.want)
			}
		})
	}
}

// TestGoSourceUnsafeSliceViewRefusals: a view moves the bytes into the view's
// backing, so anything the move cannot represent — an element type with bytes
// the interpreter does not hold, a partial element, or the moved-from storage
// used beside the view — fails by name and never yields stale or invented
// bytes.
func TestGoSourceUnsafeSliceViewRefusals(t *testing.T) {
	const view = "b := make([]limit, 2)\nh := H{unsafe.Pointer(&b[0]), 16, 16}\ns := *(*[]ID)(unsafe.Pointer(&h))\ns[0] = 7\n"
	for _, tc := range []struct{ name, body, want string }{
		{"string elements", `b := make([]string, 2); h := H{unsafe.Pointer(&b[0]), 4, 4}; fmt.Println(*(*[]int64)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-VIEW: type string"},
		{"interface target", `b := make([]limit, 2); h := H{unsafe.Pointer(&b[0]), 4, 4}; fmt.Println(*(*[]any)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-VIEW: interface type"},
		{"padded struct", `type P struct{ a int8; b int64 }; b := make([]P, 2); h := H{unsafe.Pointer(&b[0]), 4, 4}; fmt.Println(*(*[]int64)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-VIEW: padded"},
		{"past the span", `b := make([]limit, 2); h := H{unsafe.Pointer(&b[0]), 24, 24}; fmt.Println(*(*[]ID)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-SPAN"},
		{"partial element", `b := make([]limit, 2); h := H{unsafe.Pointer(&b[0]), 3, 3}; fmt.Println(*(*[]ID)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-SPAN"},
		{"invalid header", `b := make([]limit, 2); h := H{unsafe.Pointer(&b[0]), 9, 8}; fmt.Println(*(*[]ID)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-VIEW: slice header has invalid len"},
		{"pointer bytes as integers", `x := 1; b := []*int{&x}; h := H{unsafe.Pointer(&b[0]), 1, 1}; fmt.Println(*(*[]uint64)(unsafe.Pointer(&h)))`, "BASHPP-EUNSAFE-VIEW: the bytes of a live pointer"},
		{"write through header", `b := make([]limit, 2); h := H{unsafe.Pointer(&b[0]), 16, 16}; p := (*[]ID)(unsafe.Pointer(&h)); *p = nil`, "BASHPP-EUNSAFE-WRITE"},
		{"moved source read", view + `fmt.Println(b[0].min)`, "b[0] has no fields"},
		{"moved source written", view + `b[0].min = 3; fmt.Println(s[0])`, "not struct storage"},
		{"moved source printed", view + `fmt.Println(b)`, "goSourceUnsafeMoved"},
		{"moved source viewed again", view + `h2 := H{unsafe.Pointer(&b[0]), 64, 64}; fmt.Println(*(*[]uint8)(unsafe.Pointer(&h2)))`, "BASHPP-EUNSAFE-VIEW: storage was moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "unsafe-slice-view-refusal", unsafeSliceViewPrelude+"func main() {\n"+tc.body+"\n}\n")
			if err == nil || out != "" || !strings.Contains(err.Error()+stderr, tc.want) {
				t.Fatalf("err=%v out=%q stderr=%q want refusal %q", err, out, stderr, tc.want)
			}
		})
	}
}
