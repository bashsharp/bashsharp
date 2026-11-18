//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

// TestGoSourceNamedSlicePointerVariadicMethod pins the compiler Nodes shape:
// an original pointer-receiver method replaces a named slice header with the
// result of variadic append while an imported formatter synchronously drives
// the containing original String method. The interpreter must preserve the
// original header, reuse or replace its backing storage exactly as Go does,
// retain existing elements, and distinguish nil from non-nil empty slices
// when append receives no values.
func TestGoSourceNamedSlicePointerVariadicMethod(t *testing.T) {
	const source = `package main
import "fmt"

type Nodes []int

func (n *Nodes) Append(values ...int) {
	*n = append(*n, values...)
}

type Holder struct {
	Body Nodes
}

func (h *Holder) String() string {
	h.Body.Append(1, 2, 3)
	full := h.Body[:cap(h.Body)]
	h.Body.Append()
	return fmt.Sprint(h.Body, full)
}

func main() {
	h := &Holder{}
	fmt.Println(h)
	fmt.Println(h.Body, len(h.Body), cap(h.Body))
	full := h.Body[:cap(h.Body)]
	h.Body.Append(4)
	fmt.Println(h.Body, full)
	h.Body.Append(5, 6)
	fmt.Println(h.Body, len(h.Body), cap(h.Body))

	var nilNodes Nodes
	nilNodes.Append()
	emptyNodes := Nodes{}
	emptyNodes.Append()
	fmt.Println(nilNodes == nil, emptyNodes == nil)
}
`
	differGoSource(t, source, nil, "")
}

// TestGoSourceVariadicMethodCallback exercises the generated stub itself. The
// dependency supplies its variadic parameter as one []int callback argument;
// the interpreter must bind that value as a spread call, including the zero
// argument case, while the pointer receiver keeps original identity.
func TestGoSourceVariadicMethodCallback(t *testing.T) {
	const dependency = `package dep
type Adder interface { Add(...int) }
func Call(a Adder, values ...int) { a.Add(values...) }
`
	const source = `package main
import (
	"fmt"
	"example.com/variadicmethod/dep"
)
type Counter int
func (c *Counter) Add(values ...int) {
	for _, value := range values { *c += Counter(value) }
}
func main() {
	var counter Counter
	dep.Call(&counter, 2, 3)
	dep.Call(&counter)
	fmt.Println(counter)
}
`
	differGoSourceDependencyModule(t, "example.com/variadicmethod", dependency, source)
}
