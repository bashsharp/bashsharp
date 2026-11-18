// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import "strings"

func bashppReservedWord(name string) bool {
	switch name {
	case "var", "const", "func", "import", "package", "goto":
		return true
	}
	return false
}

// Once a reserved start site is selected, an invalid continuation is a
// language error, never a command whose meaning depends on PATH.
func (p *Parser) bashppReservedFallback(ce *CallExpr) {
	if ce == nil || len(ce.Args) == 0 {
		return
	}
	head := bashppBareLit(ce.Args[0])
	if head == nil {
		var name strings.Builder
		for _, part := range ce.Args[0].Parts {
			lit, ok := part.(*Lit)
			if !ok {
				return
			}
			name.WriteString(lit.Value)
		}
		head = &Lit{ValuePos: ce.Args[0].Pos(), ValueEnd: ce.Args[0].End(), Value: name.String()}
	}
	if head.Value != "func" && bashppReservedWord(head.Value) {
		if head.Value == "package" {
			p.posErr(head.Pos(), "package must begin a Go compilation unit; use the Go source route")
		} else {
			p.posErr(head.Pos(), "invalid %s statement; use command %s to invoke a shell command", head.Value, head.Value)
		}
		return
	}
	if head.Value == "command" {
		return
	}
	for i, w := range ce.Args {
		if i > 0 && w.Lit() == ":=" {
			if _, ok := bashppShortLHS(ce.Args[:i]); ok {
				p.posErr(w.Pos(), "invalid := binding")
			}
			return
		}
	}
	if len(ce.Args) == 1 {
		if _, args, ok := bashppCallTypeArgs(head); ok && len(args) > 0 {
			p.posErr(head.Pos(), "generic instantiation must be used in a call or binding")
		}
	}
}

func bashppKeywordIdentifier(name string) bool {
	switch name {
	case "if", "then", "elif", "else", "fi", "while", "until", "for", "do", "done", "case", "esac", "select", "function", "time", "coproc", "in":
		return !isGoReservedWord(name)
	}
	return false
}

// Probe the existing Go statement handlers before a shell keyword consumes
// its body. Ordinary keyword grammar is restored unchanged on a nonmatch.
func (p *Parser) bashppKeywordStatement(s *Stmt) bool {
	if !bashppKeywordIdentifier(p.val) {
		return false
	}
	txn := p.beginBashPPTxn()
	probe := &Stmt{Position: s.Position}
	name := p.wordOne(p.lit(p.pos, p.val))
	p.next()
	p.callExpr(probe, name, false)
	switch probe.Cmd.(type) {
	case *BashPPShortDecl, *BashPPAssign, *BashPPUpdate, *BashPPIncDec, *BashPPSend:
		if p.err == nil {
			txn.commit(p)
			s.Cmd, s.Redirs = probe.Cmd, probe.Redirs
			return true
		}
	}
	txn.rollback(p)
	return false
}

func (p *Parser) bashppLabel(s *Stmt) bool {
	name, ok := strings.CutSuffix(p.val, ":")
	if !ok || !bashppIsIdent(name) || isGoReservedWord(name) {
		return false
	}
	label := p.lit(p.pos, name)
	colon := posAddCol(p.pos, len(name))
	p.next()
	node := &BashPPLabeled{Label: label, Colon: colon}
	if p.tok != _Newl && p.tok != semicolon && p.tok != _EOF && !p.stopToken() {
		node.Stmt = p.getStmt(false, false, true)
	}
	s.Cmd = node
	return true
}
