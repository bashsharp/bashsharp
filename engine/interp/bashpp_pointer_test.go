// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestBashPPPointersAddressDereferenceAndNew(t *testing.T) {
	src := `type S struct { N int }
func main() {
 x := 1
 p := &x
 before := *p
 *p = 3
 s := S{N: 4}
 fp := &s.N
 *fp = 5
 a := [1]int{6}
 ap := &a[0]
 *ap = 7
 sl := []int{8}
 sp := &sl[0]
 *sp = 9
 np := new(int)
 *np = 10
 nv := *np
 ns := new(S)
 sv := *ns
 na := new([1]int)
 av := *na
 nsl := new([]int)
 slv := *nsl
 nm := new(map[string]int)
 mv := *nm
 printf '%s:%s:%s:%s:%s:%s:%s:%s:%s:%s\n' "$before" "$x" s.N a[0] sl[0] "$nv" sv.N av[0] "$slv" "$mv"
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1:3:5:7:9:10:0:0:null:null\n"))
}

func TestBashPPPointerDiagnostics(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"nil", "var p *int\n x := *p", "BASHPP-ENIL-DEREF:"},
		{"nonaddressable", "p := &1", "BASHPP-ENONADDRESSABLE:"},
		{"map element", "m := map[string]int{\"x\": 1}\n p := &m[\"x\"]", "BASHPP-ENONADDRESSABLE:"},
		{"target", "x := 1\n y := *x", "BASHPP-EPOINTER-TARGET:"},
		{"assignment", "x := 1\n p := &x\n *p = \"bad\"", "BASHPP-EASSIGN-MISMATCH:"},
		{"stale storage shape", "a := [1]int{1}\n p := &a[0]\n a=oops\n x := *p", "BASHPP-EPOINTER-TARGET:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := "func main() {\n" + test.body + "\n}\nmain()\n"
			_, stderr, err := runBashSharpCall(t, src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			if !strings.HasPrefix(stderr, test.want) {
				t.Fatalf("stderr %q does not start with %q", stderr, test.want)
			}
		})
	}
}

func TestBashPPPointerSubshellSnapshot(t *testing.T) {
	src := `func main() {
 x := 1
 p := &x
 ( *p = 2; printf 'sub:%s\n' "$x" )
	 printf 'parent:%s\n' "$x"
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "sub:2\nparent:1\n"))
}

func TestBashPPPointerTaskSnapshotAndIdentity(t *testing.T) {
	src := `func main() {
 x := 1
 p := &x
 alias := p
 *alias = 2
 finished := make(chan int)
 go func() {
  *p = 3
  printf 'task:%s\n' "$x"
  finished <- 1
 }()
 # File completion cancels unfinished tasks; wait for the snapshot assertion.
 completed := <-finished
 printf 'parent:%s\n' "$x"
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	for _, line := range []string{"task:3\n", "parent:2\n"} {
		qt.Assert(t, qt.StringContains(out, line))
	}
}

func TestBashPPPointerRetainsDeepReadonlyIdentity(t *testing.T) {
	src := `type S struct { N int }
s := S{N: 1}
func main() {
 p := &s.N
 readonly s
 *p = 2
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.Equals(stderr, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"s\" through pointer\n"))
}

func TestBashPPPointerCannotBypassScalarReadonly(t *testing.T) {
	src := `func main() {
 x := 1
 p := &x
 readonly x
 *p = 2
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.Equals(stderr, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer\n"))
}
