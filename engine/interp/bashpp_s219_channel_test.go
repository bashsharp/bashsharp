//go:build full

package interp_test

import (
	"github.com/bashsharp/bashsharp/engine/gosource"
	"strings"
	"testing"
)

// TestS219ChannelIdentity keeps channel values on the typed value path across
// the argument, result, assignment, and nil-operation boundaries exercised by
// the Sprint 219 corpus roots. These are deliberately smaller programs than
// the corpus fixtures, but retain the exact failing expression shapes.
func TestS219ChannelIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{
			name: "nil_second_channel_argument",
			source: `package main
func send(a, b chan uint) { a <- 1; if b != nil { b <- 2 } }
func main() { a := make(chan uint, 1); send(a, nil); println(<-a) }
`,
			want: "1\n",
		},
		{
			name: "returned_channel",
			source: `package main
func odds() chan int { out := make(chan int); go func() { out <- 5 }(); return out }
func main() { a := odds(); println(<-a) }
`,
			want: "5\n",
		},
		{
			name: "package_defined_element_assignment",
			source: `package main
type block [7]byte
var c chan block
var capacity = 1
func main() { c = make(chan block, capacity); println(cap(c)) }
`,
			want: "1\n",
		},
		{
			name: "defined_large_element_failed_make",
			source: `package main
type block [1<<16-1]byte
var c chan block
var capacity = -1
func shouldPanic(f func()) { defer func() { if recover() == nil { panic("did not panic") } }(); f() }
func main() { shouldPanic(func() { c = make(chan block, capacity) }); println("ok") }
`,
			want: "ok\n",
		},
		{
			name: "defined_channel_conversion_in_func_literal",
			source: `package main
type XChan chan int
var makers = []func(chan int) XChan { func(c chan int) XChan { return XChan(c) } }
func main() { c := make(chan int, 1); c <- 9; println(<-makers[0](c)) }
`,
			want: "9\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, got, err := bashPPRunGoSource(t, t.TempDir(), "outside-corpus.go", tc.source)
			if err != nil || stdout != "" || got != tc.want {
				t.Fatalf("run=%v stdout=%q stderr=%q; want stderr=%q", err, stdout, got, tc.want)
			}
		})
	}
}

// TestS219ChannelRange guards the call boundary used by the Go corpus channel
// tests. A channel-producing call is evaluated once, and range binds each
// received element rather than a synthetic collection index.
func TestS219ChannelRange(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{
			name: "call_once_received_values",
			source: `package main
var calls int
func seq(lo, hi int) chan int { calls++; c := make(chan int, hi-lo+1); for i := lo; i <= hi; i++ { c <- i }; close(c); return c }
func main() { s := ""; for v := range seq('a', 'c') { s += string(v) }; println(s, calls) }
`,
			want: "abc 1\n",
		},
		{
			name: "interpreted_element_channel",
			source: `package main
type item struct { n int }
func seq() chan item { c := make(chan item, 2); c <- item{7}; c <- item{9}; close(c); return c }
func main() { total := 0; for v := range seq() { total += v.n }; println(total) }
`,
			want: "16\n",
		},
		{
			name: "closed_channel_zero_and_range_control",
			source: `package main
func closed() chan uint { c := make(chan uint); close(c); return c }
func main() { n := 0; for range closed() { n++ }; c := closed(); v, ok := <-c; println(n, v, ok) }
`,
			want: "0 0 false\n",
		},
		{
			name: "typed_second_send_parameter",
			source: `package main
func send(a, b chan uint) { a <- 1; if b != nil { b <- 2 } }
func main() { a := make(chan uint, 1); b := make(chan uint, 1); send(a, b); println(<-a, <-b) }
`,
			want: "1 2\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, got, err := bashPPRunGoSource(t, t.TempDir(), "outside-corpus.go", tc.source)
			if err != nil || stdout != "" || got != tc.want {
				t.Fatalf("run=%v stdout=%q stderr=%q; want stderr=%q", err, stdout, got, tc.want)
			}
		})
	}
}

func TestS219ChannelDirectionGuards(t *testing.T) {
	for _, tc := range []struct {
		name, source string
	}{
		{
			name: "argument_widening",
			source: `package main
func wide(chan int) {}
func pass(c <-chan int) { wide(c) }
func main() { pass(make(chan int)) }
`,
		},
		{
			name: "result_widening",
			source: `package main
func wide(c <-chan int) chan int { return c }
func main() { _ = wide(make(chan int)) }
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program, err := gosource.Parse(strings.NewReader(tc.source), "outside-corpus-negative.go", gosource.Options{RunMain: true})
			if err == nil || program != nil {
				t.Fatal("invalid directional channel conversion succeeded")
			}
		})
	}
}
