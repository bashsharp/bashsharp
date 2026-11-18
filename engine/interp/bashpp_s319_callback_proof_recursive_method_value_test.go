//go:build full

package interp

import (
	"strings"
	"testing"
)

// Sprint 319 story 2405313cf7e2 (#1086). Interpreted
// cmd/compile/internal/types2 refused TestTypeSetString at
// syntax.Parse(nil, strings.NewReader(src), errh, nil, 0) with "asynchronous or
// retained original function callbacks are unsupported": the whole-package
// lifetime proof of cmd/compile/internal/syntax.Parse had stopped certifying.
//
// The cause is the interaction of two rules, not either one alone. Story #1088
// made a method value carry its receiver, so `p.constDecl` became tainted
// wherever the parser holds the error handler -- correct, and required. But the
// recursion rule only knew how to re-bind a func-typed parameter from one local
// CLOSURE to another (story #810's places rule); a re-bind from one method value
// to another looked like a change of identity across tainted state and refused.
//
// parser.appendGroup is entered from fileOrNil as appendGroup(f.DeclList,
// p.constDecl) and re-entered, through constDecl -> ... -> stmtOrNil ->
// declStmt, as appendGroup(nil, p.constDecl): the same method of the same
// receiver, freshly selected, so a different wrapper object. These reductions
// pin the rule that admits it and the boundary that still refuses.

// dependencyCallbackRecursiveMethodValueSource is appendGroup's shape: a
// recursive body entered with a method value formed on a callback-bearing
// receiver, re-entered with the same method of the same receiver selected
// again. Nothing stores anything; the callback is only ever invoked.
const dependencyCallbackRecursiveMethodValueSource = `package dep

type parser struct {
	errh func(error)
	tok  int
}

func (p *parser) appendGroup(list []int, f func(int) int) []int {
	if p.tok == 0 {
		p.tok = 1
		list = append(list, f(1))
	} else {
		list = append(list, f(2))
	}
	return list
}

func (p *parser) constDecl(x int) int {
	p.errh(nil)
	if p.tok == 1 {
		p.tok = 2
		p.appendGroup(nil, p.constDecl)
	}
	return x
}

func Parse(errh func(error)) []int {
	p := &parser{errh: errh}
	return p.appendGroup(nil, p.constDecl)
}
`

// dependencyCallbackGrowingMethodValueSource is the boundary: the frame is
// entered with a local closure that reaches no callback at all, and the
// recursive edge re-binds the same parameter to a method value that reaches one
// through its receiver. That is one more place than the entry value held, so it
// must stay refused however provable each individual value is.
const dependencyCallbackGrowingMethodValueSource = `package dep

type parser struct {
	errh func(error)
	tok  int
}

func (p *parser) appendGroup(f func(int) int) {
	if p.tok == 0 {
		p.tok = 1
		f(1)
		p.appendGroup(p.constDecl)
	}
}

func (p *parser) constDecl(x int) int {
	p.errh(nil)
	return x
}

func Parse(errh func(error)) {
	p := &parser{errh: errh}
	p.appendGroup(func(x int) int { return x })
}
`

// dependencyCallbackRetainedMethodValueSource keeps story #1088's negative in
// view from this lane: the recursive edge is fine, but the body stores the
// method value into package state, so the callback outlives the call.
const dependencyCallbackRetainedMethodValueSource = `package dep

var saved func(int) int

type parser struct {
	errh func(error)
	tok  int
}

func (p *parser) appendGroup(f func(int) int) {
	if p.tok == 0 {
		p.tok = 1
		f(1)
		p.appendGroup(p.constDecl)
	}
}

func (p *parser) constDecl(x int) int {
	p.errh(nil)
	saved = p.constDecl
	return x
}

func Parse(errh func(error)) {
	p := &parser{errh: errh}
	p.appendGroup(p.constDecl)
}
`

// Sprint: #319; Story: #1086; Story-ID: 2405313cf7e2
func TestS319CallbackProofRecursiveMethodValue(t *testing.T) {
	ok, reason := dependencyCallbackProveSource(t, dependencyCallbackRecursiveMethodValueSource, "Parse", []int{0})
	if !ok {
		t.Fatalf("recursive method-value re-bind refused: %s", reason)
	}
}

// Sprint: #319; Story: #1086; Story-ID: 2405313cf7e2
func TestS319CallbackProofRecursiveMethodValueNegatives(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "places_grow", source: dependencyCallbackGrowingMethodValueSource},
		{name: "method_value_retained", source: dependencyCallbackRetainedMethodValueSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			ok, reason := dependencyCallbackProveSource(t, test.source, "Parse", []int{0})
			if ok {
				t.Fatal("callback-reachable recursive re-bind incorrectly admitted")
			}
			t.Logf("refused: %s", reason)
		})
	}
}

// dependencyCallbackFormerProofDepthBound is the bound this lane raised from.
// It is kept here so the measurement can show that the whole-package proof of
// syntax.Parse fails under it for a BUDGET reason and succeeds above it, rather
// than asserting a pinned depth that a toolchain bump would invalidate.
const dependencyCallbackFormerProofDepthBound = 64

// TestS319CallbackProofSyntaxParseDepth is the measurement the depth bound is
// set from. The walk charges two levels per nested Go frame -- one for the call,
// one for the body it enters -- so the former bound of 64 admitted only 32
// nested frames, and a recursive-descent parser needs more than that once method
// values are tainted and their bodies are therefore walked. Under the former
// bound the whole cmd/compile/internal/syntax package runs out of depth INSIDE a
// generalized body summary, which used to be silent; above it the proof
// completes well inside the step budget.
//
// Sprint: #319; Story: #1086; Story-ID: 2405313cf7e2
func TestS319CallbackProofSyntaxParseDepth(t *testing.T) {
	fset, files, _ := dependencyCallbackEnumerationFiles(t, nil)
	for _, test := range []struct {
		name string
		args []int
	}{
		{name: "Parse", args: []int{2}},
		{name: "ParseFile", args: []int{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := newDependencyCallbackProof(files)
			proof.fset = fset
			ok := proof.prove(test.name, test.args)
			t.Logf("syntax.%s: ok=%v steps=%d/%d maxDepth=%d/%d", test.name, ok,
				proof.steps, proof.stepLimit, proof.maxDepth, proof.depthLimit)
			if !ok {
				t.Fatalf("whole-package proof of syntax.%s refused: %s", test.name, proof.reason)
			}
			if proof.maxDepth >= proof.depthLimit {
				t.Fatalf("syntax.%s rode the depth bound: maxDepth=%d limit=%d", test.name, proof.maxDepth, proof.depthLimit)
			}

			former := newDependencyCallbackProof(files)
			former.fset = fset
			former.depthLimit = dependencyCallbackFormerProofDepthBound
			if former.prove(test.name, test.args) {
				t.Fatalf("syntax.%s no longer needs more than depth %d: this measurement has stopped measuring",
					test.name, dependencyCallbackFormerProofDepthBound)
			}
			t.Logf("syntax.%s under the former bound: maxDepth=%d reason=%s", test.name, former.maxDepth, former.reason)
			if !strings.Contains(former.reason, "bounds exceeded") {
				t.Fatalf("syntax.%s under the former bound refused for a rule, not a budget: %s", test.name, former.reason)
			}
		})
	}
}
