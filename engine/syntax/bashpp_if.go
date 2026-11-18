// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

// bashppIf transactionally recognizes a complete Go brace-form if. The
// transaction is intentionally unbounded: `if COND { ... }` and a legal shell
// if are indistinguishable until the matching brace is followed by something
// other than `then`. Any shell shape restores parser state and unread input in
// full before Parser.ifClause sees it.
func (p *Parser) bashppIf(stmt *Stmt) bool {
	txn := p.beginBashPPTxn()
	node, structural, errPos, errText := p.bashppIfClause()
	if !structural {
		txn.rollback(p)
		return false
	}

	// A shell condition may put `then` after either a semicolon or any number
	// of newlines following the matching brace. Consume that separator only
	// while the outer transaction can still restore it byte for byte.
	switch p.tok {
	case semicolon:
		checkpoint := txn.checkpoint(p)
		p.next()
		p.got(_Newl)
		if p.tok == _LitWord && p.val == "then" {
			txn.rollback(p)
			return false
		}
		txn.rewind(p, checkpoint)
	case _Newl:
		checkpoint := txn.checkpoint(p)
		p.got(_Newl)
		if p.tok == _LitWord && p.val == "then" {
			txn.rollback(p)
			return false
		}
		txn.rewind(p, checkpoint)
	case _EOF:
	default:
		if !(p.tok == _LitWord && p.val == "}") {
			txn.rollback(p)
			return false
		}
	}

	txn.commit(p)
	stmt.Cmd = node
	if errText != "" {
		p.posErr(errPos, "%s", errText)
	}
	return true
}

// bashppIfClause parses one `if` plus its optional else chain. It delays
// expression diagnostics until the complete brace form has been seen, because
// before that point the same tokens may still belong to a classic shell if.
func (p *Parser) bashppIfClause() (*BashPPIf, bool, Pos, string) {
	if !(p.tok == _LitWord && p.val == "if") {
		return nil, false, Pos{}, ""
	}
	node := &BashPPIf{Site: StartGoIf, If: p.pos}
	p.next()

	var before, after []*Word
	var semi Pos
	for {
		if p.tok == _LitWord && (p.val == "{" || p.val == "{}") {
			break
		}
		if p.tok == _EOF || p.tok == _Newl {
			return nil, false, Pos{}, ""
		}
		if p.tok == semicolon {
			if semi.IsValid() || len(before) == 0 {
				return nil, false, Pos{}, ""
			}
			semi = p.pos
			p.next()
			continue
		}
		word := p.bashppIfHeaderWord()
		if word == nil {
			return nil, false, Pos{}, ""
		}
		if semi.IsValid() {
			after = append(after, word)
		} else {
			before = append(before, word)
		}
	}

	condWords := before
	var initInvalid bool
	if semi.IsValid() {
		node.Semicolon = semi
		node.Init = bashppShortDecl(&CallExpr{Args: before}, nil, true)
		initInvalid = node.Init == nil || node.Init.Expr == nil
		condWords = after
	}
	if len(condWords) > 0 {
		node.Cond = bashppScalarExpr(bashppJoinWords(condWords))
	}

	var body Stmt
	p.bashppBlock(&body)
	node.Then, _ = body.Cmd.(*Block)
	if node.Then == nil || !node.Then.Rbrace.IsValid() || p.err != nil {
		return nil, false, Pos{}, ""
	}

	var problemPos Pos
	var problem string
	if p.tok == _LitWord && p.val == "else" && p.pos.Line() == node.Then.Rbrace.Line() {
		node.ElsePos = p.pos
		p.next()
		switch {
		case p.tok == _LitWord && p.val == "if":
			child, ok, childPos, childProblem := p.bashppIfClause()
			if !ok {
				return nil, false, Pos{}, ""
			}
			node.Else = child
			problemPos, problem = childPos, childProblem
		case p.tok == _LitWord && (p.val == "{" || p.val == "{}"):
			var els Stmt
			p.bashppBlock(&els)
			node.Else, _ = els.Cmd.(*Block)
			if node.Else == nil || p.err != nil {
				return nil, false, Pos{}, ""
			}
		default:
			return nil, false, Pos{}, ""
		}
	}

	if problem == "" && initInvalid {
		problemPos, problem = node.If, "bash++ if init must be a short declaration"
	} else if problem == "" && node.Cond == nil {
		problemPos = node.If
		if len(condWords) > 0 {
			problemPos = condWords[0].Pos()
		}
		problem = "bash++ if condition must be a scalar expression"
	}
	return node, true, problemPos, problem
}

func (p *Parser) bashppIfHeaderWord() *Word {
	if op := bashppScalarOpTok(p.tok); op != "" {
		word := p.wordOne(&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, len(op)), Value: op})
		p.next()
		return word
	}
	word := p.bashppScalarOperand()
	head := bashppBareLit(word)
	if head == nil || (head.Value != "len" && head.Value != "cap") || p.tok != leftParen {
		return word
	}
	// A call is collected only inside the enclosing transactional brace-if
	// recognizer. A shell near miss still rewinds all tokens and source positions.
	parts, ok := p.bashppParenWords()
	if !ok {
		return nil
	}
	return bashppJoinWords(append([]*Word{word}, parts...))
}
