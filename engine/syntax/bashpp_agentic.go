package syntax

import "strings"

// BashPPAgenticBlock explicitly enables assistance for statements in the
// current shell. It introduces no variable scope or subprocess.
type BashPPAgenticBlock struct {
	Kw   *Lit
	Body *Block
}

func (b *BashPPAgenticBlock) Pos() Pos   { return b.Kw.Pos() }
func (b *BashPPAgenticBlock) End() Pos   { return b.Body.End() }
func (*BashPPAgenticBlock) commandNode() {}

// bashppAgentic claims only the measured unquoted block prefix (Class E),
// or a definition with an opening parenthesis (Class R). All other command
// uses, including incomplete declaration prefixes, rewind to ordinary shell.
func (p *Parser) bashppAgentic(s *Stmt) bool {
	txn := p.beginBashPPTxn()
	modifier := p.lit(p.pos, p.val)
	p.next()
	if p.tok == _LitWord && p.val == "{" {
		txn.commit(p)
		p.block(s)
		if body, ok := s.Cmd.(*Block); ok {
			s.Cmd = &BashPPAgenticBlock{Kw: modifier, Body: body}
		}
		return true
	}
	if p.tok != _LitWord || (p.val != "func" && p.val != "function") {
		txn.rollback(p)
		return false
	}
	kw := p.lit(p.pos, p.val)
	p.next()
	args := []*Word{p.wordOne(kw)}
	if p.tok != leftParen {
		w := p.getWord()
		if w == nil {
			txn.rollback(p)
			return false
		}
		args = append(args, w)
		// Only the existing generic parameter spelling may extend the name
		// across words. Ordinary near-miss commands stop after one name.
		generic := kw.Value == "func" && strings.Contains(bashppWordText(w), "[")
		for generic && !strings.HasSuffix(bashppWordText(w), "]") && p.tok != leftParen && p.pos.Offset()-modifier.Pos().Offset() < maxLookahead {
			w = p.getWord()
			if w == nil {
				break
			}
			args = append(args, w)
		}
	}
	if p.tok != leftParen {
		txn.rollback(p)
		return false
	}
	if kw.Value == "func" {
		txn.commit(p)
		cmd := p.bashppFuncForm(&CallExpr{Args: args})
		if cmd == nil {
			if p.err == nil {
				p.posErr(kw.Pos(), "agentic func requires a typed function or method declaration")
			}
			return true
		}
		cmd.(*BashPPFuncDecl).Agentic = modifier
		s.Cmd = cmd
		return true
	}
	if len(args) != 2 {
		txn.rollback(p)
		return false
	}
	txn.commit(p)
	name := bashppBareLit(args[1])
	if name == nil {
		p.posErr(args[1].Pos(), "agentic function requires an unquoted literal name")
		return true
	}
	p.next()
	p.follow(kw.Pos(), "agentic function name(", rightParen)
	p.funcDecl(s, kw.Pos(), true, true, name)
	if fd, ok := s.Cmd.(*FuncDecl); ok {
		fd.Agentic = modifier
	}
	return true
}
