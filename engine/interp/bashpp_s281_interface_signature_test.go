//go:build full

package interp_test

import "testing"

func TestS281LinkedInterfaceGenericSignature(t *testing.T) {
	stdout, stderr, err := runGoSourcePackages(t, `package main
import "test/p"
func main() { p.Run() }
`, map[string]string{"a.go": `package p
import (
	"fmt"
	"hash/maphash"
)

type Type interface{ String() string }
type Basic struct{ name string }
func (b *Basic) String() string { return b.name }

type Hasher struct{}
func (Hasher) Hash(h *maphash.Hash, t Type) { h.WriteString(t.String()) }
func (Hasher) Equal(x, y Type) bool { return x == y }

func main() {
	var h maphash.Hasher[Type] = Hasher{}
	t := &Basic{name: "int"}
	fmt.Println(h.Equal(t, t))
}
func Run() { main() }
`})
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr)
	}
	if stdout != "true\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}
