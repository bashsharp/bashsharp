package interp_test

// Sprint: #281; Story: #808; Story-ID: a5674a7da9a6

import "testing"

// TestGoSourceS281MinMaxScalarOperand pins the general fix for min/max used
// as an operand inside a larger scalar expression, not as the sole
// right-hand side of `:=`. bashPPEvalScalarExpr's call switch recognized
// only the bare `len`/`cap`/`copy` spellings as builtins with a scalar
// result; every other predeclared name, including `min`/`max`, fell through
// to the ordinary function lookup and failed with "BASHPP-EEXPR-UNDEFINED:
// undefined callable max". cmd/compile/internal/abt hits exactly this shape
// computing `t.height_ = 1 + max(l.height(), r.height())`.
func TestGoSourceS281MinMaxScalarOperand(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s281-min-max-scalar", `package main
import "fmt"
type node struct {
	left, right *node
	height      int8
}
func (n *node) h() int8 {
	if n == nil {
		return 0
	}
	return n.height
}
func rebalance(l, r *node) int8 {
	return 1 + max(l.h(), r.h())
}
func main() {
	left := &node{height: 2}
	right := &node{height: 5}
	n := &node{left: left, right: right}
	n.height = rebalance(n.left, n.right)
	shallow := min(n.left.h(), n.right.h()) - 1
	fmt.Println(n.height, shallow)
}`)
	if err != nil || out != "6 1\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// TestGoSourceS281DereferencedStructValueCopy pins the value/reference split
// used by applicative data structures. Copying *tree must detach the tree's
// scalar and pointer fields as one struct value, while the node reached through
// the copied pointer remains shared. cmd/compile/internal/abt relies on this in
// T.Copy: historical tree headers are independent, but their immutable nodes
// are shared until an updated path is installed in the current tree.
func TestGoSourceS281DereferencedStructValueCopy(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s281-deref-struct-copy", `package main
import "fmt"

type node struct {
	key  int
	next *node
}

type tree struct {
	root *node
	size int
}

func (t *tree) Copy() *tree {
	u := *t
	return &u
}

func main() {
	shared := &node{key: 7}
	current := &tree{root: shared, size: 1}
	history := current.Copy()

	current.size = 2
	current.root = &node{key: 9, next: shared}
	history.root.key = 8

	fmt.Println(history.size, history.root.key)
	fmt.Println(current.size, current.root.key, current.root.next.key)
}`)
	if err != nil || out != "1 8\n2 9 8\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
