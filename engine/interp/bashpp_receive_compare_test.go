// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestBashPPInlineReceiveCompareEvaluatesOnce guards the Sprint 270 / Story 761
// fix: an inline `<-ch` compared with a scalar variable must receive exactly
// once. Before the fix the scalar operand yielded the comparable path's
// scalar-only sentinel after the receive had already run, making the caller
// retry the whole comparison and receive from the channel a second time — the
// value under test was compared against the wrong element and a following
// receive read past the drained buffer. Each program's output is fixed by the
// language, so the assertions are self-contained and need no Go toolchain.
func TestBashPPInlineReceiveCompareEvaluatesOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			// The reported root: receive on the left, scalar on the right.
			// Only one value is consumed by the comparison, so the buffered 2
			// is still available for the second receive.
			name: "recv-left-scalar-right",
			src: `package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	v := 1
	fmt.Println(<-ch == v)
	fmt.Println(<-ch)
}

`,
			want: "true\n2\n",
		},
		{
			// Parenthesised receive must be recognised through the paren.
			name: "recv-left-paren-scalar-right",
			src: `package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 7
	ch <- 8
	v := 7
	fmt.Println((<-ch) == v)
	fmt.Println(<-ch)
}

`,
			want: "true\n8\n",
		},
		{
			// Scalar on the left already fell back before the receive fired,
			// so this direction was correct before the fix; keep it covered so
			// the reorder does not regress it.
			name: "scalar-left-recv-right",
			src: `package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	v := 1
	fmt.Println(v == <-ch)
	fmt.Println(<-ch)
}
`,
			want: "true\n2\n",
		},
		{
			// Two receives genuinely consume two values, in source order.
			name: "recv-both-sides",
			src: `package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 5
	ch <- 6
	fmt.Println(<-ch == <-ch)
}
`,
			want: "false\n",
		},
		{
			// A mismatching compare still leaves the sibling value intact.
			name: "recv-left-scalar-right-mismatch",
			src: `package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 3
	ch <- 4
	v := 9
	fmt.Println(<-ch == v)
	fmt.Println(<-ch)
}
`,
			want: "false\n4\n",
		},
		{
			// A literal sibling takes the scalar fallback only after both
			// operands were read, so the receive had already consumed its
			// value; the fallback must compare that value, not receive again.
			name: "recv-left-literal-right",
			src: `package main

import "fmt"

func main() {
	ch := make(chan int32, 2)
	ch <- 123
	ch <- 124
	if <-ch != 123 {
		panic("wrong value")
	}
	fmt.Println(len(ch), <-ch)
}
`,
			want: "1 124\n",
		},
		{
			name: "literal-left-recv-right",
			src: `package main

import "fmt"

func main() {
	ch := make(chan string, 2)
	ch <- "a"
	ch <- "b"
	fmt.Println("a" == <-ch)
	fmt.Println(len(ch), <-ch)
}
`,
			want: "true\n1 b\n",
		},
		{
			// On an unbuffered channel the second receive never returns: the
			// receiver must finish after one send.
			name: "recv-literal-unbuffered",
			src: `package main

import "fmt"

func main() {
	c := make(chan int64)
	done := make(chan bool)
	go func() {
		if <-c != 123456 {
			panic("wrong value")
		}
		done <- true
	}()
	c <- 123456
	fmt.Println(<-done)
}
`,
			want: "true\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			out, errOut, err := bashPPRunGoSource(t, dir, dir+"/original.go", tc.src)
			if err != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, out, errOut)
			}
			if out != tc.want {
				t.Fatalf("stdout=%q, want %q (stderr=%q)", out, tc.want, errOut)
			}
		})
	}
}

func TestBashPPInlineReceiveBinaryEvaluatesOnceAcrossOperators(t *testing.T) {
	operations := []struct {
		op        string
		leftWant  string
		rightWant string
	}{
		{"==", "false", "false"}, {"!=", "true", "true"},
		{"<", "false", "true"}, {"<=", "false", "true"},
		{">", "true", "false"}, {">=", "true", "false"},
		{"+", "10", "10"}, {"-", "6", "-6"}, {"*", "16", "16"},
		{"/", "4", "0"}, {"%", "0", "2"}, {"<<", "32", "512"},
		{">>", "2", "0"}, {"&", "0", "0"}, {"|", "10", "10"},
		{"^", "10", "10"}, {"&^", "8", "2"},
	}

	var src, want strings.Builder
	src.WriteString("package main\n\nimport \"fmt\"\n\nfunc main() {\n")
	for _, tc := range operations {
		fmt.Fprintf(&src, "{ ch := make(chan int, 1); ch <- 8; fmt.Println(<-ch %s 2) }\n", tc.op)
		fmt.Fprintf(&src, "{ ch := make(chan int, 1); ch <- 8; fmt.Println(2 %s <-ch) }\n", tc.op)
		fmt.Fprintf(&want, "%s\n%s\n", tc.leftWant, tc.rightWant)
	}
	src.WriteString("}\n")

	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/original.go", src.String())
	if err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, out, errOut)
	}
	if out != want.String() {
		t.Fatalf("stdout=%q, want %q (stderr=%q)", out, want.String(), errOut)
	}
}
