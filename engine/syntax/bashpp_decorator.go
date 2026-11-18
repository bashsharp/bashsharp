// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import "strings"

// bashppDecoratedDecl recognizes an entire decorator stack and its function
// declaration in one transaction. Any unsupported argument, missing separator,
// or non-declaration target restores the ordinary Bash parse byte for byte.
func (p *Parser) bashppDecoratedDecl(s *Stmt) bool {
	if p.tok != _LitWord || !strings.HasPrefix(p.val, "@") || p.spaced || p.r != '(' {
		return false
	}
	txn := p.beginBashPPTxn()
	// Function parsing can register a name before discovering a malformed body.
	// Isolate the map as part of this outer transaction so a fallback cannot
	// leave that speculative name callable later in a recovering parse.
	if p.bashppFuncNames != nil {
		cloned := make(map[string]bool, len(p.bashppFuncNames))
		for name, callable := range p.bashppFuncNames {
			cloned[name] = callable
		}
		p.bashppFuncNames = cloned
	}
	var decorators []*BashPPDecorator
	for {
		text := strings.TrimPrefix(p.val, "@")
		if text == "" || !bashppSelector(text) {
			txn.rollback(p)
			return false
		}
		at := p.pos
		namePos := posAddCol(at, 1)
		name := &Lit{ValuePos: namePos, ValueEnd: posAddCol(namePos, len(text)), Value: text}
		p.next()
		if p.tok != leftParen || p.spaced {
			txn.rollback(p)
			return false
		}
		lparen := p.pos
		p.next()
		args, argNames, ellipsis, ok := p.bashppCallArgs()
		if !ok || ellipsis.IsValid() || p.tok != rightParen {
			txn.rollback(p)
			return false
		}
		rparen := p.pos
		p.next()

		// @n() followed by a compound command is already Bash's definition
		// syntax for a function literally named @n. Look only past newlines,
		// then restore the separator before continuing the decorator parse.
		if len(args) == 0 {
			checkpoint := txn.checkpoint(p)
			for p.got(_Newl) {
			}
			compound := p.startsCompoundCommand()
			txn.rewind(p, checkpoint)
			if compound {
				txn.rollback(p)
				return false
			}
		}

		decorator := &BashPPDecorator{
			At: at, Name: name, Args: args, ArgNames: argNames,
			Lparen: lparen, Rparen: rparen,
		}
		switch p.tok {
		case semicolon:
			decorator.Semicolon = p.pos
			p.next()
			for p.got(_Newl) {
			}
		case _Newl:
			for p.got(_Newl) {
			}
		default:
			txn.rollback(p)
			return false
		}
		decorator.Comments, p.accComs = p.accComs, nil
		decorators = append(decorators, decorator)

		if p.tok == _LitWord && strings.HasPrefix(p.val, "@") && p.r == '(' && !p.spaced {
			continue
		}
		break
	}

	// Parse only the declaration-shaped target. The recursive statement parser
	// is intentional: it keeps typed methods, agentic declarations, both shell
	// function spellings, and their diagnostics on their existing code paths.
	if p.tok != _LitWord {
		txn.rollback(p)
		return false
	}
	probe := &Stmt{Position: p.pos}
	if p.gotStmtPipe(probe, false) == nil || p.err != nil {
		txn.rollback(p)
		return false
	}
	switch decl := probe.Cmd.(type) {
	case *BashPPFuncDecl:
		decl.Decorators = decorators
	case *FuncDecl:
		decl.Decorators = decorators
	default:
		txn.rollback(p)
		return false
	}
	txn.commit(p)
	s.Cmd, s.Redirs = probe.Cmd, probe.Redirs
	return true
}
