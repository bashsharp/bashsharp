//go:build full

package interp_test

// Sprint: #319; Story: #1285; Story-ID: 64efad8be341

import "testing"

// TestS319UnsafePointerAddressAtNativeBoundary: an unsafe.Pointer conversion of
// an interpreter address crosses into a dependency's unsafe.Pointer parameter
// as the pointer it is. It used to reach the scalar evaluator and fail with
// BASHPP-EEXPR-FORM for *syntax.BashPPAddressExpr (SSA allocators.go:317).
func TestS319UnsafePointerAddressAtNativeBoundary(t *testing.T) {
	for name, body := range map[string]string{
		"index address":    `b := []int64{1, 2, 3}; v := reflect.NewAt(reflect.TypeOf(int64(0)), unsafe.Pointer(&b[1])).Elem(); fmt.Println(v.Int())`,
		"variable address": `x := int64(2); v := reflect.NewAt(reflect.TypeOf(int64(0)), unsafe.Pointer(&x)).Elem(); fmt.Println(v.Int())`,
		"pointer variable": `x := int64(2); p := &x; v := reflect.NewAt(reflect.TypeOf(int64(0)), unsafe.Pointer(p)).Elem(); fmt.Println(v.Int())`,
	} {
		t.Run(name, func(t *testing.T) {
			src := "package main\nimport (\"fmt\"; \"reflect\"; \"unsafe\")\nfunc main() { " + body + " }\n"
			out, stderr, err := runGoSource(t, "s319-unsafe-bridge", src)
			if err != nil || out != "2\n" || stderr != "" {
				t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
			}
		})
	}
}
