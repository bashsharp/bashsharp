// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

// Mixed programs do not pass through go/types. Validate their labels against
// the existing block tree before the runner uses its block-unwinding goto.
func (p *Parser) bashppValidateLabels(root Node) {
	type site struct {
		pos  Pos
		path []Node
	}
	labels := make(map[string]site)
	type jump struct {
		name string
		site
	}
	var jumps []jump
	var declarations []site
	var path, stack []Node
	Walk(root, func(n Node) bool {
		if p.err != nil {
			return false
		}
		if n == nil {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(path) > 0 && path[len(path)-1] == last {
				path = path[:len(path)-1]
			}
			return true
		}
		if n != root {
			switch x := n.(type) {
			case *BashPPFuncDecl:
				p.bashppValidateLabels(x.Body)
				return false
			case *BashPPFuncLit:
				p.bashppValidateLabels(x.Body)
				return false
			case *FuncDecl:
				p.bashppValidateLabels(x.Body)
				return false
			case *Subshell:
				p.bashppValidateLabels(x)
				return false
			case *CmdSubst:
				p.bashppValidateLabels(x)
				return false
			}
		}
		stack = append(stack, n)
		switch n.(type) {
		case *Block, *BashPPSelectCase, *IfClause, *WhileClause, *ForClause, *CaseItem:
			path = append(path, n)
		}
		here := site{n.Pos(), append([]Node(nil), path...)}
		switch x := n.(type) {
		case *BashPPLabeled:
			if _, exists := labels[x.Label.Value]; exists {
				p.posErr(x.Pos(), "label %s already defined", x.Label.Value)
			} else {
				labels[x.Label.Value] = here
			}
		case *BashPPGoto:
			jumps = append(jumps, jump{x.Label.Value, here})
		case *BashPPShortDecl:
			declarations = append(declarations, here)
		case *BashPPDecl:
			if x.Kw.Value == "var" {
				declarations = append(declarations, here)
			}
		}
		return true
	})
	prefix := func(a, b []Node) bool {
		if len(a) > len(b) {
			return false
		}
		for i, n := range a {
			if b[i] != n {
				return false
			}
		}
		return true
	}
	for _, j := range jumps {
		if p.err != nil {
			return
		}
		target, ok := labels[j.name]
		if !ok {
			p.posErr(j.pos, "label %s is not defined in this function", j.name)
			return
		}
		if !prefix(target.path, j.path) {
			p.posErr(j.pos, "goto %s jumps into a block", j.name)
			return
		}
		for _, d := range declarations {
			if d.pos.After(j.pos) && target.pos.After(d.pos) && prefix(d.path, target.path) {
				p.posErr(j.pos, "goto %s jumps over a variable declaration", j.name)
				return
			}
		}
	}
}
