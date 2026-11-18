//go:build full

package interp

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// A callee that cannot reach the original callback can neither call it nor
// store it, so the proof summarizes it as effect-free without descending into
// its body. These tests fix both halves of that rule: the positive is a printer
// whose call chain is far deeper than the proof's depth bound and must still be
// admitted, and the negatives are the four routes by which a callee with clean
// direct arguments can still reach the callback, each of which must stay
// refused.

func dependencyCallbackProveSource(t *testing.T, source, name string, args []int) (bool, string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	ok := proof.prove(name, args)
	return ok, proof.reason
}

// dependencyCallbackMiniPrinterSource is a faithful reduction of the SDK node
// printer: a writer struct over a byte buffer, reached as a plain field of the
// callback-holding parser, driven by a printRawNode-like walk whose chain of
// distinct frames is levels deep. The printer holds neither the parser nor the
// error handler, so no part of it can reach the callback.
func dependencyCallbackMiniPrinterSource(levels int) string {
	var b strings.Builder
	b.WriteString(`package dep

type node struct {
	kind  string
	child *node
}

type writer struct {
	buf []byte
}

func (w *writer) writeBytes(data []byte) {
	w.buf = append(w.buf, data...)
}

func (w *writer) writeString(s string) {
	w.writeBytes([]byte(s))
}

`)
	for level := 0; level < levels; level++ {
		next := fmt.Sprintf("printRawNode%d", (level+1)%levels)
		fmt.Fprintf(&b, `func (w *writer) printRawNode%d(n *node) {
	if n == nil {
		return
	}
	w.writeString(n.kind)
	w.%s(n.child)
}

`, level, next)
	}
	b.WriteString(`type parser struct {
	errh func(error)
	out  writer
}

func (p *parser) describe(n *node) {
	p.out.printRawNode0(n)
	p.out.writeString("\n")
}

func Parse(cb func(error)) {
	p := &parser{errh: cb}
	p.describe(&node{kind: "root", child: &node{kind: "leaf"}})
}
`)
	return b.String()
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestS281CallbackProofUnreachableCalleeMiniPrinter(t *testing.T) {
	// Derived from the bound, not pinned: the point of the fixture is a chain
	// the walk could not possibly descend, so it has to outgrow the bound
	// whenever the bound moves.
	levels := dependencyCallbackProofDepthBound + 16
	if levels <= dependencyCallbackProofDepthBound {
		t.Fatalf("mini printer chain %d must exceed the depth bound %d", levels, dependencyCallbackProofDepthBound)
	}
	ok, reason := dependencyCallbackProveSource(t, dependencyCallbackMiniPrinterSource(levels), "Parse", []int{0})
	if !ok {
		t.Fatalf("callback-unreachable printer refused: %s", reason)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestS281CallbackProofUnreachableCalleeNegatives(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		reason string
	}{
		{
			// The callee's direct arguments are clean; it reaches the callback
			// through a package-level variable assigned earlier.
			name: "package_variable",
			source: `package dep
var g func()
var saved func()
func helper() { saved = g }
func Retain(cb func()) {
	g = cb
	helper()
}`,
			reason: "package global",
		},
		{
			// Same route, with the package-level store itself hidden behind a
			// helper so the refusal is only reachable by descending.
			name: "package_variable_via_helper",
			source: `package dep
var g func()
var saved func()
func stash(cb func()) { g = cb }
func helper() { saved = g }
func Retain(cb func()) {
	stash(cb)
	helper()
}`,
			reason: "package global",
		},
		{
			// The argument is a struct value whose nested pointer field reaches
			// the callback.
			name: "nested_pointer_field",
			source: `package dep
type inner struct{ f func() }
type outer struct{ p *inner }
var saved func()
func take(o outer) { saved = o.p.f }
func Retain(cb func()) {
	take(outer{p: &inner{f: cb}})
}`,
			reason: "package global",
		},
		{
			// The callee is a closure taking no arguments at all; it reaches the
			// callback through a captured variable.
			name: "closure_capture",
			source: `package dep
var saved func()
func Retain(cb func()) {
	f := cb
	run := func() { saved = f }
	run()
}`,
			reason: "package global",
		},
		{
			// The argument is statically an interface; its dynamic value holds
			// the callback behind a pointer.
			name: "interface_dynamic_value",
			source: `package dep
type holder interface{ run() }
type impl struct{ f func() }
func (i *impl) run() {}
var saved func()
func take(h holder) {
	if v, ok := h.(*impl); ok {
		saved = v.f
	}
}
func Retain(cb func()) {
	take(&impl{f: cb})
}`,
			reason: "package global",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ok, reason := dependencyCallbackProveSource(t, test.source, "Retain", []int{0})
			if ok {
				t.Fatal("callback-reachable callee incorrectly admitted")
			}
			if !strings.Contains(reason, test.reason) {
				t.Fatalf("refused for the wrong reason: got %q, want it to mention %q", reason, test.reason)
			}
		})
	}
}
