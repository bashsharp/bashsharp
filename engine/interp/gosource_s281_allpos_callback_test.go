//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"strings"
	"testing"
)

func TestS281AllPosSynchronousNativePositionVisitor(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"cmd/internal/obj"
	"cmd/internal/src"
)

func main() {
	var ctxt obj.Link
	outerBase := src.NewFileBase("outer.go", "/abs/outer.go")
	middleBase := src.NewFileBase("middle.go", "/abs/middle.go")
	innerBase := src.NewFileBase("inner.go", "/abs/inner.go")

	outer := ctxt.PosTable.XPos(src.MakePos(outerBase, 10, 2))
	middle := ctxt.PosTable.XPos(src.MakePos(middleBase, 20, 3))
	outerIndex := ctxt.InlTree.Add(-1, outer, nil, "outer")
	middleIndex := ctxt.InlTree.Add(outerIndex, middle, nil, "middle")

	inlinedBase := src.NewInliningBase(innerBase, middleIndex)
	innermost := ctxt.PosTable.XPos(src.MakePos(inlinedBase, 30, 4))

	sep := ""
	sum := uint(0)
	ctxt.AllPos(innermost, func(pos src.Pos) {
		fmt.Print(sep, pos.String())
		sep = "|"
		sum += pos.Line() + pos.Col()
	})
	fmt.Println()
	fmt.Println("sum", sum, "sep", sep)
}
`
	got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/allpos")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "outer.go:10:2|middle.go:20:3|inner.go:30:4\nsum 69 sep |\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

func TestS281AllPosUnknownCallbackConsumersStillRefused(t *testing.T) {
	for name, source := range map[string]string{
		"async": `package main
import ("fmt"; "time"; "cmd/internal/src")
func main() {
	time.AfterFunc(time.Millisecond, func() { fmt.Println(src.NoPos) })
	time.Sleep(10 * time.Millisecond)
}
`,
		"retained": `package main
import ("bufio"; "strings"; "cmd/internal/src")
func main() {
	s := bufio.NewScanner(strings.NewReader(""))
	s.Split(func([]byte, bool) (int, []byte, error) { println(src.NoPos); return 0, nil, nil })
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := runGoSourceIdentity(t, source, "cmd/compile/internal/allpos")
			if err == nil {
				t.Fatalf("unsupported callback consumer was admitted: %+v", got)
			}
			if !strings.Contains(err.Error(), "callback") {
				t.Fatalf("missing callback refusal: %v", err)
			}
			if strings.Contains(got.stdout+got.stderr, "<unknown line number>") {
				t.Fatalf("refused callback still ran: %+v", got)
			}
		})
	}
}
