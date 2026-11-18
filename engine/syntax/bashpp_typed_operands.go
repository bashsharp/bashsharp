// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.
package syntax

import (
	"strings"
	"unicode/utf8"
)

// bashppReadScalarTail only runs after a caller establishes ownership of the
// typed prefix. It retains lexer positions and delegates bounded expression
// translation to the existing BashPPExpr converter.
func (p *Parser) bashppReadScalarTail(raw bool) ([]*Word, bool) {
	var words []*Word
	for !p.bashppScalarTailEnd() {
		if p.tok == leftParen {
			group, ok := p.bashppParenWords()
			if !ok {
				return nil, false
			}
			words = append(words, group...)
			continue
		}
		var word *Word
		if raw && p.tok == bckQuote {
			word = p.bashppRawStringWord()
		} else if op := bashppScalarOpTok(p.tok); op != "" {
			word = p.wordOne(&Lit{ValuePos: p.pos, ValueEnd: posAddCol(p.pos, len(op)), Value: op})
			p.next()
		} else {
			word = p.bashppScalarOperand()
		}
		if word == nil {
			return nil, false
		}
		words = append(words, word)
	}
	return words, len(words) > 0
}

func (p *Parser) bashppScalarReturn(ce *CallExpr) *BashPPReturn {
	txn := p.beginBashPPTxn()
	tail, ok := p.bashppReadScalarTail(false)
	if !ok {
		txn.rollback(p)
		return nil
	}
	words := append(append([]*Word(nil), ce.Args[1:]...), tail...)
	value := bashppJoinWords(words)
	expr := bashppScalarExpr(value)
	// Bare words/quoted values, tuples, calls and escaping function literals keep
	// their existing result paths. This carrier owns only scalar operator trees.
	switch expr.(type) {
	case *BashPPBinaryExpr, *BashPPUnaryExpr, *BashPPParenExpr:
	default:
		txn.rollback(p)
		return nil
	}
	txn.commit(p)
	return &BashPPReturn{Kw: bashppBareLit(ce.Args[0]), Results: []*Word{value}, Expr: expr}
}

func (p *Parser) bashppLeadingScalarOperand(ce *CallExpr) *BashPPShortDecl {
	txn := p.beginBashPPTxn()
	tail, ok := p.bashppReadScalarTail(true)
	if p.err != nil {
		txn.commit(p)
		return nil
	}
	if !ok {
		txn.rollback(p)
		return nil
	}
	args := append(append([]*Word(nil), ce.Args...), tail...)
	decl := bashppShortDecl(&CallExpr{Args: args}, nil, true)
	if decl == nil || decl.Expr == nil {
		txn.rollback(p)
		return nil
	}
	txn.commit(p)
	return decl
}

// bashppRawStringWord reads a Go raw literal without entering command-substitution
// parsing. It is never called for a shell command argument or a top-level
// Class-E declaration. Backslashes and escaped newlines are literal bytes.
func (p *Parser) bashppRawStringWord() *Word {
	start := p.pos
	var text strings.Builder
	text.WriteByte('`')
	// A nested shell backquote must not unescape bytes owned by this literal.
	saved := p.openBquotes
	p.openBquotes = 0
	defer func() { p.openBquotes = saved }()
	for ch := p.rune(); ; ch = p.rune() {
		switch ch {
		case '`':
			end := posAddCol(p.nextPos(), 1)
			text.WriteByte('`')
			p.openBquotes = saved
			p.rune()
			p.next()
			return p.wordOne(&Lit{ValuePos: start, ValueEnd: end, Value: text.String()})
		case escNewl:
			text.WriteString("\\\n")
		case utf8.RuneSelf:
			p.posErr(start, "unterminated Bash++ raw string literal")
			return nil
		default:
			text.WriteRune(ch)
		}
	}
}
