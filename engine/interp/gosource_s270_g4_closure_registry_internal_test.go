//go:build full

// Sprint: #270; Story: #759; Story-ID: 9fa63957d084

package interp

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A function literal that is the callee of its own call — deferred, invoked in
// place, or launched — never becomes a value, so no handle to it exists and it
// must not be retained in the closure registry. A literal that IS a value keeps
// its registry entry. Before the fix a loop calling a function with
// `defer func() {}()` retained every closure and its captured scope
// (issue19078: 4.6 GB peak over a million iterations).
func TestS270G4ImmediateClosuresAreNotRegistered(t *testing.T) {
	source := `package main

var sink interface{}

func deferred(x *int) *int {
	defer func() {}()
	sink = &x
	return x
}

func main() {
	total := 0
	for i := 0; i < 200; i++ {
		_ = deferred(nil)
		func() { total++ }()
		defer func(n int) { _ = n }(i)
	}
	f := func() int { return total }
	println(f())
}
`
	var stdout, stderr bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), p.File); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	if got := stdout.String() + stderr.String(); got != "200\n" {
		t.Fatalf("output %q, want %q", got, "200\n")
	}
	if n := len(r.bashPPClosures); n > 10 {
		t.Fatalf("closure registry retained %d entries for immediately invoked literals", n)
	}
	if n := len(r.bashPPClosures); n < 1 {
		t.Fatalf("the function value f was not registered")
	}
}
