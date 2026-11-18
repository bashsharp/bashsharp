// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

// bashppConstGroup parses the Class-R `const (` production. Since stock bash
// rejects that complete prefix, malformed bodies are Bash++ diagnostics rather
// than shell fallback. The parser still consumes incrementally and never scans
// beyond the closing parenthesis.
func (p *Parser) bashppConstGroup(ce *CallExpr) Command {
	if ce == nil || len(ce.Assigns) != 0 || len(ce.Args) != 1 || p.tok != leftParen {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	if kw == nil || kw.Value != "const" {
		return nil
	}
	lparen := p.pos
	p.next()
	for p.tok == _Newl || p.tok == semicolon {
		p.next()
	}
	g := &BashPPConstGroup{Kw: kw, Lparen: lparen}
	for p.tok != rightParen && p.tok != _EOF {
		comments := p.accComs
		p.accComs = nil
		nameWord := p.getWord()
		name := bashppBareLit(nameWord)
		if name == nil || !bashppIsIdent(name.Value) {
			p.curErr("bash++ const specification requires one identifier")
			return g
		}
		var words []*Word
		for p.tok != _Newl && p.tok != semicolon && p.tok != rightParen && p.tok != _EOF {
			if op := bashppScalarOpTok(p.tok); op != "" {
				words = append(words, p.wordOne(&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, len(op)), Value: op}))
				p.next()
				continue
			}
			word := p.getWord()
			if word == nil {
				p.curErr("malformed bash++ const specification")
				return g
			}
			words = append(words, word)
		}
		spec := &BashPPConstSpec{Comments: comments, Name: name, Iota: uint32(len(g.Specs))}
		if len(words) > 0 {
			eq := -1
			for i, word := range words {
				if word.Lit() == "=" {
					if eq >= 0 {
						eq = -2
						break
					}
					eq = i
				}
			}
			if eq < 0 || eq+1 >= len(words) || eq > 1 {
				p.posErr(name.Pos(), "malformed bash++ const specification for %s", name.Value)
				return g
			}
			if eq == 1 {
				typ := bashppTypeLit(words[0])
				typExpr := bashppTypeExpr(words[0])
				if typ == nil || typExpr == nil {
					p.posErr(words[0].Pos(), "invalid bash++ const type")
					return g
				}
				spec.DeclType, spec.DeclTypeExpr = typ, typExpr
			}
			spec.Eq = words[eq].Pos()
			spec.Init = words[eq+1:]
			spec.InitExpr = bashppScalarExpr(bashppJoinWords(spec.Init))
			if spec.InitExpr == nil {
				p.posErr(spec.Init[0].Pos(), "bash++ const initializer must be a scalar constant expression")
				return g
			}
		} else if len(g.Specs) == 0 {
			p.posErr(name.Pos(), "first bash++ const specification requires an initializer")
			return g
		}
		g.Specs = append(g.Specs, spec)
		if p.tok == _Newl || p.tok == semicolon {
			p.next()
			for p.tok == _Newl || p.tok == semicolon {
				p.next()
			}
		} else if p.tok != rightParen {
			p.curErr("bash++ const specifications must be separated by a newline or semicolon")
			return g
		}
	}
	if p.tok != rightParen {
		p.posErr(lparen, "unclosed bash++ const declaration")
		return g
	}
	g.Last, p.accComs = p.accComs, nil
	g.Rparen = p.pos
	p.next()
	if !bashppCallTerminator(p.tok) {
		p.curErr("bash++ const declaration must end after closing parenthesis")
		return g
	}
	return g
}
