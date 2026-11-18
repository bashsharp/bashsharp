//go:build full

package interp

import (
	"strings"
	"testing"
)

func TestS319ManagerOpaqueGlobalAliasMutationRefuses(t *testing.T) {
	source := `package dep
import "example.com/foreign"
type Expr interface { Eval(func() bool) bool }
type Root struct { Next Expr }
func (r *Root) Eval(cb func() bool) bool {
	foreign.ReplaceGlobal()
	return r.Next.Eval(cb)
}
type Leaf struct{}
func (*Leaf) Eval(cb func() bool) bool { return cb() }
`
	// A caller may have passed &r.Next to foreign.Bind before Eval begins.
	ok, reason := dependencyCallbackProveMethodSource(t, source, "Root", "Eval", []string{"Root", "Leaf"}, []int{0})
	if ok || !strings.Contains(reason, "target is not source-visible") {
		t.Fatalf("opaque global alias mutation admitted; ok=%v reason=%q", ok, reason)
	}
}
