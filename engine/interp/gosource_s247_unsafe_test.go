//go:build full

package interp_test

// Sprint: #247; Story: #672; Story-ID: fa5b3cf5a929

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestS247UnsafeRoot runs the upstream unsafebuiltins root unchanged.
func TestS247UnsafeRoot(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "unsafebuiltins.go"))
	if err != nil {
		t.Skip(err)
	}
	out, stderr, err := runGoSource(t, "unsafebuiltins", string(source))
	if err != nil || out != "" || stderr != "" {
		t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
	}
}

// TestS247UnsafeSpan covers the storage-span model: unsafe.Slice aliases the
// real backing both ways, unsafe.Add moves within the span, and StringData /
// SliceData round-trip through Slice and String.
func TestS247UnsafeSpan(t *testing.T) {
	src := `package main
import "unsafe"
func main() {
	var p [4]byte
	s := unsafe.Slice(&p[1], 3)
	s[0] = 7
	p[2] = 9
	println(p[1], s[1], len(s), cap(s), &s[2] == &p[3])
	q := (*byte)(unsafe.Add(unsafe.Pointer(&p[0]), 3))
	*q = 5
	println(p[3], s[2])
	b := unsafe.Add(unsafe.Pointer(&p[3]), -3)
	println(b == unsafe.Pointer(&p[0]), b == unsafe.Pointer(&p[1]))
	w := []int{10, 20, 30}
	println(*(*int)(unsafe.Add(unsafe.Pointer(&w[0]), 2*unsafe.Sizeof(w[0]))))
	d := []byte("abc")
	d2 := unsafe.Slice(unsafe.SliceData(d), len(d))
	d2[0] = 'x'
	println(string(d), unsafe.String(unsafe.SliceData(d), 2))
	println(string(unsafe.Slice(unsafe.StringData("hey"), 3)))
}`
	out, stderr, err := runGoSource(t, "s247-unsafe-span", src)
	if err != nil {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	want := "7 9 3 3 true\n5 5\ntrue false\n30\nxbc xb\nhey\n"
	if out+stderr != want {
		t.Fatalf("out=%q stderr=%q want %q", out, stderr, want)
	}
}

// TestS247UnsafeRefusals: a forged address and a pointer moved outside its
// span never read or write storage, and the refusal is not a recoverable
// Go panic.
func TestS247UnsafeRefusals(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"forged read", `q := (*byte)(unsafe.Pointer(^uintptr(0))); println(*q)`, "BASHPP-EUNSAFE-FORGED"},
		{"forged write", `q := (*byte)(unsafe.Pointer(^uintptr(0))); *q = 1`, "BASHPP-EUNSAFE-FORGED"},
		{"forged slice", `last := (*byte)(unsafe.Pointer(^uintptr(0))); s := unsafe.Slice(last, 1); println(len(s))`, "BASHPP-EUNSAFE-FORGED"},
		{"forged string", `last := (*byte)(unsafe.Pointer(^uintptr(0))); println(unsafe.String(last, 1))`, "BASHPP-EUNSAFE-FORGED"},
		{"add outside span", `defer func() { println("recovered", recover() != nil) }(); var p [4]byte; q := (*byte)(unsafe.Add(unsafe.Pointer(&p[0]), 9)); println(*q)`, "BASHPP-EUNSAFE-SPAN"},
		{"add off element grid", `var w [2]int; q := (*int)(unsafe.Add(unsafe.Pointer(&w[0]), 1)); println(*q)`, "BASHPP-EUNSAFE-SPAN"},
		{"slice past span", `var p [4]byte; _ = unsafe.Slice(&p[1], 4)`, "BASHPP-EUNSAFE-SPAN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package main\nimport \"unsafe\"\nfunc main() { " + tc.body + " }\n"
			out, stderr, err := runGoSource(t, "s247-unsafe-refusal", src)
			if err == nil || !strings.Contains(err.Error()+stderr, tc.want) || strings.Contains(out+stderr, "recovered") {
				t.Fatalf("err=%v out=%q stderr=%q want refusal %s", err, out, stderr, tc.want)
			}
		})
	}
}
