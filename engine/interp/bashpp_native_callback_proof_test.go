package interp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyFunctionCallbackLifetimeProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{
			name: "parser stack local callback",
			source: `package dep
type ErrorHandler func(error)
type source struct { errh ErrorHandler }
func (s *source) init(errh ErrorHandler) { s.errh = errh }
func (s *source) fail(err error) { if s.errh != nil { s.errh(err) } }
type scanner struct { source }
func (s *scanner) init(errh ErrorHandler) { s.source.init(errh) }
func (s *scanner) next() { s.fail(nil) }
type parser struct { scanner; errh ErrorHandler }
func (p *parser) init(errh ErrorHandler) {
	p.errh = errh
	p.scanner.init(func(err error) { p.errorAt(err) })
}
func (p *parser) errorAt(err error) { if p.errh != nil { p.errh(err) } }
func Parse(errh ErrorHandler) { var p parser; p.init(errh); p.next() }
`,
			want: true,
		},
		{
			name: "global retention",
			source: `package dep
type ErrorHandler func(error)
var retained ErrorHandler
func Parse(errh ErrorHandler) { retained = errh }
`,
		},
		{
			name: "goroutine",
			source: `package dep
type ErrorHandler func(error)
func Parse(errh ErrorHandler) { go errh(nil) }
`,
		},
		{
			name: "local container escaped before storage",
			source: `package dep
type ErrorHandler func(error)
type holder struct { callback ErrorHandler }
var saved *holder
func keep(h *holder) { saved = h }
func Parse(errh ErrorHandler) { var h holder; keep(&h); h.callback = errh }
`,
		},
		{
			name: "deferred callback runs before return",
			source: `package dep
type ErrorHandler func(error)
func Parse(errh ErrorHandler) { defer errh(nil) }
`,
			want: true,
		},
		{
			name: "unknown callee",
			source: `package dep
type ErrorHandler func(error)
func Parse(errh ErrorHandler) { external(errh) }
`,
		},
		{
			name: "conditional retention",
			source: `package dep
var saved func()
func Parse(cb func(), choose bool) { var f func(); if choose { f = cb }; saved = f }
`,
		},
		{
			name: "loop retention",
			source: `package dep
var saved func()
func Parse(cb func(), choose bool) { var f func(); for choose { f = cb; break }; saved = f }
`,
		},
		{
			name: "block retention",
			source: `package dep
var saved func()
func Parse(cb func()) { var f func(); { f = cb }; saved = f }
`,
		},
		{
			name: "switch retention",
			source: `package dep
var saved func()
func Parse(cb func(), choose bool) { var f func(); switch { case choose: f = cb }; saved = f }
`,
		},
		{
			name: "pointer alias branch clearing cannot sanitize",
			source: `package dep
type holder struct { f func() }
var saved *holder
func Parse(cb func(), choose bool) { h := &holder{}; alias := h; h.f = cb; if choose { alias.f = nil }; saved = h }
`,
		},
		{
			name: "named callback result naked return",
			source: `package dep
func Parse(cb func()) (out func()) { out = cb; return }
`,
		},
		{
			name: "conditional returned closure",
			source: `package dep
var saved func()
func wrap(cb func(), choose bool) func() { if choose { return func() { cb() } }; return nil }
func Parse(cb func(), choose bool) { saved = wrap(cb, choose) }
`,
		},
		{
			name: "async method on tainted receiver",
			source: `package dep
type holder struct { f func() }
func (h *holder) run() { h.f() }
func Parse(cb func()) { h := &holder{f: cb}; go h.run() }
`,
		},
		{
			name: "nested deferred local factory receiver",
			source: `package dep
type holder struct { f func() }
func factory(h *holder) func() { return h.f }
func Parse(cb func()) { h := &holder{f: cb}; defer factory(h)() }
`,
			want: true,
		},
		{
			name: "deferred closure actual retention",
			source: `package dep
var saved func()
func Parse(cb func()) { defer func() { saved = cb }() }
`,
		},
		{
			name: "deferred closure parameter actual retention",
			source: `package dep
var saved func()
func Parse(cb func()) { defer func(f func()) { saved = f }(cb) }
`,
		},
		{
			name: "deferred closure parameter synchronous call",
			source: `package dep
func Parse(cb func()) { defer func(f func()) { f() }(cb) }
`,
			want: true,
		},
		{
			name: "deferred unknown callee",
			source: `package dep
func Parse(cb func()) { defer external(cb) }
`,
		},
		{
			name: "deferred factory actual retention",
			source: `package dep
var saved func()
func factory(cb func()) func() { saved = cb; return func() {} }
func Parse(cb func()) { defer factory(cb)() }
`,
		},
		{
			name: "deferred late variable retention",
			source: `package dep
var saved func()
func Parse(cb func()) { var f func(); defer func() { saved = f }(); f = cb }
`,
		},
		{
			name: "deferred late field retention",
			source: `package dep
var saved func()
func Parse(cb func()) { h := &struct{ f func() }{}; defer func() { saved = h.f }(); h.f = cb }
`,
		},
		{
			name: "deferred lifo mutation retention",
			source: `package dep
var saved func()
func Parse(cb func()) { var f func(); defer func() { saved = f }(); defer func() { f = cb }() }
`,
		},
		{
			name: "variadic callback retention",
			source: `package dep
var saved func()
func keep(prefix string, callbacks ...func()) { saved = callbacks[0] }
func Parse(cb func()) { keep("callback", cb) }
`,
		},
		{
			name: "variadic synchronous callback",
			source: `package dep
func invoke(prefix string, callbacks ...func()) { callbacks[0]() }
func Parse(cb func()) { invoke("callback", cb) }
`,
			want: true,
		},
		{
			name: "unnamed callback parameter",
			source: `package dep
func discard(func()) {}
func Parse(cb func()) { discard(cb) }
`,
			want: true,
		},
		{
			name: "grouped callback parameters",
			source: `package dep
func invoke(first, second func()) { first(); second() }
func Parse(cb func()) { invoke(cb, func() {}) }
`,
			want: true,
		},
		{
			name: "grouped callback retention",
			source: `package dep
var saved func()
func keep(first, second func()) { saved = second }
func Parse(cb func()) { keep(func() {}, cb) }
`,
		},
		{
			name: "syntax parser trace-shaped defer",
			source: `package dep
const trace = false
type source struct { errh func(error) }
func (s *source) init(errh func(error)) { s.errh = errh }
type scanner struct { source }
type parser struct { scanner }
func (p *parser) init(cb func(error)) { p.scanner.source.init(cb) }
func (p *parser) trace(string) func() { _ = p.scanner.source.errh; return func() { _ = p } }
func (p *parser) parse() { if trace { defer p.trace("file")() } }
func Parse(cb func(error)) { var p parser; p.init(cb); p.parse() }
`,
			want: true,
		},
		{
			name: "clean function field on callback-bearing receiver",
			source: `package dep
type Handler func(int)
type source struct { callback func() }
func (s *source) pos() int { return 1 }
type parser struct { source; handler Handler }
func (p *parser) clear() { p.handler(p.pos()) }
func Parse(cb func(), handler Handler) { var p parser; p.callback = cb; p.handler = handler; p.clear() }
`,
			want: true,
		},
		{
			name: "recursive synthetic basic scalar leaf",
			source: `package dep
type parser struct { bad bool; callback func() }
func (p *parser) walk(n int) { _ = p.bad; if n > 0 { p.walk(n-1) }; p.callback() }
func Parse(cb func()) { var p parser; p.callback = cb; p.walk(1) }
`,
			want: true,
		},
		{
			name: "recursive synthetic named scalar leaf",
			source: `package dep
type LitKind uint8
type parser struct { kind LitKind; callback func() }
func (p *parser) walk(n int) { _ = p.kind; if n > 0 { p.walk(n-1) }; p.callback() }
func Parse(cb func()) { var p parser; p.callback = cb; p.walk(1) }
`,
			want: true,
		},
		{
			name: "recursive synthetic alias scalar leaf",
			source: `package dep
type LitKind uint8
type Alias = LitKind
type parser struct { kind Alias; callback func() }
func (p *parser) walk(n int) { _ = p.kind; if n > 0 { p.walk(n-1) }; p.callback() }
func Parse(cb func()) { var p parser; p.callback = cb; p.walk(1) }
`,
			want: true,
		},
		{
			name: "function field cannot receive callback",
			source: `package dep
type Handler func(func())
type parser struct { callback func(); handler Handler }
func (p *parser) pass() { p.handler(p.callback) }
func Parse(cb func(), handler Handler) { var p parser; p.callback = cb; p.handler = handler; p.pass() }
`,
		},
		{
			name: "unknown field on callback-bearing receiver",
			source: `package dep
type parser struct { callback func() }
func (p *parser) pos() int { return 1 }
func (p *parser) call() { p.unknown(p.pos()) }
func Parse(cb func()) { var p parser; p.callback = cb; p.call() }
`,
		},
		{
			name: "recursive callback substitution is unproved",
			source: `package dep
func walk(cb func()) { next := func() { cb() }; walk(next) }
func Parse(cb func()) { walk(cb) }
`,
		},
		{
			name: "recursive receiver field mutation is unproved",
			source: `package dep
var saved func()
type holder struct{ f func() }
func (h *holder) run(cb func(), choose bool) {
	if h.f != nil {
		saved = h.f
		return
	}
	h.f = cb
	if choose {
		h.run(cb, false)
	}
}
func Parse(cb func(), choose bool) {
	h := &holder{}
	h.run(cb, choose)
}
`,
		},
		{
			name: "recursive actual callback field mutation is unproved",
			source: `package dep
var saved func()
type holder struct{ f func() }
func mutate(h *holder, cb func(), choose bool) {
	h.f = cb
	if choose {
		mutate(h, cb, false)
	}
}
func inspect(h *holder) {
	if h.f != nil {
		saved = h.f
	}
}
func Parse(cb func(), choose bool) {
	h := &holder{}
	mutate(h, cb, choose)
	inspect(h)
}
`,
		},
		{
			name: "recursive captured environment mutation is unproved",
			source: `package dep
var saved func()
type holder struct{ f func() }
func run(invoke func(), h *holder, cb func(), choose bool) {
	h.f = cb
	if choose {
		run(invoke, h, cb, false)
		return
	}
	invoke()
}
func Parse(cb func(), choose bool) {
	h := &holder{}
	invoke := func() {
		if h.f != nil {
			saved = h.f
		}
	}
	run(invoke, h, cb, choose)
}
`,
		},
		{
			name: "recursive closure capture is unproved",
			source: `package dep
var saved func()
func run(invoke func(), cb func(), choose bool) {
	next := func() { invoke(); cb() }
	if choose {
		run(next, cb, false)
		return
	}
	next()
}
func Parse(cb func(), choose bool) {
	run(func(){}, cb, choose)
}
`,
		},
		{
			name: "alias topology can introduce callback reachability",
			source: `package dep
var saved func()
type H struct{ dst *H; f func() }
func Retain(cb func()) {
	h := &H{}
	other := &H{}
	h.dst = other
	other.f = cb
	saved = h.dst.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "whole pointer overwrite preserves aliases",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	alias := h
	*alias = holder{f: cb}
	saved = h.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "slice element alias later taint",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	xs := []*holder{h}
	h.f = cb
	saved = xs[0].f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "resliced element alias later taint",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	xs := []*holder{h}
	ys := xs[:]
	h.f = cb
	saved = ys[0].f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "map element alias later taint",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	xs := map[int]*holder{0: h}
	h.f = cb
	saved = xs[0].f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "type assertion preserves source object alias",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{f: cb}
	var v any = h
	p := v.(*holder)
	saved = p.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "two-result type assertion preserves source object alias",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{f: cb}
	var v any = h
	p, _ := v.(*holder)
	saved = p.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "escaped aggregate preserves reachable element",
			source: `package dep
var saved []*holder
type holder struct{ f func() }
func keep(xs []*holder) { saved = xs }
func Retain(cb func()) {
	h := &holder{}
	xs := []*holder{h}
	keep(xs)
	h.f = cb
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "cyclic joined elements retain callback fields",
			source: `package dep
var saved func()
type holder struct{ next *holder; f func() }
func Retain(cb func()) {
	a := &holder{}
	b := &holder{}
	a.next = a
	b.next = b
	a.f = cb
	b.f = cb
	xs := []*holder{a, b}
	saved = xs[0].f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "joined distinct elements retain callback fields",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h1 := &holder{f: cb}
	h2 := &holder{f: cb}
	xs := []*holder{h1, h2}
	saved = xs[0].f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "positional struct literal retains callback field",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := holder{cb}
	saved = h.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "map identifier key preserves element alias",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	k := 0
	xs := map[int]*holder{k: h}
	h.f = cb
	saved = xs[k].f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "retained clean closure observes later object callback",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	invoke := func() { h.f() }
	saved = invoke
	h.f = cb
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "retained clean closure observes later lexical callback",
			source: `package dep
var saved func()
func Retain(cb func()) {
	var f func()
	invoke := func() { f() }
	saved = invoke
	f = cb
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "clean closure observes later object callback",
			source: `package dep
var saved func()
type holder struct{ f func() }
func Retain(cb func()) {
	h := &holder{}
	invoke := func() { saved = h.f }
	h.f = cb
	invoke()
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "clean closure observes later lexical callback",
			source: `package dep
var saved func()
func Retain(cb func()) {
	var f func()
	invoke := func() { saved = f }
	f = cb
	invoke()
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "recursive generalized child helper store is unproved",
			source: `package dep
type C struct{ f func() }
type H struct{ child C }
var saved func()
func store(c *C, cb func()) { c.f = cb }
func walk(a, b *H, cb func(), n int) {
	if n > 0 {
		walk(a, a, cb, n-1)
	}
	store(&a.child, cb)
	saved = b.child.f
}
func Retain(cb func()) { walk(&H{}, &H{}, cb, 1) }
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "recursive current aliases expose lazy descendant store",
			source: `package dep
type C struct{ f func() }
type H struct{ child C }
var saved func()
func walk(a, b *H, cb func(), n int) {
	if n > 0 {
		walk(a, a, cb, n-1)
	}
	a.child.f = cb
	saved = b.child.f
}
func Parse(cb func()) { walk(&H{}, &H{}, cb, 1) }
`,
		},
		{
			name: "recursive current aliases expose rhs call store",
			source: `package dep
type C struct{ f func() }
type H struct{ child C }
var saved func()
func store(c *C, cb func()) int { c.f = cb; return 0 }
func walk(a, b *H, cb func(), n int) {
	if n > 0 {
		walk(a, a, cb, n-1)
	}
	x := store(&a.child, cb)
	_ = x
	saved = b.child.f
}
func Parse(cb func()) { walk(&H{}, &H{}, cb, 1) }
`,
		},
		{
			name: "recursive current aliases expose local closure store",
			source: `package dep
type C struct{ f func() }
type H struct{ child C }
var saved func()
func walk(a, b *H, cb func(), n int) {
	if n > 0 {
		walk(a, a, cb, n-1)
	}
	store := func() { a.child.f = cb }
	store()
	saved = b.child.f
}
func Parse(cb func()) { walk(&H{}, &H{}, cb, 1) }
`,
		},
		{
			name: "recursive fresh local helper closure",
			source: `package dep
type labelScope struct { errh func(error) }
func (ls *labelScope) blockBranches(n int) {
	innerBlock := func(next int) {
		ls.blockBranches(next)
	}
	if n > 0 {
		innerBlock(n-1)
	}
	if ls.errh != nil {
		ls.errh(nil)
	}
}
func Parse(cb func(error)) {
	ls := &labelScope{errh: cb}
	ls.blockBranches(1)
}
`,
			want: true,
		},
		{
			name: "recursive call result assignment is not callback store",
			source: `package dep
type labelScope struct { errh func(error) }
type block struct { parent *block }
type stmt struct{}
func (ls *labelScope) blockBranches(parent *block, n int, body []stmt) []stmt {
	b := &block{parent: parent}
	innerBlock := func(body []stmt) {
		_ = b
		body = append(body, ls.blockBranches(b, n-1, body)...)
	}
	if n > 0 {
		innerBlock(body)
	}
	if ls.errh != nil {
		ls.errh(nil)
	}
	return body
}
func Parse(cb func(error)) {
	ls := &labelScope{errh: cb}
	ls.blockBranches(nil, 1, nil)
}
`,
			want: true,
		},
		{
			name: "recursive mixed short declaration existing cell is unproved",
			source: `package dep
type labelScope struct { errh func(error) }
var saved func(error)
func (ls *labelScope) blockBranches(n int) {
	var retained func()
	retained, innerBlock := func() { saved = ls.errh }, func(next int) {
		ls.blockBranches(next)
	}
	_ = retained
	if n > 0 {
		innerBlock(n-1)
	}
}
func Parse(cb func(error)) {
	ls := &labelScope{errh: cb}
	ls.blockBranches(1)
}
`,
		},
		{
			name: "direct star value copy retains callback after source field clear",
			source: `package dep
type H struct{ f func() }
var saved func()
func Retain(cb func()) {
	h := &H{f: cb}
	copied := *h
	h.f = nil
	saved = copied.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			name: "recursive star value copy retains callback after source field clear",
			source: `package dep
type H struct{ f func() }
var saved func()
func walk(h *H, n int) {
	copied := *h
	h.f = nil
	if n > 0 {
		walk(h, n-1)
	}
	saved = copied.f
}
func Parse(cb func()) { walk(&H{f: cb}, 1) }
`,
		},
		{
			name: "direct star value copy nested alias keeps later callback",
			source: `package dep
type C struct{ f func() }
type H struct{ child *C }
var saved func()
func Retain(cb func()) {
	c := &C{}
	h := &H{child: c}
	copied := *h
	c.f = cb
	saved = copied.child.f
}
func Parse(cb func()) { Retain(cb) }
`,
		},
		{
			// Retain reads a pointer of unproven origin, so the copy it makes
			// is modelled as possibly holding a callback -- but nothing Parse
			// hands Retain reaches one, and no package variable can hold one in
			// a state this proof has not refused, so Retain cannot name cb at
			// all and is summarized without being walked. The conservatism
			// about unknown dereferences is kept exactly where it is
			// load-bearing, by the case below.
			name: "unknown star value copy in a callback-unreachable callee",
			source: `package dep
type H struct{ f func() }
var saved func()
func Retain(h *H) {
	copied := *h
	saved = copied.f
}
func Parse(cb func()) { Retain(nil) }
`,
			want: true,
		},
		{
			// The same unknown dereference in a callee that does reach the
			// callback -- through a second argument it never even reads. The
			// body is walked and the copy is refused.
			name: "unknown star value copy hides callback from a reaching callee",
			source: `package dep
type H struct{ f func() }
type keep struct{ cb func() }
var saved func()
func Retain(h *H, k *keep) {
	copied := *h
	saved = copied.f
}
func Parse(cb func()) { Retain(nil, &keep{cb: cb}) }
`,
		},
		{
			// The field is not a certified scalar leaf, so it is not omitted
			// from the frame snapshot; lazily materializing it still leaves
			// the callback exactly where it was, so the recursion is a
			// fixpoint. TestS281CallbackProofLazyFieldScalarCertification
			// guards the classification itself.
			name: "pointer scalar-looking field does not move the callback",
			source: `package dep
type Pointer *int
type parser struct { pos Pointer; callback func() }
func (p *parser) walk(n int) { _ = p.pos; if n > 0 { p.walk(n-1) }; p.callback() }
func Parse(cb func()) { var p parser; p.callback = cb; p.walk(1) }
`,
			want: true,
		},
		{
			name: "local declaration shadows scalar predeclared name",
			source: `package dep
type bool struct { f func() }
type parser struct { bad bool; callback func() }
func (p *parser) walk(n int) { _ = p.bad; if n > 0 { p.walk(n-1) }; p.callback() }
func Parse(cb func()) { var p parser; p.callback = cb; p.walk(1) }
`,
			want: true,
		},
		{
			name: "generic field does not move the callback",
			source: `package dep
type Box[T any] struct { value T }
type parser struct { box Box[int]; callback func() }
func (p *parser) walk(n int) { _ = p.box; if n > 0 { p.walk(n-1) }; p.callback() }
func Parse(cb func()) { var p parser; p.callback = cb; p.walk(1) }
`,
			want: true,
		},
		{
			// The same shapes do retain once the lazily materialized field
			// actually receives the callback.
			name: "pointer scalar-looking field can still retain",
			source: `package dep
type Pointer *int
type carrier struct { pos Pointer; f func() }
var saved *carrier
func (p *carrier) walk(n int) { _ = p.pos; if n > 0 { p.walk(n-1) }; saved = p }
func Parse(cb func()) { p := &carrier{}; p.f = cb; p.walk(1) }
`,
		},
		{
			name: "generic field can still retain",
			source: `package dep
type Box[T any] struct { value T }
type carrier struct { box Box[int]; f func() }
var saved func()
func (p *carrier) walk(n int) { _ = p.box; if n > 0 { p.walk(n-1) }; saved = p.f }
func Parse(cb func()) { p := &carrier{}; p.f = cb; p.walk(1) }
`,
		},
		{
			name: "named scalar method can retain late callback",
			source: `package dep
var saved func()
type Kind uint8
func (k Kind) keep(cb func()) { saved = cb }
type parser struct { kind Kind }
func (p *parser) walk(cb func(), n int) { _ = p.kind; if n > 0 { p.walk(cb, n-1) }; p.kind.keep(cb) }
func Parse(cb func()) { var p parser; p.walk(cb, 1) }
`,
		},
		{
			name: "cross argument lazy scalar child does not hide callback alias",
			source: `package dep
var saved func()
type State struct { kind uint8; f func() }
type parser struct { state State }
func walk(a, b *parser, cb func(), n int) {
	_ = a.state.kind
	if n > 0 { walk(a, a, cb, n-1) }
	a.state.f = cb
	saved = b.state.f
}
func Parse(cb func()) { walk(&parser{}, &parser{}, cb, 1) }
`,
		},
		{
			name: "lazy child recursion keeps aggregate field",
			source: `package dep
var saved func()
type child struct { f func() }
type parser struct { count int; child child }
func (p *parser) walk(cb func(), n int) {
	_ = p.count
	_ = p.child
	if n > 0 { p.walk(cb, n-1) }
	p.child.f = cb
	saved = p.child.f
}
func Parse(cb func()) { var p parser; p.walk(cb, 1) }
`,
		},
		{
			name: "retained scalar-looking value with callback field",
			source: `package dep
var saved func()
type uint8 struct { f func() }
type parser struct { kind uint8 }
func retain(k uint8) { saved = k.f }
func (p *parser) walk(cb func(), n int) {
	_ = p.kind
	if n > 0 { p.walk(cb, n-1) }
	p.kind.f = cb
	retain(p.kind)
}
func Parse(cb func()) { var p parser; p.walk(cb, 1) }
`,
		},
		{
			name: "escaped clean child cannot later receive callback",
			source: `package dep
var saved *child
type child struct{ f func() }
type holder struct{ child *child }
func keep(c *child) { saved = c }
func (h *holder) walk(cb func(), choose bool) {
	keep(h.child)
	if choose {
		h.walk(cb, false)
		return
	}
	h.child.f = cb
}
func Parse(cb func(), choose bool) {
	h := &holder{}
	h.walk(cb, choose)
}
`,
		},
		{
			name: "stable recursive synchronous callback",
			source: `package dep
func walk(cb func(), n int) { if n > 0 { walk(cb, n-1) } else { cb() } }
func Parse(cb func()) { walk(cb, 1) }
`,
			want: true,
		},
		{
			name: "value returning closure synchronously invokes callback",
			source: `package dep
func list(each func() bool) { _ = each() }
func Parse(cb func()) { list(func() bool { cb(); return true }) }
`,
			want: true,
		},
		{
			name: "value returning closure still cannot return callback",
			source: `package dep
var saved func()
func list(each func() func()) { saved = each() }
func Parse(cb func()) { list(func() func() { return cb }) }
`,
		},
		{
			name: "lexical local shadows global",
			source: `package dep
type ErrorHandler func(error)
var retained ErrorHandler
func Parse(errh ErrorHandler) { var retained ErrorHandler; retained = errh; retained(nil) }
`,
			want: true,
		},
		{
			name: "nested short declaration shadows outer",
			source: `package dep
func Parse(cb func()) { var f func(); { f := cb; f() }; _ = f }
`,
			want: true,
		},
		{
			name: "recursive outer short declaration shadow is fresh local",
			source: `package dep
type labelScope struct { errh func(error) }
func (ls *labelScope) walk(n int) {
	inner := func() {}
	{
		inner := func(next int) { ls.walk(next) }
		if n > 0 { inner(n-1) }
	}
	inner()
	if ls.errh != nil { ls.errh(nil) }
}
func Parse(cb func(error)) { ls := &labelScope{errh: cb}; ls.walk(1) }
`,
			want: true,
		},
		{
			name: "recursive select clause store is unproved",
			source: `package dep
type holder struct { f func() }
func (h *holder) walk(cb func(), ch chan int, n int) {
	if n > 0 { h.walk(cb, ch, n-1) }
	select {
	case <-ch:
		h.f = cb
	default:
	}
}
func Parse(cb func(), ch chan int) { h := &holder{}; h.walk(cb, ch, 1) }
`,
		},
		{
			name: "recursive literal closure effect is not skipped",
			source: `package dep
var saved func()
type holder struct { f func() }
func (h *holder) walk(cb func(), n int) {
	if n > 0 {
		h.walk(cb, n-1)
	}
	func() {
		h.f = cb
	}()
	saved = h.f
}
func Parse(cb func()) { h := &holder{}; h.walk(cb, 1) }
`,
		},
		{
			name: "recursive RHS factory effect is not skipped",
			source: `package dep
var saved func()
type holder struct { f func() }
func factory(h *holder, cb func()) func() {
	h.f = cb
	return func() {}
}
func (h *holder) walk(cb func(), n int) {
	if n > 0 {
		h.walk(cb, n-1)
	}
	_ = factory(h, cb)
	saved = h.f
}
func Parse(cb func()) { h := &holder{}; h.walk(cb, 1) }
`,
		},
		{
			name: "branch literal closure effect remains persistent taint",
			source: `package dep
var saved func()
type holder struct { f func() }
func Parse(cb func(), choose bool) {
	h := &holder{}
	if choose {
		func() { h.f = cb }()
	}
	saved = h.f
}
`,
		},
		{
			name: "conditional synchronous callback",
			source: `package dep
func Parse(cb func(), choose bool) { if choose { cb() } }
`,
			want: true,
		},
		{
			name: "loop synchronous callback",
			source: `package dep
func Parse(cb func(), choose bool) { for choose { cb(); choose = false } }
`,
			want: true,
		},
		{
			name: "completed frame later object taint",
			source: `package dep
var saved func()
type holder struct { f func() }
func inspect(h *holder) { if h.f != nil { saved = h.f } }
func Parse(cb func()) { h := &holder{f: func(){}}; inspect(h); h.f = cb; inspect(h) }
`,
		},
		{
			name: "completed frame later closure capture",
			source: `package dep
var saved func()
func inspect(f func()) { if f != nil { saved = f } }
func Parse(cb func()) { inspect(func(){}); inspect(func(){ cb() }) }
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "dep.go", test.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			got := newDependencyCallbackProof([]*ast.File{file}).prove("Parse", []int{0})
			if got != test.want {
				t.Fatalf("proof=%v, want %v", got, test.want)
			}
		})
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestS281ManagerCallbackProofHelperClosureCellEffect(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{
			name: "helper closure write taints captured cell observed later",
			source: `package dep
var saved func()
func run(fn func()) { fn() }
func Retain(cb func(), choose bool) {
	var f func()
	setter := func() { f = cb }
	getter := func() { saved = f }
	if choose {
		run(setter)
	}
	getter()
}
func Parse(cb func(), choose bool) { Retain(cb, choose) }
`,
		},
		{
			name: "nested helper closure write taints captured cell observed later",
			source: `package dep
var saved func()
func run(fn func()) { fn() }
func Retain(cb func(), choose bool) {
	var f func()
	setter := func() {
		inner := func() { f = cb }
		inner()
	}
	getter := func() { saved = f }
	if choose {
		run(setter)
	}
	getter()
}
func Parse(cb func(), choose bool) { Retain(cb, choose) }
`,
		},
		{
			name: "block local closure keeps captured cell alive",
			source: `package dep
var saved func()
func Parse(cb func()) {
	var getter func()
	{
		var f func() = cb
		getter = func() { saved = f }
	}
	getter()
}
`,
		},
		{
			name: "direct helper callback invocation remains synchronous",
			source: `package dep
func run(fn func()) { fn() }
func Parse(cb func()) { run(func() { cb() }) }
`,
			want: true,
		},
		{
			name: "helper parameter and local shadow captured names",
			source: `package dep
func run(fn func()) { fn() }
func Parse(cb func()) {
	var f func()
	setter := func(f func()) {
		local := cb
		f = local
		f()
	}
	run(func() { setter(func() {}) })
	_ = f
}
`,
			want: true,
		},
		{
			name: "nested helper parameter and local shadow captured names",
			source: `package dep
func run(fn func()) { fn() }
func Parse(cb func()) {
	var f func()
	setter := func() {
		inner := func(cb func()) {
			var f func()
			f = cb
			f()
		}
		inner(func() {})
	}
	run(setter)
	_ = f
}
`,
			want: true,
		},
		{
			name: "mixed short declaration reuses same block captured cell",
			source: `package dep
var saved func()
func Parse(cb func()) {
	var f func()
	getter := func() { saved = f }
	f, n := cb, 1
	_ = n
	getter()
}
`,
		},
		{
			name: "labeled mixed short declaration reuses same block captured cell",
			source: `package dep
var saved func()
func Parse(cb func()) {
	var f func()
	getter := func() { saved = f }
	goto L
L:
	f, n := cb, 1
	_ = n
	getter()
}
`,
		},
		{
			name: "goto cannot skip callback clear",
			source: `package dep
var saved func()
func Parse(cb func()) {
	f := cb
	goto L
	f = nil
L:
	saved = f
}
`,
		},
		{
			name: "mixed short declaration shadows outer captured cell",
			source: `package dep
var saved func()
func Parse(cb func()) {
	var f func()
	getter := func() { saved = f }
	{
		f, n := cb, 1
		_ = n
		f()
	}
	getter()
}
`,
			want: true,
		},
		{
			name: "short declaration shadow initializer retains callback",
			source: `package dep
var saved func()
func Parse(cb func()) {
	{
		cb := cb
		saved = cb
	}
}
`,
		},
		{
			name: "var declaration shadow initializer retains callback",
			source: `package dep
var saved func()
func Parse(cb func()) {
	{
		var cb = cb
		saved = cb
	}
}
`,
		},
		{
			name: "caller join keeps distinct shadow capture cell",
			source: `package dep
var saved func()
func Parse(cb func()) {
	var f func()
	var invoke func()
	{
		var f func() = cb
		invoke = func() { f() }
	}
	invoke()
	saved = f
}
`,
			want: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "dep.go", test.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			got := newDependencyCallbackProof([]*ast.File{file}).prove("Parse", []int{0})
			if got != test.want {
				t.Fatalf("proof=%v, want %v", got, test.want)
			}
		})
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofRecursiveCloneLineage(t *testing.T) {
	callback := dependencyCallbackValue{callback: &dependencyCallbackClosure{}}
	shared := &dependencyCallbackCell{value: callback}
	receiver := dependencyCallbackValue{object: &dependencyCallbackObject{
		typ: "parser", owned: true,
		fields: map[string]dependencyCallbackValue{
			"left":  {cell: shared},
			"right": {cell: shared},
		},
	}}
	active := dependencyCallbackActiveFrame{
		receiver:         receiver,
		receiverSnapshot: dependencyCallbackValueSnapshot(receiver),
	}

	clone := newDependencyCallbackGraphCloner().value(receiver)
	clone.object.fields["offset"] = dependencyCallbackValue{object: &dependencyCallbackObject{
		typ: "uint", owned: true, synthetic: true, scalar: true,
		fields: make(map[string]dependencyCallbackValue),
	}}
	if same, generalized, rejection := dependencyCallbackSameFrame(active, nil, clone); !same || generalized || rejection != "" {
		t.Fatalf("lineage-preserving clone with clean lazy scalar = (%v, %v, %q), want (true, false, empty)", same, generalized, rejection)
	}

	distinctCell := &dependencyCallbackCell{value: callback}
	distinct := dependencyCallbackValue{object: &dependencyCallbackObject{
		typ: "parser", owned: true,
		fields: map[string]dependencyCallbackValue{
			"left":  {cell: distinctCell},
			"right": {cell: distinctCell},
		},
	}}
	if same, _, _ := dependencyCallbackSameFrame(active, nil, distinct); same {
		t.Fatal("structurally similar distinct receiver was admitted without clone lineage")
	}

	brokenAlias := newDependencyCallbackGraphCloner().value(receiver)
	brokenAlias.object.fields["right"] = dependencyCallbackValue{cell: &dependencyCallbackCell{
		lineage: shared,
		value:   callback,
	}}
	if same, _, _ := dependencyCallbackSameFrame(active, nil, brokenAlias); same {
		t.Fatal("clone with changed cell alias identity was admitted")
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofActiveFrameFreezesAliasedEntryGraph(t *testing.T) {
	callback := dependencyCallbackValue{callback: &dependencyCallbackClosure{}}
	shared := &dependencyCallbackCell{value: callback}
	receiver := dependencyCallbackValue{object: &dependencyCallbackObject{
		typ: "parser", owned: true,
		fields: map[string]dependencyCallbackValue{"callback": {cell: shared}},
	}}
	supplied := map[string]dependencyCallbackValue{"callback": {cell: shared}}

	active := dependencyCallbackNewActiveFrame(supplied, receiver)
	frozenReceiverCell := active.receiver.object.fields["callback"].cell
	if frozenReceiverCell == shared {
		t.Fatal("active receiver retained the mutable live cell")
	}
	if frozenReceiverCell != active.supplied["callback"].cell {
		t.Fatal("active frame clone lost receiver/argument cell alias")
	}
	shared.value = dependencyCallbackValue{}
	if !active.receiver.tainted() || !active.supplied["callback"].tainted() {
		t.Fatal("live mutation changed immutable active-frame callback facts")
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofRecursiveGeneralizationUsesCurrentAliases(t *testing.T) {
	a := dependencyCallbackValue{object: &dependencyCallbackObject{typ: "H", owned: true, fields: make(map[string]dependencyCallbackValue)}}
	b := dependencyCallbackValue{object: &dependencyCallbackObject{typ: "H", owned: true, fields: make(map[string]dependencyCallbackValue)}}
	active := dependencyCallbackNewActiveFrame(map[string]dependencyCallbackValue{"a": a, "b": b}, dependencyCallbackValue{})
	before := dependencyCallbackFrameSnapshot(active.supplied, active.receiver)
	current := map[string]dependencyCallbackValue{"a": a, "b": a}

	same, generalized, rejection := dependencyCallbackSameFrame(active, current, dependencyCallbackValue{})
	if !same || !generalized || rejection != "" {
		t.Fatalf("recursive alias generalization = (%v, %v, %q), want (true, true, empty)", same, generalized, rejection)
	}
	if after := dependencyCallbackFrameSnapshot(active.supplied, active.receiver); after != before {
		t.Fatalf("same-frame comparison mutated immutable entry graph:\nbefore %s\nafter  %s", before, after)
	}
	if !current["a"].object.general || current["a"].object != current["b"].object {
		t.Fatal("current recursive aliases did not retain identity and generalization")
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofRecursiveStoreScanDoesNotMutateActiveGraph(t *testing.T) {
	source := `package dep
type holder struct { f func() }
func walk(h *holder, cb func()) { h.f = cb }
`
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	active := dependencyCallbackActiveFrame{
		supplied: map[string]dependencyCallbackValue{
			"h":  {object: &dependencyCallbackObject{typ: "holder", owned: true, general: true, fields: make(map[string]dependencyCallbackValue)}},
			"cb": {callback: &dependencyCallbackClosure{}},
		},
	}
	before := dependencyCallbackFrameSnapshot(active.supplied, dependencyCallbackValue{})
	if !proof.recursiveBodyStoresCallback(proof.funcs["walk"][0], "walk", active.supplied, active.receiver, 0) {
		t.Fatal("recursive store scan did not observe the callback store")
	}
	after := dependencyCallbackFrameSnapshot(active.supplied, dependencyCallbackValue{})
	if after != before {
		t.Fatalf("recursive store scan mutated active graph:\nbefore %s\nafter  %s", before, after)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofBranchScanDoesNotMutateLiveGraph(t *testing.T) {
	source := `package dep
func Parse(cb func(), choose bool) {
	if choose {
		h.f = cb
	}
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	env := map[string]dependencyCallbackValue{
		"h":  {object: &dependencyCallbackObject{owned: true, fields: make(map[string]dependencyCallbackValue)}},
		"cb": {callback: &dependencyCallbackClosure{}},
	}
	before := dependencyCallbackFrameSnapshot(env, dependencyCallbackValue{})
	if proof.statement(proof.funcs["Parse"][0].Body.List[0], env, 0) {
		t.Fatal("branch statement unexpectedly accepted callback taint")
	}
	after := dependencyCallbackFrameSnapshot(env, dependencyCallbackValue{})
	if after != before {
		t.Fatalf("branch scan mutated live graph:\nbefore %s\nafter  %s", before, after)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofStoreDiagnosticNilFileSet(t *testing.T) {
	source := `package dep
type holder struct { f func() }
func walk(a, b *holder, cb func(), n int) {
	if n > 0 { walk(a, a, cb, n-1) }
	b.f = cb
}
func Parse(cb func()) { walk(&holder{}, &holder{}, cb, 1) }
`
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.diagnostics = true
	if proof.prove("Parse", []int{0}) {
		t.Fatal("proof unexpectedly accepted generalized recursive store")
	}
	joined := strings.Join(proof.diagnostic, "\n")
	if !strings.Contains(joined, "store=assign lhs=b.f rhs=cb") {
		t.Fatalf("diagnostic missing store context:\n%s", joined)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofRecursiveDiagnostic(t *testing.T) {
	source := `package dep
func walk(cb func()) { next := func() { cb() }; walk(next) }
func Parse(cb func()) { walk(cb) }
`
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.diagnostics = true
	if proof.prove("Parse", []int{0}) {
		t.Fatal("proof unexpectedly accepted recursive callback substitution")
	}
	joined := strings.Join(proof.diagnostic, "\n")
	for _, want := range []string{
		"recursive frame difference for walk",
		"arg.cb active=closure#",
		"current=closure#",
		"captures=cb",
		"recursive call changes callback capture state",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, joined)
		}
	}
	if len(proof.diagnostic) > dependencyCallbackProofDiagnosticLimit {
		t.Fatalf("diagnostic has %d lines, limit %d", len(proof.diagnostic), dependencyCallbackProofDiagnosticLimit)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofRecursiveDiagnosticUsesEntryGraph(t *testing.T) {
	source := `package dep
type holder struct { f func() }
func walk(h *holder, cb func()) { h.f = cb; walk(h, cb) }
func Parse(cb func()) { h := &holder{}; walk(h, cb) }
`
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.diagnostics = true
	if proof.prove("Parse", []int{0}) {
		t.Fatal("proof unexpectedly accepted recursive object mutation")
	}
	joined := strings.Join(proof.diagnostic, "\n")
	for _, want := range []string{
		"same-frame rejection: argument h callback reachability changed after entry",
		"recursive frame difference for walk",
		"arg.h active=object#",
		"current=object#",
		"taint=false",
		"taint=true",
		"arg.h.f active=<missing> current=closure#",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "<no graph path difference>") {
		t.Fatalf("diagnostic reported no graph path difference:\n%s", joined)
	}
	if len(proof.diagnostic) > dependencyCallbackProofDiagnosticLimit {
		t.Fatalf("diagnostic has %d lines, limit %d", len(proof.diagnostic), dependencyCallbackProofDiagnosticLimit)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofRecursiveStoreDiagnosticUsesAssignmentSite(t *testing.T) {
	source := `package dep
type holder struct { f func() }
func walk(a, b *holder, cb func(), n int) {
	if n > 0 { walk(a, a, cb, n-1) }
	b.f = cb
}
func Parse(cb func()) { walk(&holder{}, &holder{}, cb, 1) }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.fset = fset
	proof.diagnostics = true
	if proof.prove("Parse", []int{0}) {
		t.Fatal("proof unexpectedly accepted generalized recursive store")
	}
	joined := strings.Join(proof.diagnostic, "\n")
	for _, want := range []string{
		"dep.go:4:",
		"selector assignment stores callback-bearing value in generalized recursive region",
		"store=assign lhs=b.f rhs=cb",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, joined)
		}
	}
	if len(proof.diagnostic) > dependencyCallbackProofDiagnosticLimit {
		t.Fatalf("diagnostic has %d lines, limit %d", len(proof.diagnostic), dependencyCallbackProofDiagnosticLimit)
	}
}

func TestDependencyCallbackProofCompilerSyntaxSource(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "src", "cmd", "compile", "internal", "syntax")
	selected := []string{
		"syntax.go",
		"parser.go",
		"scanner.go",
		"source.go",
		"branches.go",
		"tokens.go",
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(selected))
	for _, name := range selected {
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, file)
	}
	for _, test := range []struct {
		name string
		fn   string
		args []int
	}{
		{name: "Parse", fn: "Parse", args: []int{2}},
		{name: "ParseFile", fn: "ParseFile", args: []int{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := newDependencyCallbackProof(files)
			proof.fset = fset
			if !proof.prove(test.fn, test.args) {
				t.Fatalf("%s callback proof refused: %s", test.fn, proof.reason)
			}
		})
	}
}

// A call written inside a function literal runs in the literal's own scope, not
// where the literal is written. Walking it with the enclosing environment loses
// every binding the literal declares and misreads a shadowing parameter or local
// as the captured cell it shadows.
//
// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackProofNestedLiteralCallUsesClosureScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{{
		name: "nested literal local call resolves in closure scope",
		source: `package dep
func run(fn func()) { fn() }
func Parse(cb func()) {
	setter := func() {
		inner := func() {}
		inner()
		inner()
	}
	run(setter)
}
`,
		want: true,
	}, {
		name: "nested literal parameter shadows captured callback name",
		source: `package dep
func run(fn func()) { fn() }
func Parse(cb func()) {
	var f func()
	setter := func() {
		inner := func(cb func()) {
			var f func()
			f = cb
			f()
		}
		inner(func() {})
	}
	run(setter)
	_ = f
}
`,
		want: true,
	}, {
		name: "nested literal still leaks the captured callback when invoked",
		source: `package dep
var saved func()
func run(fn func()) { fn() }
func Parse(cb func()) {
	setter := func() {
		inner := func() { saved = cb }
		inner()
	}
	run(setter)
}
`,
	}, {
		name: "immediately invoked literal resolves its own locals",
		source: `package dep
func Parse(cb func()) {
	func() {
		inner := func(cb func()) { cb() }
		inner(func() {})
	}()
}
`,
		want: true,
	}, {
		name: "immediately invoked literal still leaks the captured callback",
		source: `package dep
var saved func()
func Parse(cb func()) {
	func() {
		inner := func() { saved = cb }
		inner()
	}()
}
`,
	}} {
		t.Run(test.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "dep.go", test.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			proof := newDependencyCallbackProof([]*ast.File{file})
			proof.fset = fset
			proof.diagnostics = true
			if got := proof.prove("Parse", []int{0}); got != test.want {
				t.Fatalf("proof=%v, want %v:\n%s", got, test.want, strings.Join(proof.diagnostic, "\n"))
			}
		})
	}
}

// The active frame records a frozen clone of the entry graph, so a recursive
// call never sees the pointer it was cloned from. Identity must therefore be
// decided by lineage of the value root alone: a mutated argument is the same
// argument with a changed snapshot, and the taint that matters is the one the
// live argument carries now.
//
// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestDependencyCallbackSameFrameSeparatesIdentityFromSnapshot(t *testing.T) {
	object := &dependencyCallbackObject{typ: "holder", owned: true, fields: map[string]dependencyCallbackValue{}}
	argument := dependencyCallbackValue{object: object}
	active := dependencyCallbackNewActiveFrame(map[string]dependencyCallbackValue{"h": argument}, dependencyCallbackValue{})

	if !dependencyCallbackSameIdentity(active.supplied["h"], argument) {
		t.Fatal("frozen entry clone is not the same argument as the live value")
	}
	if same, _, rejection := dependencyCallbackSameFrame(active, map[string]dependencyCallbackValue{"h": argument}, dependencyCallbackValue{}); !same {
		t.Fatalf("unmutated argument rejected: %s", rejection)
	}

	// A callback-free field appearing after entry leaves the callback
	// reachability unchanged: the frame is the same, generalized over the new
	// region, which the body summary must then keep callback-free.
	object.fields["n"] = dependencyCallbackValue{object: &dependencyCallbackObject{typ: "int", owned: true}}
	if same, generalized, rejection := dependencyCallbackSameFrame(active, map[string]dependencyCallbackValue{"h": argument}, dependencyCallbackValue{}); !same || !generalized {
		t.Fatalf("callback-free argument growth rejected: same=%v generalized=%v %s", same, generalized, rejection)
	}

	object.fields["f"] = dependencyCallbackValue{callback: &dependencyCallbackClosure{}}
	same, _, rejection := dependencyCallbackSameFrame(active, map[string]dependencyCallbackValue{"h": argument}, dependencyCallbackValue{})
	if same {
		t.Fatal("recursive call with a newly tainted argument accepted")
	}
	if want := "argument h callback reachability changed after entry"; rejection != want {
		t.Fatalf("rejection = %q, want %q", rejection, want)
	}
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// A field is dropped from the frame snapshot only when its type is certified
// to be a scalar leaf, because such a field can never come to hold a callback.
// Anything else — a named pointer type, a locally declared type that shadows a
// predeclared scalar name, an instantiated generic — must stay classified as a
// non-leaf so the snapshot keeps describing it.
func TestS281CallbackProofLazyFieldScalarCertification(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		typ    string
		field  string
		want   bool
	}{
		{
			name:   "predeclared scalar",
			source: "package dep\ntype parser struct { bad bool }\n",
			typ:    "parser", field: "bad", want: true,
		},
		{
			name:   "named scalar",
			source: "package dep\ntype LitKind uint8\ntype parser struct { kind LitKind }\n",
			typ:    "parser", field: "kind", want: true,
		},
		{
			name:   "alias of named scalar",
			source: "package dep\ntype LitKind uint8\ntype Alias = LitKind\ntype parser struct { kind Alias }\n",
			typ:    "parser", field: "kind", want: true,
		},
		{
			name:   "named pointer type",
			source: "package dep\ntype Pointer *int\ntype parser struct { pos Pointer }\n",
			typ:    "parser", field: "pos",
		},
		{
			name:   "local declaration shadows predeclared scalar name",
			source: "package dep\ntype bool struct { f func() }\ntype parser struct { bad bool }\n",
			typ:    "parser", field: "bad",
		},
		{
			name:   "instantiated generic",
			source: "package dep\ntype Box[T any] struct { value T }\ntype parser struct { box Box[int] }\n",
			typ:    "parser", field: "box",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "dep.go", test.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			shape, ok := newDependencyCallbackProof([]*ast.File{file}).types[test.typ]
			if !ok {
				t.Fatalf("type %s has no recorded shape", test.typ)
			}
			field, ok := shape.fields[test.field]
			if !ok {
				t.Fatalf("type %s has no field %s", test.typ, test.field)
			}
			if field.scalar != test.want {
				t.Fatalf("%s.%s scalar=%v, want %v", test.typ, test.field, field.scalar, test.want)
			}
		})
	}
}
