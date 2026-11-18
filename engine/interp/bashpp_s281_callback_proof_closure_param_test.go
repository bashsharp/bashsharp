package interp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// s281ClosureParamPrelude mirrors the shape of cmd/compile/internal/syntax's
// parser.list: a receiver that carries the callback in one field, and a
// func-typed parameter that every caller binds to a *different local closure*
// which parses further and re-enters list. The only real callback is the error
// handler, and it is only ever called.
const s281ClosureParamPrelude = `package dep

type Error struct{ msg string }

type ErrorHandler func(Error)

type Node struct {
	kind int
	list []*Node
}

type parser struct {
	errh ErrorHandler
	tok  int
	base int
}

func (p *parser) errorAt(msg string) {
	p.errh(Error{msg})
}

func (p *parser) got(sep int) bool {
	if p.tok == sep {
		p.tok = p.tok + 1
		return true
	}
	return false
}
`

// s281ClosureParamList is the function under test. f is called, never stored.
const s281ClosureParamList = `
func (p *parser) list(context string, sep int, close int, f func() bool) int {
	done := false
	for p.tok != 0 && p.tok != close && !done {
		done = f()
		if !p.got(sep) && p.tok != close {
			p.errorAt("in " + context)
			return p.tok
		}
	}
	return p.tok
}
`

// s281ClosureParamEntry drives the parser the way syntax.Parse does: the
// handler is installed in the receiver and the walk begins.
const s281ClosureParamEntry = `
func Parse(errh ErrorHandler) []*Node {
	p := &parser{errh: errh, tok: 5}
	return p.argList()
}

func (p *parser) expr() *Node {
	n := &Node{kind: p.tok}
	if p.tok == 5 {
		n.list = p.argList()
	} else if p.tok == 6 {
		n.list = p.structType()
	} else if p.tok == 7 {
		n.list = p.interfaceType()
	}
	return n
}
`

func s281ClosureParamSource(callers string) string {
	return s281ClosureParamPrelude + s281ClosureParamList + callers + s281ClosureParamEntry
}

func s281ProveClosureParam(t *testing.T, source string) (bool, string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dep.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	proof := newDependencyCallbackProof([]*ast.File{file})
	proof.fset = fset
	return proof.prove("Parse", []int{0}), proof.reason
}

// s281ClosureParamCallers binds three distinct local closures to the same
// func-typed parameter of list, and each of them re-enters list through expr.
const s281ClosureParamCallers = `
func (p *parser) argList() []*Node {
	var args []*Node
	hasDots := false
	p.list("argument list", 1, 2, func() bool {
		args = append(args, p.expr())
		hasDots = p.tok == 9
		return hasDots
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	named := 0
	p.list("struct type", 3, 4, func() bool {
		fields = append(fields, p.expr())
		named = named + 1
		return false
	})
	return fields
}

func (p *parser) interfaceType() []*Node {
	var methods []*Node
	p.list("interface type", 3, 4, func() bool {
		methods = append(methods, p.expr())
		return false
	})
	return methods
}
`

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// list never stores f: it calls it. Each caller binds a different local
// closure, so the recursive edge into list changes the *code* of the
// func-typed parameter while every callback-bearing part of the frame -- the
// receiver's handler field, reachable from each closure's captured p -- stays
// exactly where it was. The proof must admit it.
func TestS281CallbackProofClosureParameterRecursion(t *testing.T) {
	ok, reason := s281ProveClosureParam(t, s281ClosureParamSource(s281ClosureParamCallers))
	if !ok {
		t.Fatalf("recursion through a local closure bound to a func-typed parameter was refused: %s", reason)
	}
}

// Each variant retains the callback somewhere, from inside a closure the
// recursive edge introduces. Every one must stay refused.
func TestS281CallbackProofClosureParameterRetainingVariants(t *testing.T) {
	for name, callers := range map[string]string{
		// The second closure stores the callback itself into a global.
		"closure_parameter_stores_callback_in_global": `
var stored ErrorHandler

func (p *parser) argList() []*Node {
	var args []*Node
	p.list("argument list", 1, 2, func() bool {
		args = append(args, p.expr())
		return false
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	p.list("struct type", 3, 4, func() bool {
		stored = p.errh
		fields = append(fields, p.expr())
		return false
	})
	return fields
}

func (p *parser) interfaceType() []*Node {
	var methods []*Node
	p.list("interface type", 3, 4, func() bool {
		methods = append(methods, p.expr())
		return false
	})
	return methods
}
`,
		// The second closure stores a value that reaches the callback.
		"closure_parameter_stores_callback_bearing_value_in_global": `
var parsers []*parser

func (p *parser) argList() []*Node {
	var args []*Node
	p.list("argument list", 1, 2, func() bool {
		args = append(args, p.expr())
		return false
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	p.list("struct type", 3, 4, func() bool {
		parsers = append(parsers, &parser{errh: p.errh})
		fields = append(fields, p.expr())
		return false
	})
	return fields
}

func (p *parser) interfaceType() []*Node {
	var methods []*Node
	p.list("interface type", 3, 4, func() bool {
		methods = append(methods, p.expr())
		return false
	})
	return methods
}
`,
		// The second closure parks the callback in a captured accumulator
		// whose contents outlive the call.
		"closure_parameter_appends_callback_to_captured_accumulator": `
var kept []ErrorHandler

func (p *parser) argList() []*Node {
	var args []*Node
	p.list("argument list", 1, 2, func() bool {
		args = append(args, p.expr())
		return false
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	var handlers []ErrorHandler
	p.list("struct type", 3, 4, func() bool {
		handlers = append(handlers, p.errh)
		fields = append(fields, p.expr())
		return false
	})
	kept = handlers
	return fields
}

func (p *parser) interfaceType() []*Node {
	var methods []*Node
	p.list("interface type", 3, 4, func() bool {
		methods = append(methods, p.expr())
		return false
	})
	return methods
}
`,
		// The third closure -- reachable only from inside the second one --
		// stores the callback. A per-function summary shortcut would miss it.
		"second_level_closure_parameter_stores_callback_in_global": `
var stored ErrorHandler

func (p *parser) argList() []*Node {
	var args []*Node
	p.list("argument list", 1, 2, func() bool {
		args = append(args, p.expr())
		return false
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	p.list("struct type", 3, 4, func() bool {
		fields = append(fields, p.expr())
		return false
	})
	return fields
}

func (p *parser) interfaceType() []*Node {
	var methods []*Node
	p.list("interface type", 3, 4, func() bool {
		stored = p.errh
		methods = append(methods, p.expr())
		return false
	})
	return methods
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if ok, _ := s281ProveClosureParam(t, s281ClosureParamSource(callers)); ok {
				t.Fatal("a closure parameter that retains the callback was admitted")
			}
		})
	}
}

// The func-typed parameter may also hand the callback back through its result.
// list stores whatever f returns, so a closure returning the handler retains
// it and must stay refused, while one returning nothing must not.
func TestS281CallbackProofClosureParameterResult(t *testing.T) {
	const returningList = `
var kept ErrorHandler

func (p *parser) list(context string, sep int, close int, f func() ErrorHandler) int {
	for p.tok != 0 && p.tok != close {
		kept = f()
		if !p.got(sep) && p.tok != close {
			p.errorAt("in " + context)
			return p.tok
		}
	}
	return p.tok
}
`
	const callers = `
func (p *parser) argList() []*Node {
	var args []*Node
	p.list("argument list", 1, 2, func() ErrorHandler {
		args = append(args, p.expr())
		return nil
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	p.list("struct type", 3, 4, func() ErrorHandler {
		fields = append(fields, p.expr())
		return RETURN
	})
	return fields
}

func (p *parser) interfaceType() []*Node {
	var methods []*Node
	p.list("interface type", 3, 4, func() ErrorHandler {
		methods = append(methods, p.expr())
		return nil
	})
	return methods
}
`
	source := s281ClosureParamPrelude + returningList + callers + s281ClosureParamEntry
	if ok, _ := s281ProveClosureParam(t, strings.Replace(source, "RETURN", "p.errh", 1)); ok {
		t.Fatal("a closure parameter returning the callback into a retained result was admitted")
	}
	if ok, reason := s281ProveClosureParam(t, strings.Replace(source, "RETURN", "nil", 1)); !ok {
		t.Fatalf("closure parameters returning nothing were refused: %s", reason)
	}
}

// s281ClosureParamNestedSource reaches the retaining closure only through
// another closure: interfaceType is called from embeddedElem, embeddedElem only
// from the closure structType binds to list's func-typed parameter. So the
// third closure's body runs only inside the body summary of the second one --
// a summary shortcut keyed by function name rather than by obligation would
// never walk it, and would admit the retention.
const s281ClosureParamNestedSource = s281ClosureParamPrelude + s281ClosureParamList + `
var stored ErrorHandler

func (p *parser) argList() []*Node {
	var args []*Node
	p.list("argument list", 1, 2, func() bool {
		args = append(args, p.expr())
		return false
	})
	return args
}

func (p *parser) structType() []*Node {
	var fields []*Node
	p.list("struct type", 3, 4, func() bool {
		fields = append(fields, p.embeddedElem())
		return false
	})
	return fields
}

func (p *parser) embeddedElem() *Node {
	if p.tok == 7 {
		return p.interfaceType()
	}
	return &Node{kind: p.tok}
}

func (p *parser) interfaceType() *Node {
	n := &Node{kind: p.tok}
	p.list("interface type", 3, 4, func() bool {
		RETAIN
		n.list = append(n.list, p.expr())
		return false
	})
	return n
}

func (p *parser) expr() *Node {
	n := &Node{kind: p.tok}
	if p.tok == 5 {
		n.list = p.argList()
	} else if p.tok == 6 {
		n.list = p.structType()
	}
	return n
}

func Parse(errh ErrorHandler) []*Node {
	p := &parser{errh: errh, tok: 5}
	return p.argList()
}
`

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestS281CallbackProofClosureParameterNestedRetention(t *testing.T) {
	retaining := strings.Replace(s281ClosureParamNestedSource, "RETAIN", "stored = p.errh", 1)
	if ok, _ := s281ProveClosureParam(t, retaining); ok {
		t.Fatal("a closure parameter reached only through another closure retained the callback and was admitted")
	}
	clean := strings.Replace(s281ClosureParamNestedSource, "RETAIN", "_ = p.base", 1)
	if ok, reason := s281ProveClosureParam(t, clean); !ok {
		t.Fatalf("the same nesting without any retention was refused: %s", reason)
	}
}
