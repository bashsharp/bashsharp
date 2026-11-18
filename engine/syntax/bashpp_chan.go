// Copyright (c) 2026, the bash++ authors.
// See LICENSE for licensing information.

package syntax

import "strings"

// This file holds the Bash++ P4 CHANNEL forms: send, receive, the short
// declaration bound from a receive, and make(chan T, n).
//
// WHY THERE IS NO LEXER CHANGE HERE, AND WHY THAT IS THE WHOLE DESIGN.
//
// `<-` is not a token in this parser and must not become one. The lexer
// already produces `<` as [rdrIn] and lets the redirect machinery take the
// filename after it, which is how `cmd < file` works; a global recognizer that
// intercepted `<` to look for an arrow is the shape that has to advance the
// lexer on every ordinary redirect too, and an earlier attempt that forgot to
// hung TestParseFiles/bash/OK/085-0 in an unbounded read. So nothing here
// touches the redirect path at all.
//
// Instead every channel form is reclassified AFTER the ordinary shell parse of
// the command has completed, from the tree it already produced:
//
//	ch <- v      CallExpr{Args: [ch, v]}   + Redirect{Op: <, Word: "-"}
//	<-ch         (no Cmd)                  + Redirect{Op: <, Word: "-ch"}
//	v := <-ch    CallExpr{Args: [v, :=]}   + Redirect{Op: <, Word: "-ch"}
//	v, ok := <-ch    CallExpr{Args: [v,, ok, :=]} + same
//
// Each carries exact source positions already, so the typed node is built with
// no re-lexing, no lookahead and — decisively — no way to affect an input the
// recognizer declines. Declining means leaving the CallExpr and its Redirect
// exactly as the shell parser built them.
//
// WHY THE FORMS ARE GATED ON AN OPEN FUNC BODY. `ch <- v` and `<-ch` are legal
// bash today: the first runs `ch` with argument `v` reading stdin from a file
// named `-`, the second reads stdin from `-ch`. Claiming them unconditionally
// would be a Class E meaning change with no published table row. Inside a
// Bash++ func body there is nothing to protect — `func f(…) { … }` is itself
// Class R, so bash never reaches the body at all — which is the same reasoning
// [Parser.bashppReturn] and [Parser.bashppSwitch] already rely on. Callers
// therefore check p.bashppFuncDepth > 0 before consulting anything here, and
// LangBash, LangPOSIX and process substitution are untouched by construction.

// bashppChanForm reclassifies a completed command plus its redirects as a
// channel operation. ce may be nil, which is the standalone `<-ch` statement:
// bash parses it as a redirect with no command at all.
//
// It returns nil for every shape it does not claim, and claiming nothing is
// the common case: a single `<` redirect whose filename does not begin with a
// dash glued to the operator cannot be an arrow and is rejected on the first
// check.
func bashppChanForm(ce *CallExpr, redirs []*Redirect) Command {
	if len(redirs) != 1 {
		return nil
	}
	rd := redirs[0]
	if rd.Op != RdrIn || rd.N != nil || rd.Hdoc != nil || rd.Word == nil {
		return nil
	}
	// The dash must be the second half of the arrow, written against the `<`.
	// `cmd < -file` keeps its two-token spelling and stays a redirect.
	if rd.Word.Pos().Offset() != rd.OpPos.Offset()+1 {
		return nil
	}
	lit := bashppBareLit(rd.Word)
	if lit == nil || !strings.HasPrefix(lit.Value, "-") {
		return nil
	}
	var pre []*Word
	if ce != nil {
		if len(ce.Assigns) != 0 {
			return nil
		}
		pre = ce.Args
	}
	// Words the shell parser collected AFTER the arrow are the operation's
	// right-hand side, not part of its left. `ch <- v` puts `ch` and `v` in
	// one argument list with the redirect between them, and only the recorded
	// positions say which is which.
	post := []*Word{rd.Word}
	for i, w := range pre {
		if w.Pos().Offset() > rd.OpPos.Offset() {
			post = append(post, pre[i:]...)
			pre = pre[:i]
			break
		}
	}
	return bashppClassifyComm(pre, rd.OpPos, post)
}

// bashppClassifyComm builds the typed node for one communication clause, given
// the words written before the arrow, the arrow's own position, and the words
// after it. It is shared by the statement-level reclassification above and by
// the `case` arms of a select, which reach the same three shapes through a
// different reader; keeping one classifier is what makes `case v := <-ch:` and
// a standalone `v := <-ch` provably the same grammar rather than two.
func bashppClassifyComm(pre []*Word, arrow Pos, post []*Word) Command {
	if len(post) == 0 {
		return nil
	}
	head := bashppBareLit(post[0])
	if head == nil || !strings.HasPrefix(head.Value, "-") {
		return nil
	}
	if head.Value == "-" {
		// `ch <- v`: the dash is the arrow's own tail and the value is the
		// next word. A send has exactly one channel and one value.
		if len(pre) != 1 || len(post) != 2 {
			return nil
		}
		if !bashppChanOperand(pre[0]) {
			return nil
		}
		return &BashPPSend{Chan: pre[0], Arrow: arrow, Value: post[1]}
	}
	// `<-ch`: the channel name is glued to the dash, so it arrives as one
	// literal and is split back out with its true columns.
	if len(post) != 1 {
		return nil
	}
	name := head.Value[1:]
	if !bashppSelector(name) {
		return nil
	}
	chanPos := posAddCol(head.ValuePos, 1)
	recv := &BashPPReceive{Arrow: arrow, Chan: &Word{Parts: []WordPart{&Lit{
		ValuePos: chanPos,
		ValueEnd: posAddCol(chanPos, len(name)),
		Value:    name,
	}}}}
	if len(pre) == 0 {
		return recv
	}
	// `v := <-ch`, `v, ok := <-ch`, `v = <-ch`, and `v, ok = <-ch`.
	// Go allows the second value only for a receive, and only as the boolean
	// that reports whether the channel was still open, so two targets is the
	// ceiling.
	opLit := bashppBareLit(pre[len(pre)-1])
	if opLit == nil {
		return nil
	}
	switch opLit.Value {
	case ":=":
		lhs, ok := bashppShortLHS(pre[:len(pre)-1])
		if !ok || len(lhs) == 0 || len(lhs) > 2 {
			return nil
		}
		return &BashPPShortDecl{Lhs: lhs, Class: ClassE, OpPos: opLit.Pos(), GoRegion: true, Recv: recv}
	case "=":
		return bashppReceiveAssign(pre[:len(pre)-1], opLit.Pos(), recv)
	default:
		return nil
	}
}

// bashppReceiveAssign builds the same assignment node used for ordinary `=`
// statements. Identifier lists retain Names for tuple assignment; a selector
// or index target retains its typed address expression for the existing
// assignment machinery.
func bashppReceiveAssign(lhs []*Word, eq Pos, recv *BashPPReceive) *BashPPAssign {
	if names, ok := bashppShortLHS(lhs); ok {
		if len(names) == 0 || len(names) > 2 {
			return nil
		}
		return &BashPPAssign{Names: names, Eq: eq, Recv: recv}
	}
	if len(lhs) != 1 {
		return nil
	}
	target := bashppAssignmentTargetExpr(lhs[0])
	if target == nil {
		return nil
	}
	return &BashPPAssign{Target: lhs[0], TargetExpr: target, Eq: eq, Recv: recv}
}

// bashppChanOperand reports whether w names a channel plainly enough to be the
// left-hand side of a send. Anything else — a quoted word, an expansion, a
// glob — is left to the shell, which keeps the fallback total.
func bashppChanOperand(w *Word) bool {
	lit := bashppBareLit(w)
	return lit != nil && bashppSelector(lit.Value)
}

// bashppMakeChanTail parses `chan T` and an optional capacity, with the parser
// sitting just past the `(` of a `make(` call. kw is the `make` literal and
// lparen its parenthesis, both already consumed by the caller.
//
// It returns nil rather than raising a diagnostic, so a `make(` the channel
// grammar does not spell rewinds to whatever the caller's transaction saved.
func (p *Parser) bashppMakeChanTail(kw *Lit, lparen Pos) *BashPPMakeChan {
	w := p.getWord()
	if lit := bashppBareLit(w); lit == nil || lit.Value != "chan" {
		return nil
	}
	mk := &BashPPMakeChan{Make: kw, Lparen: lparen}
	mk.ChanType = &BashPPChanType{Chan: w.Pos()}

	elemWord := p.getWord()
	if elemWord == nil {
		return nil
	}
	elemWord, comma := bashppTrimComma(elemWord)
	elem := bashppBareLit(elemWord)
	if elem == nil || !bashppSelector(elem.Value) {
		return nil
	}
	mk.ChanType.Elem = elem

	if comma {
		// The capacity stays an unevaluated word: a buffered channel's size is
		// frequently `$n`, and deciding it here would be arithmetic the syntax
		// layer has no business doing.
		capWord := p.getWord()
		if capWord == nil {
			return nil
		}
		capWord, trailing := bashppTrimComma(capWord)
		if trailing || !bashppCallArg(capWord) {
			return nil
		}
		mk.Capacity = capWord
	}
	if p.tok != rightParen {
		return nil
	}
	mk.Rparen = p.pos
	p.next()
	if !bashppCallTerminator(p.tok) {
		return nil
	}
	return mk
}

func bashppLeadingDash(w *Word) bool {
	if w == nil || len(w.Parts) == 0 {
		return false
	}
	lit, ok := w.Parts[0].(*Lit)
	return ok && strings.HasPrefix(lit.Value, "-")
}
