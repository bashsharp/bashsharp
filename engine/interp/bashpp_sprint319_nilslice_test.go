//go:build full

package interp_test

// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
//
// Typed nil assignment to a nilable slice the dependency returned. The
// testdir harness pattern `b, err := os.ReadFile(name); if err != nil { b =
// nil }` left b's cell as a native handle whose wire type is a slice
// spelling, which no nil-assignment candidate claimed; the bare `nil` ident
// then fell through to the scalar identifier path and was refused as
// BASHPP-EASSIGN-UNDECLARED. Every case runs the unchanged original source
// against a real Go build of the same file; no case asserts an
// interpreter-only expectation.

import "testing"

func TestGoSourceNativeSliceResultNilAssign(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		// The reduced checkExpectedOutput shape: a successful read hands b to
		// the dependency's slice handle, and the reassignment must produce the
		// nil []byte the compiled program has.
		"readfile_result_reassigned_nil": `package main

import (
	"fmt"
	"os"
)

func main() {
	if err := os.WriteFile("want.txt", []byte("expected output\n"), 0o600); err != nil {
		panic(err)
	}
	b, err := os.ReadFile("want.txt")
	if err != nil {
		panic(err)
	}
	fmt.Printf("%d %q\n", len(b), string(b))
	b = nil
	fmt.Println(b == nil, len(b))
	fmt.Printf("%q\n", string(b))
}
`,
		// The missing-file branch verbatim: os.ReadFile already returned a nil
		// slice alongside its error, and the explicit `b = nil` after the
		// os.IsNotExist check must still be a legal typed nil.
		"readfile_missing_reassigned_nil": `package main

import (
	"fmt"
	"os"
)

func check(name string) string {
	b, err := os.ReadFile(name)
	if err != nil && !os.IsNotExist(err) {
		panic(err)
	}
	if err != nil {
		b = nil
	}
	return string(b)
}

func main() {
	if err := os.WriteFile("want.txt", []byte("have\n"), 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("%q %q\n", check("want.txt"), check("missing.txt"))
}
`,
		// A nil slice result reassigned and then grown again: the typed nil
		// must remain a live []byte value for append and comparison, not a
		// one-way sink.
		"reassigned_nil_then_append": `package main

import (
	"fmt"
	"os"
)

func main() {
	if err := os.WriteFile("want.txt", []byte("abc"), 0o600); err != nil {
		panic(err)
	}
	b, err := os.ReadFile("want.txt")
	if err != nil {
		panic(err)
	}
	b = nil
	b = append(b, "xyz"...)
	fmt.Println(len(b), string(b), b == nil)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			differGoSource(t, source, nil, "")
		})
	}
}
