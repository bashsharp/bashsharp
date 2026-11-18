// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import "strings"

// bashppFor transactionally recognizes the non-range Go for forms. The range
// recognizer runs first, and every shell/classic shape is restored in full for
// Parser.forClause. Bash's braced loop carries a separator before `{`, so it
// remains distinct from a condition-only Go loop.
func (p *Parser) bashppFor(stmt *Stmt) bool {
	txn := p.beginBashPPTxn()
	rootControl := len(p.bashppControls) == 0
	p.bashppControls = append(p.bashppControls, bashppControlFor)
	node := &BashPPFor{For: p.pos}
	p.next()

	parts := make([][]*Word, 1, 3)
	var semis []Pos
	for !(p.tok == _LitWord && (p.val == "{" || p.val == "{}")) {
		if p.tok == _EOF || p.tok == _Newl || len(parts) > 3 {
			txn.rollback(p)
			return false
		}
		if p.tok == semicolon {
			semis = append(semis, p.pos)
			parts = append(parts, nil)
			p.next()
			continue
		}
		word := p.bashppIfHeaderWord()
		if word == nil {
			txn.rollback(p)
			return false
		}
		parts[len(parts)-1] = append(parts[len(parts)-1], word)
	}

	var problemPos Pos
	var problem string
	switch len(semis) {
	case 0:
		if len(parts[0]) > 0 {
			node.Cond = bashppScalarExpr(bashppJoinWords(parts[0]))
			if node.Cond == nil {
				problemPos, problem = parts[0][0].Pos(), "bash++ for condition must be a scalar expression"
			}
		}
	case 2:
		node.FirstSemi, node.SecondSemi = semis[0], semis[1]
		if len(parts[0]) > 0 {
			node.Init = bashppForSimple(parts[0], true)
			if node.Init == nil {
				problemPos, problem = parts[0][0].Pos(), "bash++ for init must be a scalar short declaration, assignment, or inc-dec statement"
			}
		}
		if len(parts[1]) > 0 {
			node.Cond = bashppScalarExpr(bashppJoinWords(parts[1]))
			if problem == "" && node.Cond == nil {
				problemPos, problem = parts[1][0].Pos(), "bash++ for condition must be a scalar expression"
			}
		}
		if len(parts[2]) > 0 {
			node.Post = bashppForSimple(parts[2], false)
			if problem == "" && node.Post == nil {
				problemPos, problem = parts[2][0].Pos(), "bash++ for post must be a scalar assignment or inc-dec statement"
			}
		}
	default:
		txn.rollback(p)
		return false
	}

	var body Stmt
	p.bashppBlock(&body)
	node.Body, _ = body.Cmd.(*Block)
	if node.Body == nil || !node.Body.Rbrace.IsValid() || p.err != nil {
		txn.rollback(p)
		return false
	}
	txn.commit(p)
	stmt.Cmd = node
	p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
	if problem != "" {
		p.posErr(problemPos, "%s", problem)
	} else if rootControl {
		p.bashppValidateBranches(node)
	}
	return true
}

func bashppForSimple(words []*Word, allowShort bool) Command {
	ce := &CallExpr{Args: words}
	if allowShort {
		if decl := bashppShortDecl(ce, nil, true); decl != nil && decl.Expr != nil && len(decl.Lhs) == 1 {
			return decl
		}
	}
	if assign := bashppForScalarAssign(words); assign != nil {
		return assign
	}
	if stmt := bashppForIncDec(words); stmt != nil {
		return stmt
	}
	return nil
}

func bashppForScalarAssign(words []*Word) *BashPPForAssign {
	if len(words) < 3 {
		return nil
	}
	name := bashppBareLit(words[0])
	eq := bashppBareLit(words[1])
	if name == nil || !bashppIsIdent(name.Value) || eq == nil || eq.Value != "=" {
		return nil
	}
	expr := bashppScalarExpr(bashppJoinWords(words[2:]))
	if expr == nil {
		return nil
	}
	return &BashPPForAssign{Name: name, Eq: eq.Pos(), Expr: expr}
}

func bashppForIncDec(words []*Word) *BashPPIncDec {
	if len(words) == 1 {
		lit := bashppBareLit(words[0])
		if lit == nil {
			return nil
		}
		for _, op := range []string{"++", "--"} {
			if strings.HasSuffix(lit.Value, op) {
				name := strings.TrimSuffix(lit.Value, op)
				if !bashppIsIdent(name) {
					return nil
				}
				opPos := posAddCol(lit.End(), -len(op))
				return &BashPPIncDec{
					Name: &Lit{ValuePos: lit.Pos(), ValueEnd: opPos, Value: name},
					Op:   &Lit{ValuePos: opPos, ValueEnd: lit.End(), Value: op},
				}
			}
		}
	}
	if len(words) == 2 {
		name, op := bashppBareLit(words[0]), bashppBareLit(words[1])
		if name != nil && bashppIsIdent(name.Value) && op != nil && (op.Value == "++" || op.Value == "--") {
			return &BashPPIncDec{Name: name, Op: op}
		}
	}
	return nil
}
