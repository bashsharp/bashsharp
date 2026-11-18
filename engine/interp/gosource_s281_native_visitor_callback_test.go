//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// ir.DoChildren is an exact reviewed entry in the synchronous callback
// catalogue. Go 1.27.1's visit.go immediately calls n.doChildren(do), and the
// generated implementations only call do inline while walking child fields or
// slices, returning immediately when it reports true. They neither store the
// callback nor start a goroutine. Keep this admission scoped to that public API:
// other ir visitors and asynchronous or retained consumers remain refused.

import (
	"strings"
	"testing"
)

func TestS281DoChildrenSynchronousNativeVisitor(t *testing.T) {
	const source = `package main

import (
	"fmt"

	"cmd/compile/internal/ir"
	"cmd/internal/src"
)

func main() {
	root := ir.NewBinaryExpr(src.NoXPos, ir.OADD,
		ir.NewInt(src.NoXPos, 1), ir.NewInt(src.NoXPos, 2))

	seen := 0
	state := []int{}
	early := ir.DoChildren(root, func(ir.Node) bool {
		seen++
		state = append(state, seen)
		return true
	})
	fmt.Println("early", early, seen, state)

	seen = 0
	state = state[:0]
	all := ir.DoChildren(root, func(ir.Node) bool {
		seen++
		state = append(state, seen)
		return false
	})
	fmt.Println("all", all, seen, state)

	var nilNode ir.Node
	nilResult := ir.DoChildren(nilNode, func(ir.Node) bool {
		seen++
		state = append(state, seen)
		return true
	})
	fmt.Println("nil", nilResult, seen, state)
}
`
	got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/inline/inlheur")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "early true 1 [1]\nall false 2 [1 2]\nnil false 2 [1 2]\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want native stdout %q", got, want)
	}
}

func TestS281DoChildrenInterfaceAssertedPointerMapKey(t *testing.T) {
	const source = `package main

import (
	"fmt"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/internal/obj"
	"cmd/internal/src"
	"cmd/internal/sys"
)

func main() {
	types.PtrSize = 8
	types.RegSize = 8
	types.MaxWidth = 1 << 50
	base.Ctxt = &obj.Link{Arch: &obj.LinkArch{Arch: &sys.Arch{Alignment: 1, CanMergeLoads: true}}}
	typecheck.InitUniverse()

	pkg := types.NewPkg("", "")
	left := ir.NewNameAt(src.NoXPos, pkg.Lookup("left"), types.Types[types.TINT])
	right := ir.NewNameAt(src.NoXPos, pkg.Lookup("right"), types.Types[types.TINT])
	root := ir.NewBinaryExpr(src.NoXPos, ir.OADD, left, right)

	seen := map[*ir.Name]int{}
	order := []*ir.Name{}
	ir.DoChildren(root, func(n ir.Node) bool {
		name := n.(*ir.Name)
		seen[name]++
		order = append(order, name)
		name.SetUsed(true)
		return false
	})
	fmt.Println(len(seen), len(order), seen[left], seen[right], left.Used(), right.Used())

	ir.DoChildren(root, func(n ir.Node) bool {
		seen[n.(*ir.Name)]++
		return false
	})
	fmt.Println(len(seen), seen[left], seen[right], order[0] == left, order[1] == right)
}
`
	got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/inline/inlheur")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "2 2 1 1 true true\n2 2 2 true true\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want native stdout %q", got, want)
	}
}

func TestS281DoChildrenCallbackEligibilityStaysExact(t *testing.T) {
	for name, source := range map[string]string{
		"unknown ir visitor": `package main

import (
	"fmt"

	"cmd/compile/internal/ir"
	"cmd/internal/src"
)

func main() {
	root := ir.NewBinaryExpr(src.NoXPos, ir.OADD,
		ir.NewInt(src.NoXPos, 1), ir.NewInt(src.NoXPos, 2))
	ir.Visit(root, func(ir.Node) { fmt.Println("CALLBACK-RAN") })
}
`,
		"async": `package main

import (
	"fmt"
	"time"

	"cmd/compile/internal/ir"
)

var _ ir.Node

func main() {
	seen := 0
	time.AfterFunc(time.Millisecond, func() {
		seen++
		fmt.Println("CALLBACK-RAN", seen)
	})
	time.Sleep(10 * time.Millisecond)
	fmt.Println("done", seen)
}
`,
		"retained": `package main

import (
	"bufio"
	"fmt"
	"strings"

	"cmd/compile/internal/ir"
)

var _ ir.Node

func main() {
	seen := 0
	s := bufio.NewScanner(strings.NewReader("x"))
	s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		seen++
		fmt.Println("CALLBACK-RAN", seen)
		return 0, nil, nil
	})
	fmt.Println("done", seen)
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/inline/inlheur")
			if err == nil {
				t.Fatalf("unknown/async/retained callback consumer was admitted: %+v", got)
			}
			if !strings.Contains(err.Error(), "callback") {
				t.Fatalf("missing callback refusal: %v", err)
			}
			if strings.Contains(got.stdout+got.stderr, "CALLBACK-RAN") {
				t.Fatalf("refused callback still ran (not fail-closed): %+v", got)
			}
		})
	}
}
