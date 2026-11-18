// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.
package syntax

// bashppParenWords collects a balanced parenthesized fragment without deciding
// its language. Its caller owns the typed prefix or a rollback transaction.
func (p *Parser) bashppParenWords() ([]*Word, bool) {
	var words []*Word
	depth := 0
	for {
		switch p.tok {
		case leftParen, rightParen:
			spelling := "("
			if p.tok == rightParen {
				spelling = ")"
				depth--
			} else {
				depth++
			}
			words = append(words, p.wordOne(&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, 1), Value: spelling}))
			p.next()
			if depth == 0 {
				return words, true
			}
		case _Newl:
			p.next()
		default:
			var w *Word
			if op := bashppScalarOpTok(p.tok); op != "" {
				w = p.wordOne(&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, len(op)), Value: op})
				p.next()
			} else {
				w = p.bashppScalarOperand()
			}
			if w == nil {
				return nil, false
			}
			words = append(words, w)
		}
	}
}

// bashppSignatureTypeWord keeps concrete callable types intact while the outer
// parameter-list parser retains ownership of its own closing parenthesis.
func (p *Parser) bashppSignatureTypeWord(first *Word) *Word {
	parts, ok := p.bashppParenWords()
	if !ok {
		return nil
	}
	words := append([]*Word{first}, parts...)
	if p.tok == leftParen {
		parts, ok = p.bashppParenWords()
		if !ok {
			return nil
		}
		words = append(words, parts...)
	} else if p.tok == _LitWord && p.val != "}" && p.val != "{" && p.val != "," && p.val != "=" {
		words = append(words, p.wordOne(p.lit(p.pos, p.val)))
		p.next()
	}
	return bashppJoinWords(words)
}
