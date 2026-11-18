//go:build full

package interp_test

import "testing"

// Sprint: #249; Story: #716; Story-ID: 05dfa5ec639c
//
// The Go compiler corpus has rows that inspect gc's inliner, SSA, liveness,
// nil-check-elimination, and machine-code artifacts. Bash# deliberately has no
// gc optimizer or assembler to expose. These independent programs pin the
// source-level contracts that remain observable in both Bash# execution modes:
// the interpreter and the emitted Go artifact must agree with unchanged Go.
func TestS249CompilerArtifactReplacementConformance(t *testing.T) {
	for name, source := range map[string]string{
		"closure-capture-and-call": `package main
import "fmt"
func makeCounter(start int) func() int { n := start; return func() int { n++; return n } }
func main() { next := makeCounter(40); fmt.Println(next(), next()) }
`,
		"switch-selection-and-fallthrough": `package main
import "fmt"
func classify(v int) string {
	switch v {
	case 0:
		return "zero"
	case 1, 2:
		return "small"
	default:
		return "other"
	}
}
func main() { fmt.Println(classify(0), classify(2), classify(9)) }
`,
		"select-tuple-receive-dereference": `package main
import "fmt"
func main() {
	c := make(chan string, 1)
	c <- "received"
	var value string
	var open bool
	select {
	case *(&value), *(&open) = <-c:
		fmt.Println(value, open)
	default:
		panic("receive was not ready")
	}
}
`,
		"nil-pointer-panic-is-observable": `package main
import "fmt"
func panicsOnDeref() (panicked bool) {
	defer func() { panicked = recover() != nil }()
	var p *int
	_ = *p
	return false
}
func main() { fmt.Println(panicsOnDeref()) }
`,
		"bounds-guard-controls-indexing": `package main
import "fmt"
func at(values []int, index int) (value int, ok bool) {
	if index >= 0 && index < len(values) { return values[index], true }
	return 0, false
}
func main() { a, okA := at([]int{7, 9}, 1); b, okB := at([]int{7, 9}, 2); fmt.Println(a, okA, b, okB) }
`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
