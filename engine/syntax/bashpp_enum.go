// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import "strings"

// bashppSwitch transactionally recognizes a brace-form expression switch.
// A switch-like shell command without the complete form is restored intact.
func (p *Parser) bashppSwitch(stmt *Stmt) bool {
	txn := p.beginBashPPTxn()
	rootControl := len(p.bashppControls) == 0
	p.bashppControls = append(p.bashppControls, bashppControlSwitch)
	sw := &BashPPSwitch{Switch: p.pos}
	p.next()

	parts := make([][]*Word, 1, 2)
	var typeSwitch *BashPPShortDecl
	for !(p.tok == _LitWord && (p.val == "{" || p.val == "{}")) {
		if p.tok == _EOF || p.tok == _Newl || len(parts) > 2 {
			txn.rollback(p)
			return false
		}
		if p.tok == leftParen && len(parts) == 1 && len(parts[0]) >= 3 {
			last := bashppBareLit(parts[0][len(parts[0])-1])
			if last == nil || !strings.HasSuffix(last.Value, ".") {
				txn.rollback(p)
				return false
			}
			p.next()
			typeWord := p.getWord()
			typeLit := bashppBareLit(typeWord)
			if typeLit == nil || typeLit.Value != "type" || p.tok != rightParen {
				txn.rollback(p)
				return false
			}
			rparen := p.pos
			p.next()
			op := len(parts[0]) - 2
			opLit := bashppBareLit(parts[0][op])
			lhs, ok := bashppShortLHS(parts[0][:op])
			if opLit == nil || opLit.Value != ":=" || !ok || len(lhs) != 1 {
				txn.rollback(p)
				return false
			}
			rootName := strings.TrimSuffix(last.Value, ".")
			root := &Lit{ValuePos: last.Pos(), ValueEnd: posAddCol(last.End(), -1), Value: rootName}
			typeSwitch = &BashPPShortDecl{Lhs: lhs, OpPos: opLit.Pos(), GoRegion: true, Class: ClassR,
				Expr: &BashPPTypeAssertExpr{X: &BashPPIdent{Name: root}, Dot: root.End(), Lparen: posAddCol(last.End(), 0), TypeToken: typeLit, Rparen: rparen}}
			continue
		}
		if p.tok == semicolon {
			if len(parts) == 2 {
				txn.rollback(p)
				return false
			}
			sw.Semicolon = p.pos
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
	tagWords := parts[0]
	if len(parts) == 2 {
		if len(parts[0]) > 0 {
			sw.Init = bashppForSimple(parts[0], true)
		}
		if sw.Init == nil {
			problemPos, problem = sw.Switch, "bash++ switch init must be a scalar short declaration, assignment, or inc-dec statement"
		}
		tagWords = parts[1]
	} else if typeSwitch != nil {
		sw.Init = typeSwitch
		sw.TypeSwitch = true
		tagWords = nil
	}
	if len(tagWords) > 0 {
		sw.Tag = bashppScalarExpr(bashppJoinWords(tagWords))
		if sw.Tag == nil && problem == "" {
			problemPos, problem = tagWords[0].Pos(), "bash++ switch tag must be a scalar expression"
		}
	}
	sw.Lbrace = p.pos
	if p.val == "{}" {
		sw.Rbrace = posAddCol(p.pos, 1)
		p.next()
		txn.commit(p)
		stmt.Cmd = sw
		p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
		if problem != "" {
			p.posErr(problemPos, "%s", problem)
		} else if rootControl {
			p.bashppValidateBranches(sw)
		}
		return true
	}
	p.next()

	hasDefault := false
	for p.err == nil {
		p.got(_Newl)
		if p.tok == _EOF {
			txn.rollback(p)
			return false
		}
		if p.tok == _LitWord && p.val == "}" {
			sw.Rbrace = p.pos
			p.next()
			break
		}
		arm := &BashPPSwitchArm{Case: p.pos}
		if p.tok == _LitWord && p.val == "default:" {
			arm.Colon = posAddCol(p.pos, len("default"))
			if hasDefault && problem == "" {
				problemPos, problem = arm.Case, "bash++ switch has multiple default clauses"
			}
			hasDefault = true
			p.next()
		} else {
			if !(p.tok == _LitWord && p.val == "case") {
				txn.rollback(p)
				return false
			}
			p.next()
			var words []*Word
			for {
				if p.tok == _EOF || p.tok == _Newl {
					txn.rollback(p)
					return false
				}
				word := p.bashppIfHeaderWord()
				if word == nil {
					txn.rollback(p)
					return false
				}
				words = append(words, word)
				var ok bool
				words[len(words)-1], arm.Colon, ok = bashppTrimSwitchColon(words[len(words)-1])
				if ok {
					break
				}
			}
			arm.Exprs, arm.Commas = bashppSwitchExprs(bashppJoinWords(words))
			if len(arm.Exprs) == 0 && problem == "" {
				problemPos, problem = arm.Case, "bash++ switch case must contain scalar expressions"
			}
		}
		arm.Stmts, arm.Last = p.stmtList("case", "default:", "}")
		sw.Arms = append(sw.Arms, arm)
	}
	if !sw.Rbrace.IsValid() {
		txn.rollback(p)
		return false
	}
	txn.commit(p)
	stmt.Cmd = sw
	p.bashppControls = p.bashppControls[:len(p.bashppControls)-1]
	if problem != "" {
		p.posErr(problemPos, "%s", problem)
	} else if rootControl {
		p.bashppValidateBranches(sw)
	}
	return true
}

func bashppTrimSwitchColon(word *Word) (*Word, Pos, bool) {
	if word == nil || len(word.Parts) == 0 {
		return word, Pos{}, false
	}
	last, ok := word.Parts[len(word.Parts)-1].(*Lit)
	if !ok || len(last.Value) == 0 || last.Value[len(last.Value)-1] != ':' {
		return word, Pos{}, false
	}
	colon := posAddCol(last.End(), -1)
	copyWord := *word
	copyWord.Parts = append([]WordPart(nil), word.Parts...)
	if len(last.Value) == 1 {
		copyWord.Parts = copyWord.Parts[:len(copyWord.Parts)-1]
	} else {
		copyLit := *last
		copyLit.Value = copyLit.Value[:len(copyLit.Value)-1]
		copyLit.ValueEnd = colon
		copyWord.Parts[len(copyWord.Parts)-1] = &copyLit
	}
	return &copyWord, colon, true
}

func bashppSwitchExprs(word *Word) ([]BashPPExpr, []*Lit) {
	if word == nil {
		return nil, nil
	}
	var groups [][]WordPart
	var current []WordPart
	var commas []*Lit
	depth := 0
	flush := func() {
		for len(current) > 0 {
			lit, ok := current[0].(*Lit)
			if !ok || lit.Value != " " {
				break
			}
			current = current[1:]
		}
		for len(current) > 0 {
			lit, ok := current[len(current)-1].(*Lit)
			if !ok || lit.Value != " " {
				break
			}
			current = current[:len(current)-1]
		}
		groups = append(groups, current)
		current = nil
	}
	for _, part := range word.Parts {
		lit, ok := part.(*Lit)
		if !ok {
			current = append(current, part)
			continue
		}
		start := 0
		for i := 0; i < len(lit.Value); i++ {
			switch lit.Value[i] {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth != 0 {
					continue
				}
				if i > start {
					current = append(current, &Lit{ValuePos: posAddCol(lit.Pos(), start), ValueEnd: posAddCol(lit.Pos(), i), Value: lit.Value[start:i]})
				}
				commaPos := posAddCol(lit.Pos(), i)
				commas = append(commas, &Lit{ValuePos: commaPos, ValueEnd: posAddCol(commaPos, 1), Value: ","})
				flush()
				start = i + 1
			}
		}
		if start < len(lit.Value) {
			current = append(current, &Lit{ValuePos: posAddCol(lit.Pos(), start), ValueEnd: lit.End(), Value: lit.Value[start:]})
		}
	}
	flush()
	exprs := make([]BashPPExpr, 0, len(groups))
	for _, parts := range groups {
		expr := bashppScalarExpr(&Word{Parts: parts})
		if expr == nil {
			return nil, commas
		}
		exprs = append(exprs, expr)
	}
	return exprs, commas
}
