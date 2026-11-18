// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import "strings"

// bashppSelect recognizes the Class-R brace form.  It deliberately lives in
// syntax: this commits a Go region for parsing only; it does not imply any
// scheduling or channel runtime behaviour.
//
// The whole body is read inside a transaction because `select` is ALSO a shell
// keyword — `select x in a b; do …; done` — and a script inside a Bash++ func
// body is still entitled to it. Every shape the Go grammar does not spell is
// rewound byte for byte and handed to [Parser.selectClause], so the shell form
// is decided by what it is rather than by which recognizer ran first.
func (p *Parser) bashppSelect(stmt *Stmt) bool {
	txn := p.beginBashPPTxn()
	rootControl := len(p.bashppControls) == 0
	p.bashppControls = append(p.bashppControls, bashppControlSelect)
	sel := &BashPPSelect{Select: p.pos}
	p.next()
	p.got(_Newl)
	if p.tok == _LitWord && p.val == "{}" {
		sel.Lbrace = p.pos
		sel.Rbrace = posAddCol(p.pos, 1)
		p.next()
		txn.commit(p)
		stmt.Cmd = sel
		p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
		if rootControl {
			p.bashppValidateBranches(sel)
		}
		return true
	}
	if !(p.tok == _LitWord && p.val == "{") {
		txn.rollback(p)
		return false
	}
	// An empty select is useful on its own and, unlike the shell `select`
	// builtin, is unambiguously the Go brace production.
	sel.Lbrace = p.pos
	p.next()
	for {
		p.got(_Newl)
		if p.err != nil || p.tok == _EOF {
			// A `select x in …` whose word list happened to open with `{`
			// lands here. Rewinding is the only correct answer: the shell
			// form must not inherit a diagnostic phrased in terms of Go.
			txn.rollback(p)
			return false
		}
		if p.tok == _LitWord && p.val == "}" {
			sel.Rbrace = p.pos
			p.next()
			break
		}
		arm := p.bashppSelectArm()
		if arm == nil || p.err != nil {
			txn.rollback(p)
			return false
		}
		sel.Cases = append(sel.Cases, arm)
	}
	txn.commit(p)
	stmt.Cmd = sel
	p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
	if rootControl {
		p.bashppValidateBranches(sel)
	}
	return true
}

// bashppSelectArm parses one `case <comm>:` or `default:` arm and its
// statements. It returns nil for anything else, which rewinds the whole select.
func (p *Parser) bashppSelectArm() *BashPPSelectCase {
	arm := &BashPPSelectCase{Case: p.pos}
	arm.Comments, p.accComs = p.accComs, nil
	switch {
	case p.tok == _LitWord && p.val == "default:":
		arm.Default = true
		arm.Colon = posAddCol(p.pos, len("default"))
		p.next()
	case p.tok == _LitWord && p.val == "case":
		p.next()
		comm, colon := p.bashppCommClause()
		if comm == nil {
			return nil
		}
		arm.Comm, arm.Colon = comm, colon
	default:
		return nil
	}
	// A comment lexed while advancing beyond the colon belongs to the arm
	// header only when it shares the colon's source line. Later comments stay
	// accumulated for the first body statement.
	for len(p.accComs) > 0 && p.accComs[0].Pos().Line() == arm.Colon.Line() {
		arm.Comments = append(arm.Comments, p.accComs[0])
		p.accComs = p.accComs[1:]
	}
	// The same stop set the enum switch uses, so an arm body ends where the
	// next arm or the closing brace begins.
	arm.Stmts, arm.Last = p.stmtList("case", "default:", "}")
	return arm
}

// bashppCommClause reads a select case's communication clause up to and
// including its terminating colon, and returns the typed node for it.
//
// It reads at the TOKEN level rather than reusing the statement parser because
// the colon is glued to the last word — `case <-done:` ends in the single
// literal `-done:` — so there is no statement boundary for the ordinary
// machinery to stop at. What it does NOT do is decide the shape: the words it
// collects are handed to [bashppClassifyComm], the same classifier the
// statement-level forms use, so `case v := <-ch:` and a bare `v := <-ch` are
// one grammar rather than two that must be kept in agreement by hand.
func (p *Parser) bashppCommClause() (Command, Pos) {
	var pre, post []*Word
	var arrow, colon Pos
	for {
		if p.tok == rdrIn {
			if arrow.IsValid() {
				return nil, Pos{}
			}
			arrow = p.pos
			p.next()
			continue
		}
		if p.tok == _Newl || p.tok == _EOF || p.tok == semicolon {
			return nil, Pos{}
		}
		w := p.getWord()
		if w == nil {
			return nil, Pos{}
		}
		trimmed, colonPos, done := bashppTrimColon(w)
		if !done {
			if arrow.IsValid() {
				post = append(post, w)
			} else {
				pre = append(pre, w)
			}
			continue
		}
		colon = colonPos
		if trimmed != nil {
			if arrow.IsValid() {
				post = append(post, trimmed)
			} else {
				pre = append(pre, trimmed)
			}
		}
		break
	}
	if !arrow.IsValid() {
		return nil, Pos{}
	}
	cmd := bashppClassifyComm(pre, arrow, post)
	if cmd == nil {
		return nil, Pos{}
	}
	return cmd, colon
}

// bashppTrimColon splits a terminating `:` off a word, mirroring
// [bashppTrimComma]. The trimmed word is nil when the colon was the whole of
// it, which is the `case <-ch :` spelling.
func bashppTrimColon(w *Word) (*Word, Pos, bool) {
	if w == nil || len(w.Parts) == 0 {
		return w, Pos{}, false
	}
	last, ok := w.Parts[len(w.Parts)-1].(*Lit)
	if !ok || len(last.Value) == 0 || last.Value[len(last.Value)-1] != ':' {
		return w, Pos{}, false
	}
	colon := posAddCol(last.ValueEnd, -1)
	copyWord := *w
	copyWord.Parts = append([]WordPart(nil), w.Parts...)
	copyLit := *last
	copyLit.Value = last.Value[:len(last.Value)-1]
	copyLit.ValueEnd = colon
	if copyLit.Value == "" {
		copyWord.Parts = copyWord.Parts[:len(copyWord.Parts)-1]
	} else {
		copyWord.Parts[len(copyWord.Parts)-1] = &copyLit
	}
	if len(copyWord.Parts) == 0 {
		return nil, colon, true
	}
	return &copyWord, colon, true
}

// bashppRange recognizes range-over-channel, `for v := range ch { … }`.
//
// The shell `for` is Class E and heavily used, so the whole clause is read
// inside a transaction and every shape that is not exactly the Go one is
// rewound to [Parser.forClause]. The commit point is the `range` keyword: bash
// has no `for name := range` production — `for foo` must be followed by `in`,
// `do`, `;` or a newline — so by the time the third word is read the shape is
// already a bash syntax error and claiming it takes nothing away.
func (p *Parser) bashppRange(stmt *Stmt) bool {
	txn := p.beginBashPPTxn()
	rootControl := len(p.bashppControls) == 0
	p.bashppControls = append(p.bashppControls, bashppControlRange)
	rng := &BashPPRange{For: p.pos}
	p.next()
	if !(p.tok == _LitWord && p.val == "range") {
		var lhs []*Word
		for p.tok == _LitWord && p.val != ":=" {
			lhs = append(lhs, p.getWord())
		}
		op := bashppBareLit(p.getWord())
		if op == nil || op.Value != ":=" {
			txn.rollback(p)
			return false
		}
		names, namesOK := bashppShortLHS(lhs)
		if p.tok == _LitWord && p.val == "range" && (!namesOK || len(names) > 2) {
			rng.Range = p.pos
			problem := rng.For
			if len(lhs) > 2 {
				problem = lhs[2].Pos()
			}
			p.posErr(problem, "bash++ range permits at most two iteration variables")
			txn.commit(p)
			stmt.Cmd = rng
			p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
			return true
		}
		if !namesOK {
			txn.rollback(p)
			return false
		}
		rng.Names = names
		rng.Define = op.Pos()
	}
	if !(p.tok == _LitWord && p.val == "range") {
		txn.rollback(p)
		return false
	}
	rng.Range = p.pos
	p.next()
	firstOperand := p.getWord()
	if firstOperand == nil {
		txn.rollback(p)
		return false
	}
	operandWords := []*Word{firstOperand}
	for p.tok == _LitWord && p.val != "{" && p.val != "{}" {
		operandWords = append(operandWords, p.getWord())
	}
	ch := bashppJoinWords(operandWords)
	rng.Chan = ch
	if call := p.bashppRangeLinesCall(ch); call != nil {
		// `range x.Lines()` — the sole chained call a range reads; every
		// other `(` after the operand keeps rewinding to the shell.
		rng.Call = call
		rng.Chan = &Word{Parts: []WordPart{&Lit{ValuePos: ch.Pos(), ValueEnd: posAddCol(call.Rparen, 1), Value: bashppWordText(ch) + "()"}}}
	} else if expr := bashppIndexExpr(ch); expr != nil {
		rng.Expr = expr
	} else if expr := bashppScalarExpr(ch); expr != nil {
		rng.Expr = expr
	} else if !bashppChanOperand(ch) {
		txn.rollback(p)
		return false
	}
	p.got(_Newl)
	if !(p.tok == _LitWord && (p.val == "{" || p.val == "{}")) {
		txn.rollback(p)
		return false
	}
	var body Stmt
	p.bashppBlock(&body)
	block, _ := body.Cmd.(*Block)
	if block == nil || p.err != nil {
		txn.rollback(p)
		return false
	}
	rng.Body = block
	txn.commit(p)
	stmt.Cmd = rng
	p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
	if rootControl {
		p.bashppValidateBranches(rng)
	}
	return true
}

// bashppRangeLinesCall reads the `()` of `range x.Lines()` when the operand
// is exactly a two-part selector ending in Lines and the parenthesis follows
// unspaced. It is deliberately the narrowest seam that admits the process
// line-iteration spellings (`r.Lines()` over a completed run result,
// `p.Lines()` over a live start handle): no arguments, no deeper chain. Any
// other shape leaves the parser untouched so the caller's rewind decides.
func (p *Parser) bashppRangeLinesCall(operand *Word) *BashPPCall {
	lit := bashppBareLit(operand)
	if lit == nil || p.tok != leftParen || p.spaced {
		return nil
	}
	base, method, found := strings.Cut(lit.Value, ".")
	if !found || method != "Lines" || !BashPPValidIdent(base) {
		return nil
	}
	lparen := p.pos
	p.next()
	if p.tok != rightParen {
		return nil
	}
	rparen := p.pos
	p.next()
	return &BashPPCall{Fun: bashppSelectorLits(lit), Lparen: lparen, Rparen: rparen}
}
