package syntax

import (
	"strings"
	"unicode/utf8"
)

// bashppSourceBlock claims only a complete fence header. Near misses return to
// ordinary shell parsing unchanged because this is a Class E start site.
func (p *Parser) bashppSourceBlock() *SourceBlock {
	opener, pos := p.val, p.pos
	n := 0
	for n < len(opener) && opener[n] == '~' {
		n++
	}
	if n < 3 || n == len(opener) {
		return nil
	}
	language := opener[n:]
	if !bashppValidSourceLanguage(language) {
		return nil
	}
	fence := opener[:n]

	var tail strings.Builder
	for p.r != '\n' && p.r != utf8.RuneSelf {
		tail.WriteRune(p.r)
		p.rune()
	}
	rest := tail.String()
	alias, runner, ok := bashppSourceFenceTail(language, strings.Fields(rest))
	if !ok {
		return nil
	}
	langPos := posAddCol(pos, n)
	block := &SourceBlock{
		Fence: fence, FencePos: pos,
		Language: &Lit{ValuePos: langPos, ValueEnd: posAddCol(langPos, len(language)), Value: language},
	}
	if alias != "" {
		// The alias word is the one after "as": either the alias itself or
		// the "!runner" it is short for.
		word := alias
		if i := strings.Index(rest, " as !"+runner); runner != "" && alias == runner && i >= 0 {
			word = "!" + runner
		}
		aliasPos := posAddCol(pos, len(opener)+strings.Index(rest, word))
		block.Alias = &Lit{ValuePos: aliasPos, ValueEnd: posAddCol(aliasPos, len(alias)), Value: alias}
	}
	if runner != "" {
		runnerPos := posAddCol(pos, len(opener)+strings.LastIndex(rest, "!"+runner)+1)
		block.Runner = &Lit{ValuePos: runnerPos, ValueEnd: posAddCol(runnerPos, len(runner)), Value: runner}
	}
	if p.r == utf8.RuneSelf {
		p.posErr(pos, "unclosed source block %q", fence)
		return block
	}

	p.rune()
	block.BodyPos = p.nextPos()
	var body strings.Builder
	for p.r != utf8.RuneSelf {
		linePos := p.nextPos()
		var line strings.Builder
		for p.r != '\n' && p.r != escNewl && p.r != utf8.RuneSelf {
			line.WriteRune(p.r)
			p.rune()
		}
		// The shell lexer folds backslash-newline into a sentinel. Foreign
		// source is raw: restore it before examining the physical line.
		if p.r == escNewl {
			line.WriteByte('\\')
		}
		if line.String() == fence {
			block.Body = body.String()
			block.ClosingPos = linePos
			if alias == "" {
				p.bashppRegisterSourceBlockFuncs(language, block.Body)
			}
			if p.r == '\n' {
				p.tok = _Newl
			} else {
				p.tok = _EOF
			}
			return block
		}
		body.WriteString(line.String())
		if p.r == '\n' || p.r == escNewl {
			body.WriteByte('\n')
			p.rune()
		}
	}
	block.Body = body.String()
	p.tok = _EOF
	p.posErr(pos, "unclosed source block %q", fence)
	return block
}

// bashppRegisterSourceBlockFuncs supplies the parser only with the small bit
// of look-ahead it needs to distinguish a later zero-argument direct call
// from a shell function header. The language's own analyzer remains
// authoritative for declarations, visibility, signatures, and diagnostics.
// The per-language line reader comes from the fence look-ahead table; a
// language without a row (every text fence, which promotes nothing) needs
// none.
func (p *Parser) bashppRegisterSourceBlockFuncs(language, body string) {
	lookahead := bashppFenceLookahead[strings.ToLower(language)]
	if lookahead == nil {
		return
	}
	for _, line := range strings.Split(body, "\n") {
		declaration := lookahead(line)
		if declaration == "" {
			continue
		}
		name, _, ok := strings.Cut(declaration, "(")
		name = strings.TrimSpace(name)
		if ok && !strings.HasPrefix(name, "_") && BashPPValidIdent(name) {
			p.bashppRegisterFunc(name)
		}
	}
}

// A fence language is a lexical identifier, not a Go declaration name. The Go
// adapter therefore legitimately uses the otherwise-reserved word "go", and a
// text format is named the way a Markdown info string names it — `c++`,
// `docker-compose` — so `-` and `+` are admitted after the first character.
func bashppValidSourceLanguage(language string) bool {
	if BashPPValidIdent(language) || language == "go" {
		return true
	}
	if language == "" || !(language[0] == '_' || language[0] >= 'a' && language[0] <= 'z' || language[0] >= 'A' && language[0] <= 'Z') {
		return false
	}
	for i := 1; i < len(language); i++ {
		c := language[i]
		if c == '-' || c == '+' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		return false
	}
	return true
}

// A Go fence may use the natural `go` qualifier promised by the dag front
// door. Keep that one contextual spelling local to source fences; `go`
// remains invalid for every Bash++ declaration and ordinary callable.
func bashppValidSourceAlias(language, alias string) bool {
	return BashPPValidIdent(alias) || strings.EqualFold(language, "go") && alias == "go"
}
