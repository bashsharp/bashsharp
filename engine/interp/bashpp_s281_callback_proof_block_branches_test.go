package interp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// s281BlockBranchesPrelude mirrors the shape of cmd/compile/internal/syntax's
// labelScope.blockBranches: a receiver that carries the callback in one field
// and grows an unrelated callback-free field (labels) while recursing, plus a
// closure that assigns the recursive result into a captured accumulator.
const s281BlockBranchesPrelude = `package dep

type Error struct{ msg string }

type ErrorHandler func(Error)

type Stmt struct {
	kind int
	list []*Stmt
}

type block struct {
	parent *block
	lstmt  *Stmt
	h      ErrorHandler
}

type label struct {
	parent *block
	lstmt  *Stmt
	used   bool
}

type labelScope struct {
	errh   ErrorHandler
	labels map[string]*label
}

func (ls *labelScope) errf(msg string) {
	ls.errh(Error{msg})
}

func (ls *labelScope) declare(b *block, s *Stmt, name string) *label {
	labels := ls.labels
	if labels == nil {
		labels = make(map[string]*label)
		ls.labels = labels
	} else if alt := labels[name]; alt != nil {
		ls.errf("label already defined")
		return alt
	}
	l := &label{b, s, false}
	labels[name] = l
	return l
}
`

// s281BlockBranchesEntry drives the walker the way checkBranches does.
const s281BlockBranchesEntry = `
func Retain(errh ErrorHandler, body []*Stmt) {
	ls := &labelScope{errh: errh}
	fwdGotos := ls.blockBranches(nil, nil, body)
	for _, fwd := range fwdGotos {
		if l := ls.labels["x"]; l != nil {
			l.used = true
			ls.errf("goto jumps into block")
		} else {
			_ = fwd
			ls.errf("label not defined")
		}
	}
}
`

func s281BlockBranchesSource(walker string) string {
	return s281BlockBranchesPrelude + walker + s281BlockBranchesEntry
}

func s281ProveBlockBranches(t *testing.T, source string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return newDependencyCallbackProof([]*ast.File{file}).prove("Retain", []int{0})
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// The real walker never stores the handler anywhere: it only calls it. The
// recursive call's result is a []*Stmt, which cannot carry a callback, and the
// only receiver mutation across the recursive edge is the lazily allocated
// labels map, which is callback-free. The proof must admit it.
func TestS281CallbackProofBlockBranchesCapturedAccumulator(t *testing.T) {
	const walker = `
func (ls *labelScope) blockBranches(parent *block, lstmt *Stmt, body []*Stmt) []*Stmt {
	b := &block{parent: parent, lstmt: lstmt}

	var fwdGotos, badGotos []*Stmt

	recordVarDecl := func() {
		badGotos = append(badGotos[:0], fwdGotos...)
	}

	jumpsOverVarDecl := func(fwd *Stmt) bool {
		for _, bad := range badGotos {
			if fwd == bad {
				return true
			}
		}
		return false
	}

	innerBlock := func(body []*Stmt) {
		fwdGotos = append(fwdGotos, ls.blockBranches(b, lstmt, body)...)
	}

	for _, s := range body {
		lstmt = nil
		switch s.kind {
		case 0:
			recordVarDecl()
		case 1:
			ls.declare(b, s, "x")
			lstmt = s
			innerBlock(s.list)
		case 2:
			innerBlock(s.list)
		default:
			if jumpsOverVarDecl(s) {
				ls.errf("goto jumps over declaration")
			}
			fwdGotos = append(fwdGotos, s)
		}
	}

	return fwdGotos
}
`
	if !s281ProveBlockBranches(t, s281BlockBranchesSource(walker)) {
		t.Fatal("callback-free recursive result through a captured accumulator was refused")
	}
}

// The same shape, but the closure now retains the handler. Each variant must
// stay refused.
func TestS281CallbackProofBlockBranchesRetainingVariants(t *testing.T) {
	for name, walker := range map[string]string{
		"closure_stores_callback_in_global": `
var stored ErrorHandler

func (ls *labelScope) blockBranches(parent *block, lstmt *Stmt, body []*Stmt) []*Stmt {
	b := &block{parent: parent, lstmt: lstmt}
	var fwdGotos []*Stmt
	innerBlock := func(body []*Stmt) {
		stored = ls.errh
		fwdGotos = append(fwdGotos, ls.blockBranches(b, lstmt, body)...)
	}
	for _, s := range body {
		lstmt = nil
		ls.declare(b, s, "x")
		innerBlock(s.list)
	}
	return fwdGotos
}
`,
		"closure_appends_callback_to_captured_accumulator": `
var kept []ErrorHandler

func (ls *labelScope) blockBranches(parent *block, lstmt *Stmt, body []*Stmt) []*Stmt {
	b := &block{parent: parent, lstmt: lstmt}
	var fwdGotos []*Stmt
	var handlers []ErrorHandler
	innerBlock := func(body []*Stmt) {
		handlers = append(handlers, ls.errh)
		fwdGotos = append(fwdGotos, ls.blockBranches(b, lstmt, body)...)
	}
	for _, s := range body {
		lstmt = nil
		ls.declare(b, s, "x")
		innerBlock(s.list)
	}
	kept = handlers
	return fwdGotos
}
`,
		"closure_stores_callback_bearing_value_in_global": `
var scopes []*labelScope

func (ls *labelScope) blockBranches(parent *block, lstmt *Stmt, body []*Stmt) []*Stmt {
	b := &block{parent: parent, lstmt: lstmt}
	var fwdGotos []*Stmt
	innerBlock := func(body []*Stmt) {
		scopes = append(scopes, &labelScope{errh: ls.errh})
		fwdGotos = append(fwdGotos, ls.blockBranches(b, lstmt, body)...)
	}
	for _, s := range body {
		lstmt = nil
		ls.declare(b, s, "x")
		innerBlock(s.list)
	}
	return fwdGotos
}
`,
		"recursion_stores_callback_into_the_generalized_parent": `
func (ls *labelScope) blockBranches(parent *block, lstmt *Stmt, body []*Stmt) []*Stmt {
	b := &block{parent: parent, lstmt: lstmt}
	var fwdGotos []*Stmt
	innerBlock := func(body []*Stmt) {
		fwdGotos = append(fwdGotos, ls.blockBranches(b, lstmt, body)...)
	}
	for _, s := range body {
		lstmt = nil
		ls.declare(b, s, "x")
		innerBlock(s.list)
	}
	if parent != nil {
		parent.h = ls.errh
	}
	return fwdGotos
}
`,
		"closure_stores_callback_in_captured_escaping_field": `
type carrier struct {
	h ErrorHandler
}

var carried *carrier

func (ls *labelScope) blockBranches(parent *block, lstmt *Stmt, body []*Stmt) []*Stmt {
	b := &block{parent: parent, lstmt: lstmt}
	var fwdGotos []*Stmt
	c := &carrier{}
	carried = c
	innerBlock := func(body []*Stmt) {
		c.h = ls.errh
		fwdGotos = append(fwdGotos, ls.blockBranches(b, lstmt, body)...)
	}
	for _, s := range body {
		lstmt = nil
		ls.declare(b, s, "x")
		innerBlock(s.list)
	}
	return fwdGotos
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if s281ProveBlockBranches(t, s281BlockBranchesSource(walker)) {
				t.Fatal("retaining recursive walker was admitted")
			}
		})
	}
}
